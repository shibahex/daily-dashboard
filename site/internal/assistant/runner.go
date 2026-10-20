package assistant

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dashboard/internal/text"
)

// Update is one line the page receives while an answer streams.
type Update struct {
	Type   string `json:"type"` // "text", "break", "tool", "tool_error"
	Text   string `json:"text,omitempty"`
	Label  string `json:"label,omitempty"`
	Detail string `json:"detail,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

// Outcome is how an answer ended.
type Outcome struct {
	Reply        Message
	ChangedDiary bool // a MyFitnessPal tool changed the diary
	ChangedAnki  bool // an Anki tool changed the collection
}

var ErrBusy = errors.New("still answering the last message")

// Runner answers one message at a time.
type Runner struct {
	mu      sync.Mutex
	busy    atomic.Bool
	current *exec.Cmd
	stopped atomic.Bool
}

func NewRunner() *Runner { return &Runner{} }

func (r *Runner) Busy() bool { return r.busy.Load() }

// Stop ends the running answer, if there is one.
func (r *Runner) Stop() {
	if cmd := r.currentCmd(); cmd != nil && cmd.Process != nil {
		r.stopped.Store(true)
		cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (r *Runner) currentCmd() *exec.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Answer sends the message to the provider and streams the answer to send. It adds the message
// and the reply to the conversation (the caller saves it). It keeps going if the page closes,
// so the answer is still saved.
func (r *Runner) Answer(provider Provider, conversation *Conversation, req Request, send func(Update)) (Outcome, error) {
	if !r.busy.CompareAndSwap(false, true) {
		return Outcome{}, ErrBusy
	}
	defer r.busy.Store(false)

	invocation, err := provider.Invocation(req)
	if err != nil {
		return Outcome{}, err
	}
	now := func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	conversation.Messages = append(conversation.Messages, Message{Role: "user", Text: req.Message, At: now()})
	turn := newTurn(provider, req.Model, now())

	cmd := exec.Command(invocation.Args[0], invocation.Args[1:]...)
	cmd.Dir, cmd.Env = invocation.Dir, invocation.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{}, err
	}
	r.stopped.Store(false)
	if err := cmd.Start(); err != nil {
		return Outcome{}, fmt.Errorf("couldn't start %s: %w", provider.Label(), err)
	}
	r.mu.Lock()
	r.current = cmd
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.current = nil; r.mu.Unlock() }()

	lines := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, readErr := lines.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			for _, event := range provider.ParseLine(line) {
				turn.apply(event, conversation, send)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
	waitErr := cmd.Wait()
	turn.finish(r.stopped.Load(), cmd.ProcessState, waitErr, stderr.String())
	conversation.Messages = append(conversation.Messages, turn.reply)
	return Outcome{Reply: turn.reply, ChangedDiary: turn.changedDiary, ChangedAnki: turn.changedAnki}, nil
}

// turn builds the reply while the CLI's events come in.
type turn struct {
	provider     Provider
	reply        Message
	toolIndex    map[string]int // tool id → index in reply.Tools
	newParagraph bool           // the last text block ended
	errorMessage string
	changedDiary bool
	changedAnki  bool
}

func newTurn(provider Provider, model string, at float64) *turn {
	return &turn{
		provider:  provider,
		reply:     Message{Role: "assistant", Provider: provider.ID(), Model: model, At: at, Tools: []Tool{}},
		toolIndex: map[string]int{},
	}
}

func (t *turn) apply(event Event, conversation *Conversation, send func(Update)) {
	switch event.Kind {
	case EventSession:
		if event.Text != "" {
			conversation.SessionID = event.Text
		}
	case EventText:
		if t.newParagraph && t.reply.Text != "" {
			t.reply.Text += "\n\n"
		}
		t.newParagraph = false
		t.reply.Text += event.Text
		send(Update{Type: "text", Text: event.Text})
	case EventBreak:
		t.newParagraph = true
		send(Update{Type: "break"})
	case EventTool:
		tool := Tool{Label: ToolLabel(event.ToolName), Detail: ToolDetail(event.ToolInput)}
		t.toolIndex[event.ToolID] = len(t.reply.Tools)
		t.reply.Tools = append(t.reply.Tools, tool)
		t.changedDiary = t.changedDiary || ChangesDiary(event.ToolName)
		t.changedAnki = t.changedAnki || ChangesAnki(event.ToolName)
		send(Update{Type: "tool", Label: tool.Label, Detail: tool.Detail})
	case EventToolError:
		if i, ok := t.toolIndex[event.ToolID]; ok {
			t.reply.Tools[i].Error = true
			send(Update{Type: "tool_error", Label: t.reply.Tools[i].Label, Detail: t.reply.Tools[i].Detail, Error: true})
		}
	case EventError:
		t.errorMessage = event.Text
	}
}

// finish decides the reply's error: "Stopped", a reported error, or a crash with no answer.
func (t *turn) finish(stopped bool, state *os.ProcessState, waitErr error, stderr string) {
	killedBySignal := false
	exitCode := 0
	if state != nil {
		exitCode = state.ExitCode()
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			killedBySignal = true
		}
	}
	switch {
	case stopped || killedBySignal:
		// CLIs often exit with a code like 143 on SIGTERM rather than dying from the signal.
		t.reply.Error = "Stopped"
	case t.errorMessage != "" || exitCode != 0 || waitErr != nil:
		log.Printf("%s exited %d: %s", t.provider.ID(), exitCode, text.Truncate(stderr, 2000))
		if t.errorMessage != "" || t.reply.Text == "" {
			message := t.errorMessage
			if message == "" {
				message = text.LastLine(stderr, fmt.Sprintf("%s exited with code %d", t.provider.Label(), exitCode))
			}
			t.reply.Error = FriendlyError(t.provider, message)
		}
	}
}

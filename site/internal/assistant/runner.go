package assistant

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"dashboard/internal/text"
)

// Outcome is how an answer went.
type Outcome struct {
	ChangedDiary bool // a MyFitnessPal tool changed the diary
	Stopped      bool
}

// Run sends the request to the provider's CLI, inside the user's sandbox (req.Wrap, required),
// and calls emit with each event as it comes. Cancelling ctx stops the CLI. A failure the user
// should see (not signed in, a crash) comes as an EventError.
func Run(ctx context.Context, provider Provider, req Request, emit func(Event)) (Outcome, error) {
	if req.Wrap == nil {
		return Outcome{}, errors.New("the AI sandbox isn't set up, so the AI can't run")
	}
	invocation, err := provider.Invocation(req)
	if err == nil {
		invocation, err = req.Wrap(invocation)
	}
	if err != nil {
		return Outcome{}, err
	}
	cmd := exec.Command(invocation.Args[0], invocation.Args[1:]...)
	cmd.Dir, cmd.Env = invocation.Dir, invocation.Env
	if invocation.Stdin != "" {
		cmd.Stdin = strings.NewReader(invocation.Stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 64 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{}, err
	}
	if err := cmd.Start(); err != nil {
		return Outcome{}, fmt.Errorf("couldn't start %s: %w", provider.Label(), err)
	}
	// Stopping: SIGTERM, then SIGKILL if it lingers.
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			close(stopped)
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
			}
		case <-finished:
		}
	}()

	var outcome Outcome
	errorMessage, sawText := "", false
	lines := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, readErr := lines.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			for _, event := range provider.ParseLine(line) {
				switch event.Kind {
				case EventError:
					errorMessage = event.Text
					continue
				case EventText:
					sawText = true
				case EventTool:
					outcome.ChangedDiary = outcome.ChangedDiary || ChangesDiary(event.ToolName)
				}
				emit(event)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				io.Copy(io.Discard, stdout)
			}
			break
		}
	}
	waitErr := cmd.Wait()
	close(finished)

	select {
	case <-stopped:
		outcome.Stopped = true
		return outcome, nil
	default:
	}
	exitCode, killed := 0, false
	if state := cmd.ProcessState; state != nil {
		exitCode = state.ExitCode()
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			killed = true
		}
	}
	if errorMessage != "" || exitCode != 0 || killed || waitErr != nil {
		// Only that it failed: its output can hold the user's messages and notes, which don't
		// belong in the server's log. The reason goes to the user instead.
		log.Printf("%s exited %d (%d bytes of error output, not logged)", provider.ID(), exitCode, stderr.Len())
		if errorMessage != "" || !sawText {
			if errorMessage == "" {
				errorMessage = text.LastLine(stderr.String(), fmt.Sprintf("%s exited with code %d", provider.Label(), exitCode))
			}
			emit(Event{Kind: EventError, Text: FriendlyError(provider, errorMessage)})
		}
	}
	return outcome, nil
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

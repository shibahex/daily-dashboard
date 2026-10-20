// Package taskwarrior runs the `task` command and understands its tasks.
package taskwarrior

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Settings for every run: never ask questions, and print nothing extra.
var baseArgs = []string{"rc.confirmation=off", "rc.recurrence.confirmation=no", "rc.verbose=nothing"}

const timeout = 30 * time.Second

// Result is what one `task` run printed.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func (r Result) OK() bool { return r.ExitCode == 0 }

// Error is the trimmed error output.
func (r Result) Error() string { return strings.TrimSpace(r.Stderr) }

// Client runs Taskwarrior. Runs happen one at a time: every run creates any missing copies
// of repeating tasks, and two at once would both create the same copy.
type Client struct {
	mu  sync.Mutex
	env []string // TASKRC/TASKDATA (and TIMEWARRIORDB for the hook); nil = this computer's own
}

func NewClient() *Client { return &Client{} }

// NewClientWithEnv runs `task` with these extra environment variables: a user's own taskrc,
// task data and Timewarrior database.
func NewClientWithEnv(env []string) *Client { return &Client{env: env} }

// Run runs `task <args>` with no input, so a question makes it fail instead of hang.
func (c *Client) Run(args ...string) Result { return c.run("", args) }

// RunWithInput runs `task <args>` with the given text on stdin (for `task import -`).
func (c *Client) RunWithInput(input string, args ...string) Result { return c.run(input, args) }

func (c *Client) run(input string, args []string) Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "task", append(append([]string{}, baseArgs...), args...)...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	if len(c.env) > 0 {
		cmd.Env = append(os.Environ(), c.env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		result.ExitCode = exitErr.ExitCode()
	default:
		result.ExitCode = 127
		result.Stderr = "Taskwarrior (task) isn't installed or couldn't run: " + err.Error()
	}
	return result
}

// Export returns `task <filter> export`: the matching tasks as a JSON array.
func (c *Client) Export(filter ...string) Result {
	return c.Run(append(filter, "export")...)
}

// Tasks exports and decodes the matching tasks.
func (c *Client) Tasks(filter ...string) ([]Task, Result) {
	result := c.Export(filter...)
	if !result.OK() {
		return nil, result
	}
	return DecodeTasks(result.Stdout), result
}

// Find returns one task by uuid.
func (c *Client) Find(uuid string) (Task, bool) {
	tasks, result := c.Tasks(uuid)
	if !result.OK() || len(tasks) == 0 {
		return nil, false
	}
	return tasks[0], true
}

// DecodeTasks parses `task export` output. Numbers stay exact (json.Number), so tasks can be
// re-imported unchanged.
func DecodeTasks(exported string) []Task {
	var tasks []Task
	decoder := json.NewDecoder(strings.NewReader(exported))
	decoder.UseNumber()
	decoder.Decode(&tasks)
	return tasks
}

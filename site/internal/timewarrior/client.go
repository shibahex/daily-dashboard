// Package timewarrior runs the `timew` command: tracked time as intervals with tags.
package timewarrior

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"dashboard/internal/text"
)

const timeout = 15 * time.Second

// ExitNotInstalled is the exit code reported when timew can't run at all.
const ExitNotInstalled = 127

// Result is what one `timew` run printed.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func (r Result) OK() bool { return r.ExitCode == 0 }

// Error is the trimmed error output.
func (r Result) Error() string { return strings.TrimSpace(r.Stderr) }

// FirstErrorLine is the first line of the error output (timew's are long).
func (r Result) FirstErrorLine(fallback string) string { return text.FirstLine(r.Stderr, fallback) }

// Interval is one tracked stretch of time, as `timew export` prints it.
type Interval struct {
	ID    int      `json:"id"`
	Start string   `json:"start"` // 20261020T140000Z
	End   string   `json:"end,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

type Client struct{}

func NewClient() Client { return Client{} }

// Run runs `timew <args> :yes`, so it never stops to ask.
func (Client) Run(args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "timew", append(args, ":yes")...)
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
		result.ExitCode = ExitNotInstalled
		result.Stderr = "Timewarrior isn't available (start the dashboard with nix run)"
	}
	return result
}

// Export runs `timew export <range>`; the intervals stay raw JSON so every field passes through.
func (c Client) Export(rangeArgs ...string) (json.RawMessage, Result) {
	result := c.Run(append([]string{"export"}, rangeArgs...)...)
	if !result.OK() {
		return nil, result
	}
	exported := json.RawMessage(strings.TrimSpace(result.Stdout))
	if len(exported) == 0 {
		exported = json.RawMessage("[]")
	}
	return exported, result
}

// StillThere reports whether interval @id still starts at startWas. Timewarrior ids shift as
// entries are added, so an edit must check the id still means the same entry.
func (c Client) StillThere(id int, startWas string) bool {
	exported, result := c.Export()
	if !result.OK() {
		return false
	}
	var intervals []Interval
	json.Unmarshal(exported, &intervals)
	for _, interval := range intervals {
		if interval.ID == id && interval.Start == startWas {
			return true
		}
	}
	return false
}

// rangeWords are words timew reads as part of a time range, so they can't be tags.
var rangeWords = map[string]bool{"-": true, "from": true, "to": true, "for": true, "before": true,
	"after": true, "since": true, "until": true, "ago": true, "in": true, "on": true}

// CleanTags checks tags for `timew track` and `retag`: 1-10 of them, each up to 100 characters,
// not looking like a hint (:x), an id (@1) or a range word.
func CleanTags(tags []string) ([]string, error) {
	if len(tags) == 0 || len(tags) > 10 {
		return nil, errors.New("add at least one tag (usually the task name)")
	}
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = text.Collapse(tag)
		if tag == "" || len([]rune(tag)) > 100 || strings.ContainsAny(tag[:1], ":@") || rangeWords[strings.ToLower(tag)] {
			if tag == "" {
				tag = "(empty)"
			}
			return nil, fmt.Errorf("bad tag: %s", tag)
		}
		cleaned = append(cleaned, tag)
	}
	return cleaned, nil
}

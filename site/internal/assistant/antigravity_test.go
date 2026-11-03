package assistant

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The test binary is also the tool gate agy's hook runs (`<test binary> agy-gate <rules>`).
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == AgyGateCommand {
		AgyGate(os.Args[2], os.Stdin, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

// TestAntigravityRunsWithTheGate runs the real agy against a stand-in for Google's API that asks
// for tools: what the policy doesn't allow must be refused by the gate, and the rest must work.
// It needs agy: AGY_PROGRAM=$(nix build --impure nixpkgs#antigravity-cli --print-out-paths)/bin/agy
// (with NIXPKGS_ALLOW_UNFREE=1).
func TestAntigravityRunsWithTheGate(t *testing.T) {
	program := os.Getenv("AGY_PROGRAM")
	if program == "" {
		t.Skip("set AGY_PROGRAM to run Antigravity CLI")
	}
	var calls [][]map[string]any // set below, once the workspace exists
	var mu sync.Mutex
	var answers []string // what agy reported back for each tool call
	next := 0
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			SystemInstruction any `json:"systemInstruction"`
			Contents          []struct {
				Parts []struct {
					FunctionResponse *struct {
						Name     string         `json:"name"`
						Response map[string]any `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
		}
		json.Unmarshal(body, &req)
		parts := []map[string]any{{"text": "Title"}}
		system, _ := json.Marshal(req.SystemInstruction)
		mu.Lock()
		if strings.Contains(r.URL.Path, "streamGenerateContent") && !strings.Contains(string(system), "title generator") {
			if n := len(req.Contents); n > 0 {
				for _, p := range req.Contents[n-1].Parts {
					if p.FunctionResponse != nil {
						answers = append(answers, p.FunctionResponse.Name+": "+fmt.Sprint(p.FunctionResponse.Response["output"]))
					}
				}
			}
			parts = calls[min(next, len(calls)-1)]
			next++
		}
		mu.Unlock()
		reply, _ := json.Marshal(map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2},
		})
		if r.URL.Query().Get("alt") == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\r\n\r\n", reply)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	}))
	defer google.Close()

	self, _ := os.Executable()
	home, workspace := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("eat more greens"), 0o600)
	calls = [][]map[string]any{
		{{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "touch SHELL_RAN", "toolSummary": "t", "WaitMsBeforeAsync": 1000}}}},
		{{"functionCall": map[string]any{"name": "search_web", "args": map[string]any{"query": "weather"}}}},
		{{"functionCall": map[string]any{"name": "write_file", "args": map[string]any{"Path": filepath.Join(workspace, "WROTE.md"), "Content": "x"}}}},
		{{"functionCall": map[string]any{"name": "read_file", "args": map[string]any{"Path": filepath.Join(workspace, "notes.md")}}}},
		{{"text": "all done"}},
	}
	a := newAntigravity()
	req := Request{Message: "go", WorkDir: t.TempDir(), Home: home, Self: self, Caps: Capabilities{Read: true}}
	inv, err := a.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inv.Mounts { // what the sandbox's read-only mounts do
		os.MkdirAll(filepath.Dir(m.Inner), 0o700)
		data, _ := os.ReadFile(m.Host)
		os.WriteFile(m.Inner, data, 0o400)
	}
	args := append([]string{program}, inv.Args[1:]...)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = workspace
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GEMINI_API_KEY=test", "GOOGLE_GEMINI_BASE_URL=" + google.URL}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agy: %v\n%s", err, stderr.String())
	}
	var events []Event
	lines := bufio.NewScanner(bytes.NewReader(out))
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		events = append(events, a.ParseLine(lines.Bytes())...)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(answers) != 4 {
		t.Fatalf("tool answers: %q\nstdout: %s\nstderr: %s", answers, out, stderr.String())
	}
	for i, tool := range []string{"run_command", "search_web", "write_file"} {
		if !strings.HasPrefix(answers[i], tool+":") || !strings.Contains(answers[i], "denied by pre-tool hook") {
			t.Errorf("%s should be refused by the gate: %s", tool, answers[i])
		}
	}
	if !strings.HasPrefix(answers[3], "read_file:") || !strings.Contains(answers[3], "eat more greens") {
		t.Errorf("read_file should work: %s", answers[3])
	}
	for _, name := range []string{"SHELL_RAN", "WROTE.md"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); err == nil {
			t.Errorf("%s exists: a refused tool ran", name)
		}
	}
	var text string
	for _, e := range events {
		switch e.Kind {
		case EventText:
			text += e.Text
		case EventError:
			t.Errorf("error event: %s", e.Text)
		}
	}
	if !strings.Contains(text, "all done") || events[0].Kind != EventSession {
		t.Errorf("events: %+v", events)
	}
}

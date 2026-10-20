package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dashboard/internal/mcp"
)

func testRequest(t *testing.T) Request {
	return Request{
		Message:      "--dangerously-skip-permissions log an apple",
		Instructions: "instructions",
		WorkDir:      t.TempDir(),
		Servers: []NamedServer{
			{Name: "myfitnesspal", Server: mcp.Server{Command: "mfp"}},
			{Name: "anki", Server: mcp.Server{Command: "anki-mcp"}},
		},
	}
}

// afterFlag is the value that follows flag in args.
func afterFlag(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return "(missing)"
}

func TestClaudeGetsNoBuiltInToolsAndTheMessageLast(t *testing.T) {
	c := newClaude(nil)
	c.program = "sh" // any program on the PATH
	req := testRequest(t)
	inv, err := c.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := inv.Args
	if afterFlag(args, "--tools") != "" || afterFlag(args, "--permission-mode") != "dontAsk" {
		t.Errorf("built-in tools or permission mode not locked down: %q", args)
	}
	for _, flag := range []string{"--restricted", "--strict-mcp-config"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != req.Message {
		t.Errorf("the message must come last, after --: %q", args[len(args)-2:])
	}
	if !slices.Contains(args, "mcp__anki__storeMediaFile") || slices.Contains(args, "Read") {
		t.Errorf("tool lists wrong: %q", args)
	}
	if inv.Dir != req.WorkDir {
		t.Errorf("runs in %q, want the work dir", inv.Dir)
	}
}

func TestGeminiGetsNoBuiltInToolsAndAnInlinePrompt(t *testing.T) {
	g := newGemini()
	g.program = "sh"
	req := testRequest(t)
	inv, err := g.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.Args, "--prompt="+firstMessage(req)) || afterFlag(inv.Args, "-e") != "none" {
		t.Errorf("prompt or extensions wrong: %q", inv.Args)
	}
	if slices.Contains(inv.Args, "--include-directories") {
		t.Errorf("the agent mustn't see the repo: %q", inv.Args)
	}

	var settings struct {
		Tools struct {
			Core    []string `json:"core"`
			Exclude []string `json:"exclude"`
		} `json:"tools"`
		MCPServers map[string]struct {
			ExcludeTools []string `json:"excludeTools"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(req.WorkDir, "gemini-settings.json"))
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Tools.Core == nil || len(settings.Tools.Core) != 0 || !slices.Contains(settings.Tools.Exclude, "web_fetch") {
		t.Errorf("built-in tools not off: %+v", settings.Tools)
	}
	if !slices.Contains(settings.MCPServers["anki"].ExcludeTools, "storeMediaFile") {
		t.Errorf("anki tools not limited: %+v", settings.MCPServers)
	}

	req.Servers = nil
	inv, _ = g.Invocation(req)
	if afterFlag(inv.Args, "--allowed-mcp-server-names") != "none" {
		t.Errorf("with no servers on, the user's own must stay off: %q", inv.Args)
	}
}

func TestCodexIsOfflineAndReadsTheMessageFromStdin(t *testing.T) {
	c := newCodex()
	c.program = "sh"
	req := testRequest(t)
	inv, err := c.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inv.Args, " ")
	for _, want := range []string{`web_search="disabled"`, "features.browser_use=false", "features.computer_use=false",
		"features.apps=false", "--ignore-user-config", `mcp_servers.anki.disabled_tools=["storeMediaFile"]`, "-s read-only"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s", want)
		}
	}
	if inv.Args[len(inv.Args)-1] != "-" || inv.Stdin != firstMessage(req) || strings.Contains(joined, req.Message) {
		t.Errorf("the message must go on stdin: %q", inv.Args)
	}

	req.SessionID = "01a15a87-aa98-7053-ad6a-c9e0a989ec37"
	inv, _ = c.Invocation(req)
	if got := inv.Args[len(inv.Args)-3:]; !slices.Equal(got, []string{"resume", req.SessionID, "-"}) {
		t.Errorf("resume: %q", got)
	}
}

func TestContextOnlyReadsTheUsersOwnFiles(t *testing.T) {
	user := t.TempDir()
	os.MkdirAll(filepath.Join(user, "docs"), 0o700)
	os.WriteFile(filepath.Join(user, "docs", "overview.md"), []byte("Goal: 1 lb a week"), 0o600)
	os.WriteFile(filepath.Join(user, "plan.json"), []byte(`{"checklist": ["Weigh in"]}`), 0o600)
	os.WriteFile(filepath.Join(user, "weights.json"), []byte(`{"2026-10-20": 229}`), 0o600)
	os.WriteFile(filepath.Join(user, "secrets.json"), []byte("api key"), 0o600)

	context := Context(filepath.Join(user, "docs"), filepath.Join(user, "plan.json"), user)
	for _, want := range []string{"Goal: 1 lb a week", "Weigh in", "229", "=== notes/overview.md ==="} {
		if !strings.Contains(context, want) {
			t.Errorf("missing %q: %q", want, context)
		}
	}
	if strings.Contains(context, "api key") {
		t.Errorf("other files leaked in: %q", context)
	}
}

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
		"features.apps=false", "--ignore-user-config", `mcp_servers.anki.disabled_tools=["storeMediaFile"]`, "-s danger-full-access"} {
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

func TestCapabilitiesPickTheTools(t *testing.T) {
	none, all := Capabilities{}, Capabilities{Web: true, Read: true, Edit: true}
	if len(claudeTools(none)) != 0 || len(geminiTools(none)) != 0 {
		t.Fatal("no capabilities: no built-in tools")
	}
	if got := claudeTools(Capabilities{Read: true}); slices.Contains(got, "Write") || slices.Contains(got, "WebFetch") || !slices.Contains(got, "Read") {
		t.Fatalf("read only: %v", got)
	}
	if got := claudeTools(all); !slices.Contains(got, "Edit") || !slices.Contains(got, "WebSearch") || slices.Contains(got, "Bash") {
		t.Fatalf("everything (but never the shell): %v", got)
	}
	if slices.Contains(geminiTools(all), "run_shell_command") {
		t.Fatal("Gemini never gets its shell")
	}

	req := testRequest(t)
	req.Caps = Capabilities{Read: true, Web: true}
	req.InnerWorkDir = "/home/agent/run-config"
	c := newClaude(nil)
	c.program = "sh"
	inv, _ := c.Invocation(req)
	if afterFlag(inv.Args, "--tools") != "Read,Glob,Grep,WebSearch,WebFetch" {
		t.Fatalf("claude tools: %q", afterFlag(inv.Args, "--tools"))
	}
	if afterFlag(inv.Args, "--mcp-config") != "/home/agent/run-config/claude-mcp.json" {
		t.Fatalf("the CLI must get the path it sees inside the sandbox: %q", afterFlag(inv.Args, "--mcp-config"))
	}
	if !slices.Contains(inv.Args, "WebFetch") || !slices.Contains(inv.Args, "mcp__anki") {
		t.Fatal("allowed tools must include the built-ins it was given")
	}

	cx := newCodex()
	cx.program = "sh"
	inv, _ = cx.Invocation(req)
	if !strings.Contains(strings.Join(inv.Args, " "), `web_search="live"`) {
		t.Fatal("codex web search should follow the policy")
	}
}

type fakeProvider struct{ cli }

func (fakeProvider) Invocation(Request) (Invocation, error) {
	return Invocation{Args: []string{"true"}}, nil
}
func (fakeProvider) ParseLine([]byte) []Event { return nil }

func TestTheRunnerNeedsTheSandbox(t *testing.T) {
	req := testRequest(t)
	if _, err := NewRunner().Answer(fakeProvider{}, &Conversation{}, req, func(Message) {}); err == nil {
		t.Fatal("without a sandbox the agent must not run")
	}
	wrapped := false
	req.Wrap = func(inv Invocation) (Invocation, error) { wrapped = true; return inv, nil }
	if _, err := NewRunner().Answer(fakeProvider{}, &Conversation{}, req, func(Message) {}); err != nil || !wrapped {
		t.Fatalf("with a sandbox it runs, wrapped: %v %v", err, wrapped)
	}
}

func TestClaudeSubscriptionToken(t *testing.T) {
	if got := APIKeys["claude"].KeyEnv("sk-ant-oat01-abc"); !slices.Equal(got, []string{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-abc"}) {
		t.Fatalf("subscription token: %v", got)
	}
	if got := APIKeys["claude"].KeyEnv("sk-ant-api03-abc"); !slices.Equal(got, []string{"ANTHROPIC_API_KEY=sk-ant-api03-abc"}) {
		t.Fatalf("API key: %v", got)
	}
}

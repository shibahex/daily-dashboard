package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestClaudeGetsNoBuiltInToolsAndTheMessageOnStdin(t *testing.T) {
	c := newClaude()
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
	if afterFlag(args, "--input-format") != "stream-json" || slices.Contains(args, req.Message) {
		t.Errorf("the message must go on stdin, not in the arguments: %q", args)
	}
	var input struct {
		Type    string
		Message struct {
			Role    string
			Content []map[string]any
		}
	}
	if err := json.Unmarshal([]byte(inv.Stdin), &input); err != nil || input.Type != "user" || input.Message.Role != "user" ||
		len(input.Message.Content) != 1 || input.Message.Content[0]["text"] != req.Message {
		t.Errorf("stdin %q: %v", inv.Stdin, err)
	}
	if !slices.Contains(args, "mcp__anki__storeMediaFile") || slices.Contains(args, "Read") {
		t.Errorf("tool lists wrong: %q", args)
	}
	if inv.Dir != req.WorkDir {
		t.Errorf("runs in %q, want the work dir", inv.Dir)
	}
}

func TestAntigravityGetsItsConfigReadOnlyAndAGate(t *testing.T) {
	a := newAntigravity()
	a.program = "sh"
	req := testRequest(t)
	req.Home, req.Self, req.InnerWorkDir = "/home/agent", "/nix/store/x-dashboard/bin/dashboard", "/home/agent/run-config"
	inv, err := a.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inv.Args, "--prompt="+firstMessage(req)) || !slices.Contains(inv.Args, "--disable-slash-commands") {
		t.Errorf("prompt or slash commands wrong: %q", inv.Args)
	}
	if slices.Contains(inv.Args, "--dangerously-skip-permissions") || slices.Contains(inv.Args, "--add-dir") {
		t.Errorf("flags the agent mustn't get: %q", inv.Args)
	}
	inner := map[string]string{}
	for _, m := range inv.Mounts {
		inner[m.Inner] = m.Host
	}
	read := func(inside string, v any) {
		host, ok := inner["/home/agent/"+inside]
		if !ok {
			t.Fatalf("%s isn't mounted: %+v", inside, inv.Mounts)
		}
		data, _ := os.ReadFile(host)
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
	}
	var settings struct {
		ModelProvider string `json:"modelProvider"`
		Permissions   struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	read(agySettings, &settings)
	if settings.ModelProvider != "gemini" || !slices.Contains(settings.Permissions.Deny, "command(*)") || !slices.Contains(settings.Permissions.Deny, "read_url(*)") {
		t.Errorf("settings: %+v", settings)
	}
	var mcpConfig struct {
		MCPServers map[string]struct {
			DisabledTools []string `json:"disabledTools"`
		} `json:"mcpServers"`
	}
	read(agyMCPConfig, &mcpConfig)
	if !slices.Contains(mcpConfig.MCPServers["anki"].DisabledTools, "storeMediaFile") {
		t.Errorf("anki tools not limited: %+v", mcpConfig)
	}
	var hooks map[string]map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	read(agyHooks, &hooks)
	gate := hooks["dashboard-gate"]["PreToolUse"]
	if len(gate) != 1 || gate[0].Matcher != "*" || gate[0].Hooks[0].Command != "'/nix/store/x-dashboard/bin/dashboard' agy-gate '/home/agent/run-config/agy-gate.json'" {
		t.Errorf("gate hook: %+v", hooks)
	}

	req.SessionID = "7d634ff2-f4ba-40b7-9341-b1fa13f17484"
	inv, _ = a.Invocation(req)
	if !slices.Contains(inv.Args, "--conversation="+req.SessionID) {
		t.Errorf("resume: %q", inv.Args)
	}
	req.Self = ""
	if _, err := a.Invocation(req); err == nil {
		t.Error("no gate, no run")
	}
}

func TestAgyGateAllowsOnlyWhatThePolicyDoes(t *testing.T) {
	a := newAntigravity()
	a.program = "sh"
	req := testRequest(t)
	req.Home, req.Self, req.Caps = "/home/agent", "/dashboard", Capabilities{Read: true}
	if _, err := a.Invocation(req); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(req.WorkDir, "agy-gate.json")
	decide := func(call string) string {
		var out bytes.Buffer
		AgyGate(rules, strings.NewReader(call), &out)
		var answer struct{ Decision string }
		json.Unmarshal(out.Bytes(), &answer)
		return answer.Decision
	}
	for call, want := range map[string]string{
		`{"toolCall":{"name":"read_file","args":{"Path":"notes/a.md"}}}`:                                                  "allow",
		`{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"anki","ToolName":"findNotes","Arguments":"{}"}}}`:      "allow",
		`{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"anki","ToolName":"storeMediaFile","Arguments":"{}"}}}`: "deny",
		`{"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"other","ToolName":"x"}}}`:                              "deny",
		`{"toolCall":{"name":"read_resource","args":{"ServerName":"other","Uri":"x"}}}`:                                   "deny",
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl evil"}}}`:                                          "deny",
		`{"toolCall":{"name":"write_file","args":{"Path":"notes/a.md"}}}`:                                                 "deny",
		`{"toolCall":{"name":"search_web","args":{"query":"x"}}}`:                                                         "deny",
		`{"toolCall":{"name":"schedule","args":{"Prompt":"x"}}}`:                                                          "deny",
		`{"toolCall":{"name":"invoke_subagent","args":{}}}`:                                                               "deny",
		`not json`: "deny",
	} {
		if got := decide(call); got != want {
			t.Errorf("%s: %s, want %s", call, got, want)
		}
	}
	var out bytes.Buffer
	AgyGate(filepath.Join(req.WorkDir, "missing.json"), strings.NewReader(`{"toolCall":{"name":"read_file"}}`), &out)
	if !strings.Contains(out.String(), "deny") {
		t.Errorf("no rules, no tools: %s", out.String())
	}
}

func TestAntigravityEvents(t *testing.T) {
	a := newAntigravity()
	lines := []string{
		`{"event":"init","conversation_id":"c1","init":{"tools":["run_command"]}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Hel"}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"lo"}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"myfitnesspal","ToolName":"fitness_log_food","Arguments":"{\"query\":\"apple\"}"}}}}`,
		`{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"a.md"},"error":{"type":"x","message":"denied"}}}}`,
		`{"event":"result","result":{"status":"ERROR","error":"API key not valid"}}`,
	}
	var got []Event
	for _, line := range lines {
		got = append(got, a.ParseLine([]byte(line))...)
	}
	if len(got) != 8 || got[0].Kind != EventSession || got[0].Text != "c1" || got[1].Text != "Hel" || got[2].Text != "lo" || got[3].Kind != EventBreak {
		t.Fatalf("events: %+v", got)
	}
	if got[4].ToolName != "mcp__myfitnesspal__fitness_log_food" || got[4].ToolInput["query"] != "apple" || !ChangesDiary(got[4].ToolName) {
		t.Errorf("mcp call: %+v", got[4])
	}
	if got[5].ToolName != "read_file" || got[6].Kind != EventToolError || got[6].ToolID != "3" || got[7].Kind != EventError || got[7].Text != "API key not valid" {
		t.Errorf("tool error and result: %+v", got[5:])
	}
}

func TestAttachmentsReachClaudeAndCodex(t *testing.T) {
	req := testRequest(t)
	req.InnerWorkDir = "/home/agent/run-config"
	req.Attachments = []Attachment{
		{Kind: "image", Name: "meal.jpg", MediaType: "image/jpeg", Data: []byte("jpeg")},
		{Kind: "pdf", Name: "label.pdf", MediaType: "application/pdf", Data: []byte("%PDF")},
	}
	c := newClaude()
	c.program = "sh"
	inv, err := c.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"image"`, `"media_type":"image/jpeg","type":"base64"`, `"title":"label.pdf"`, `"type":"document"`, `"data":"anBlZw=="`} {
		if !strings.Contains(inv.Stdin, want) {
			t.Errorf("claude input lacks %s: %s", want, inv.Stdin)
		}
	}

	req.Attachments = req.Attachments[:1]
	x := newCodex()
	x.program = "sh"
	inv, err = x.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.Args[len(inv.Args)-2:]; !slices.Equal(got, []string{"--image=/home/agent/run-config/attachments/1.jpg", "-"}) {
		t.Errorf("codex image: %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(req.WorkDir, AttachmentDir, "1.jpg")); string(data) != "jpeg" {
		t.Errorf("image file holds %q", data)
	}
	req.SessionID = "01a15a87"
	inv, _ = x.Invocation(req)
	if got := inv.Args[len(inv.Args)-4:]; !slices.Equal(got, []string{"resume", "--image=/home/agent/run-config/attachments/1.jpg", "01a15a87", "-"}) {
		t.Errorf("codex resume with an image: %q", got)
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

func TestCapabilitiesPickTheTools(t *testing.T) {
	none, all := Capabilities{}, Capabilities{Web: true, Read: true, Edit: true}
	if len(claudeTools(none)) != 0 || len(agyTools(none)) != 0 {
		t.Fatal("no capabilities: no built-in tools")
	}
	if got := claudeTools(Capabilities{Read: true}); slices.Contains(got, "Write") || slices.Contains(got, "WebFetch") || !slices.Contains(got, "Read") {
		t.Fatalf("read only: %v", got)
	}
	if got := claudeTools(all); !slices.Contains(got, "Edit") || !slices.Contains(got, "WebSearch") || slices.Contains(got, "Bash") {
		t.Fatalf("everything (but never the shell): %v", got)
	}
	if slices.Contains(agyTools(all), "run_command") {
		t.Fatal("Antigravity never gets its shell")
	}

	req := testRequest(t)
	req.Caps = Capabilities{Read: true, Web: true}
	req.InnerWorkDir = "/home/agent/run-config"
	c := newClaude()
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
	if _, err := Run(context.Background(), fakeProvider{}, req, func(Event) {}); err == nil {
		t.Fatal("without a sandbox the agent must not run")
	}
	wrapped := false
	req.Wrap = func(inv Invocation) (Invocation, error) { wrapped = true; return inv, nil }
	if _, err := Run(context.Background(), fakeProvider{}, req, func(Event) {}); err != nil || !wrapped {
		t.Fatalf("with a sandbox it runs, wrapped: %v %v", err, wrapped)
	}
}

// A CLI that fails says why, as an error event, without its output reaching the log.
func TestTheRunnerReportsAFailure(t *testing.T) {
	req := testRequest(t)
	req.Wrap = func(Invocation) (Invocation, error) {
		return Invocation{Args: []string{"sh", "-c", "echo 'Error: invalid api key' >&2; exit 3"}}, nil
	}
	var events []Event
	if _, err := Run(context.Background(), fakeProvider{cli{label: "Codex", signIn: "Add a key."}}, req, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventError || events[0].Text != "Codex isn't signed in. Add a key." {
		t.Fatalf("events: %+v", events)
	}
}

// Stopping ends the CLI and reports it stopped, not failed.
func TestTheRunnerStops(t *testing.T) {
	req := testRequest(t)
	req.Wrap = func(Invocation) (Invocation, error) { return Invocation{Args: []string{"sleep", "30"}}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	outcome, err := Run(ctx, fakeProvider{}, req, func(e Event) { t.Errorf("unexpected event %+v", e) })
	if err != nil || !outcome.Stopped || time.Since(start) > 10*time.Second {
		t.Fatalf("outcome %+v, err %v", outcome, err)
	}
}

func TestKeysGoWhereEachCLILooks(t *testing.T) {
	for _, c := range []struct {
		provider, key string
		want          []string
	}{
		{"claude-code", "sk-ant-oat01-abc", []string{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-abc"}},
		{"claude-code", "sk-ant-api03-abc", []string{"ANTHROPIC_API_KEY=sk-ant-api03-abc"}},
		{"codex", "sk-proj", []string{"OPENAI_API_KEY=sk-proj", "CODEX_API_KEY=sk-proj"}},
		{"gemini", "AIza", []string{"GEMINI_API_KEY=AIza"}},
		{"gemini", "", nil},
	} {
		if got := KeyEnv(c.provider, c.key); !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %v", c.provider, c.key, got)
		}
	}
}

func TestNoInstructionsSendsTheBareCLI(t *testing.T) {
	c := newClaude()
	c.program = "sh"
	req := testRequest(t)
	req.Instructions = ""
	inv, err := c.Invocation(req)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(inv.Args, "--append-system-prompt") {
		t.Errorf("no instructions should send no system prompt: %q", inv.Args)
	}
	if got := firstMessage(req); got != req.Message {
		t.Errorf("first message = %q, want just the message", got)
	}
}

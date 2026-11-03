package assistant

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"dashboard/internal/problem"
)

// antigravity runs Google's Antigravity CLI (`agy -p`, stream-json output), which replaced
// Gemini CLI. Its id stays "gemini", which users' settings, policies and saved keys use.
//
// agy reads its settings, MCP servers and hooks only from fixed places under ~/.gemini, which
// in the sandbox is the agent's own home: the dashboard's copies are mounted read-only over them
// (Invocation.Mounts), so nothing the agent leaves there changes them. Its built-in tools can't be
// turned off one by one, so a PreToolUse hook (the dashboard's own `agy-gate`) refuses every tool
// the user's policy doesn't allow, and MCP tools the dashboard blocks.
type antigravity struct{ cli }

func newAntigravity() *antigravity {
	return &antigravity{cli{
		id: "gemini", label: "Antigravity CLI", program: "agy", nixPackage: "nixpkgs#antigravity-cli", unfree: true,
		signIn: "Add a Gemini API key on the Account page.",
		// agy takes only its own model names (`agy models`), each a model and its effort.
		models: []Model{
			{ID: "gemini-4-argon-high", Label: "Gemini 4 Argon (High)"},
			{ID: "gemini-4-argon-medium", Label: "Gemini 4 Argon (Medium)"},
			{ID: "gemini-4-argon-low", Label: "Gemini 4 Argon (Low)"},
			{ID: "gemini-3.8-flash-high", Label: "Gemini 3.8 Flash (High)"},
			{ID: "gemini-3.8-flash-medium", Label: "Gemini 3.8 Flash (Medium)"},
			{ID: "gemini-3.8-flash-low", Label: "Gemini 3.8 Flash (Low)"},
			{ID: "gemini-3.1-pro-high", Label: "Gemini 3.1 Pro (High)"},
			{ID: "gemini-3.1-pro-low", Label: "Gemini 3.1 Pro (Low)"},
		},
	}}
}

// Where agy reads its files, under the agent's home.
const (
	agySettings  = ".gemini/antigravity-cli/settings.json"
	agyMCPConfig = ".gemini/config/mcp_config.json"
	agyHooks     = ".gemini/config/hooks.json"
)

// AgyGateCommand is the dashboard's subcommand that agy's hook runs: `dashboard agy-gate <rules>`.
const AgyGateCommand = "agy-gate"

func (a *antigravity) Invocation(req Request) (Invocation, error) {
	program := a.command()
	if program == nil {
		return Invocation{}, problem.Unavailablef("Antigravity CLI isn't installed")
	}
	if req.Self == "" || req.Home == "" {
		return Invocation{}, problem.Unavailablef("Antigravity CLI needs the dashboard's tool gate, which isn't set up here")
	}
	if err := os.MkdirAll(req.WorkDir, 0o700); err != nil {
		return Invocation{}, err
	}

	servers := map[string]any{}
	gate := agyRules{Tools: agyTools(req.Caps), Servers: map[string][]string{}}
	for _, s := range req.Servers {
		server := map[string]any{"command": s.Server.Command, "args": s.Server.Args}
		if len(s.Server.Env) > 0 {
			server["env"] = s.Server.Env
		}
		if blocked := BlockedTools[s.Name]; len(blocked) > 0 {
			server["disabledTools"] = blocked
		}
		servers[s.Name] = server
		gate.Servers[s.Name] = append([]string{}, BlockedTools[s.Name]...)
	}

	// Every tool runs without asking (print mode can't ask); the gate decides. The deny rules
	// say the same again for what the permission system covers.
	deny := []string{"command(*)", "unsandboxed(*)", "execute_url(*)"}
	if !req.Caps.Web {
		deny = append(deny, "read_url(*)")
	}
	if !req.Caps.Edit {
		deny = append(deny, "write_file(*)")
	}
	if !req.Caps.Read {
		deny = append(deny, "read_file(*)")
	}
	settings := map[string]any{
		"modelProvider":           "gemini", // the user's GEMINI_API_KEY, not a Google account
		"toolPermission":          "always-proceed",
		"permissions":             map[string]any{"deny": deny},
		"allowNonWorkspaceAccess": false,
		"disableSlashCommands":    true,
		"enableTelemetry":         false,
		"showFeedbackSurvey":      false,
		"showTips":                false,
	}
	gatePath := filepath.Join(req.WorkDir, "agy-gate.json")
	hook := shellQuote(req.Self) + " " + AgyGateCommand + " " + shellQuote(req.inside(gatePath))
	hooks := map[string]any{"dashboard-gate": map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{
			map[string]any{"type": "command", "command": hook, "timeout": 10},
		}}},
	}}
	files := []struct {
		name, inner string
		value       any
	}{
		{"agy-settings.json", agySettings, settings},
		{"agy-mcp.json", agyMCPConfig, map[string]any{"mcpServers": servers}},
		{"agy-hooks.json", agyHooks, hooks},
		{"agy-gate.json", "", gate},
	}
	var mounts []FileMount
	for _, f := range files {
		path := filepath.Join(req.WorkDir, f.name)
		data, _ := json.Marshal(f.value)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return Invocation{}, err
		}
		if f.inner != "" {
			mounts = append(mounts, FileMount{Host: path, Inner: filepath.Join(req.Home, f.inner)})
		}
	}

	// --prompt=… keeps a message that starts with "-" from being read as a flag, and slash
	// commands are off so one starting with "/" isn't run as a command.
	args := append(program, "--prompt="+firstMessage(req), "--output-format", "stream-json",
		"--disable-slash-commands", "--print-timeout=0s")
	if req.Model != "" {
		args = append(args, "--model="+req.Model)
	}
	if req.SessionID != "" {
		args = append(args, "--conversation="+req.SessionID)
	}
	return Invocation{Args: args, Dir: req.WorkDir, Env: req.Env, Mounts: mounts}, nil
}

// shellQuote quotes s for sh, which runs agy's hook commands.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// agyTools are the built-in tools the capabilities allow, under the names agy gives them (they
// differ between models). Never the shell, the browser, subagents or scheduling.
func agyTools(caps Capabilities) []string {
	tools := []string{}
	if caps.Read {
		tools = append(tools, "read_file", "view_file", "list_dir", "find_by_name", "grep_search")
	}
	if caps.Edit {
		tools = append(tools, "write_file", "edit_file", "write_to_file", "replace_file_content", "multi_replace_file_content")
	}
	if caps.Web {
		tools = append(tools, "search_web", "read_url_content")
	}
	return tools
}

// agyMCPTools are agy's tools for reaching MCP servers; each names its server (ServerName).
var agyMCPTools = []string{"call_mcp_tool", "list_resources", "read_resource"}

// agyRules are what the gate allows: built-in tools, and MCP servers with the tools they block.
type agyRules struct {
	Tools   []string            `json:"tools"`
	Servers map[string][]string `json:"servers"`
}

// AgyGate is agy's PreToolUse hook: it reads the tool call agy is about to make and answers
// allow or deny. Anything it can't read is denied, and agy refuses the call when the hook fails.
func AgyGate(rulesPath string, in io.Reader, out io.Writer) {
	allowed, reason := false, "The dashboard doesn't allow this tool here."
	var rules agyRules
	var call struct {
		ToolCall struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"toolCall"`
	}
	data, err := os.ReadFile(rulesPath)
	if err == nil && json.Unmarshal(data, &rules) == nil && json.NewDecoder(io.LimitReader(in, 4<<20)).Decode(&call) == nil {
		allowed, reason = agyAllows(rules, call.ToolCall.Name, call.ToolCall.Args)
	}
	decision := map[string]string{"decision": "deny", "reason": reason}
	if allowed {
		decision = map[string]string{"decision": "allow"}
	}
	json.NewEncoder(out).Encode(decision)
}

func agyAllows(rules agyRules, name string, args map[string]any) (bool, string) {
	if slices.Contains(agyMCPTools, name) {
		server, _ := args["ServerName"].(string)
		blocked, ok := rules.Servers[server]
		if !ok {
			return false, "That MCP server isn't turned on."
		}
		if tool, _ := args["ToolName"].(string); name == "call_mcp_tool" && (tool == "" || slices.Contains(blocked, tool)) {
			return false, "The dashboard doesn't allow that MCP tool."
		}
		return true, ""
	}
	if slices.Contains(rules.Tools, name) {
		return true, ""
	}
	return false, "The dashboard doesn't allow the " + name + " tool here."
}

type agyLine struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	StepUpdate     struct {
		StepIndex int    `json:"step_index"`
		State     string `json:"state"`
		StepType  string `json:"step_type"`
		ToolName  string `json:"tool_name"`
		TextDelta string `json:"text_delta"`
		ToolInfo  struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"result"`
}

func (a *antigravity) ParseLine(line []byte) []Event {
	var ev agyLine
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	step := ev.StepUpdate
	switch {
	case ev.Event == "init" && ev.ConversationID != "":
		return []Event{{Kind: EventSession, Text: ev.ConversationID}}
	case ev.Event == "step_update" && step.StepType == "agent_response":
		var events []Event
		if step.TextDelta != "" {
			events = append(events, Event{Kind: EventText, Text: step.TextDelta})
		}
		if step.State == "DONE" {
			events = append(events, Event{Kind: EventBreak})
		}
		return events
	case ev.Event == "step_update" && step.StepType == "tool" && step.State == "DONE":
		id := strconv.Itoa(step.StepIndex)
		name, input := cmp.Or(step.ToolInfo.Name, step.ToolName, "tool"), step.ToolInfo.Parameters
		if name == "call_mcp_tool" {
			// MCP calls come as one tool; named the way the other CLIs name them.
			server, _ := input["ServerName"].(string)
			tool, _ := input["ToolName"].(string)
			name = "mcp__" + server + "__" + tool
			var arguments map[string]any
			if raw, ok := input["Arguments"].(string); ok && json.Unmarshal([]byte(raw), &arguments) == nil {
				input = arguments
			}
		}
		events := []Event{{Kind: EventTool, ToolID: id, ToolName: name, ToolInput: input}}
		if step.ToolInfo.Error != nil {
			events = append(events, Event{Kind: EventToolError, ToolID: id})
		}
		return events
	case ev.Event == "result" && ev.Result.Status != "SUCCESS":
		return []Event{{Kind: EventError, Text: cmp.Or(ev.Result.Error, strings.ToLower(ev.Result.Status), "failed")}}
	}
	return nil
}

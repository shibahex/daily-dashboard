package assistant

import (
	"encoding/json"
	"errors"
	"strings"
)

// codex runs OpenAI's Codex CLI: `codex exec --json` with the MCP servers passed as -c config
// overrides. It keeps its shell, inside the dashboard's sandbox. Codex can't be limited to a
// list of tools, so codexOffFeatures turns off every feature that reaches other accounts, and
// the user's own config.toml (with its MCP servers and plugins) is ignored. Web search follows
// the user's policy; the sandbox's egress proxy is the network boundary either way.
type codex struct{ cli }

func newCodex() *codex {
	return &codex{cli{
		id: "codex", label: "Codex", program: "codex", nixPackage: "nixpkgs#codex",
		signIn: "Run `codex login` in a terminal, or set OPENAI_API_KEY.",
		models: []Model{{ID: "gpt-5.5", Label: "GPT-5.5"}},
	}}
}

func (c *codex) Invocation(req Request) (Invocation, error) {
	program := c.command()
	if program == nil {
		return Invocation{}, errors.New("Codex isn't installed")
	}
	// -c values are TOML; JSON strings and string arrays are valid TOML too.
	toml := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	webSearch := `web_search="disabled"`
	if req.Caps.Web {
		webSearch = `web_search="live"`
	}
	overrides := []string{"-c", `approval_policy="never"`, "-c", webSearch}
	for _, feature := range codexOffFeatures {
		overrides = append(overrides, "-c", "features."+feature+"=false")
	}
	for _, s := range req.Servers {
		prefix := "mcp_servers." + s.Name + "."
		overrides = append(overrides, "-c", prefix+"command="+toml(s.Server.Command), "-c", prefix+"args="+toml(s.Server.Args))
		for key, value := range s.Server.Env {
			overrides = append(overrides, "-c", prefix+"env."+key+"="+toml(value))
		}
		if blocked := BlockedTools[s.Name]; len(blocked) > 0 {
			overrides = append(overrides, "-c", prefix+"disabled_tools="+toml(blocked))
		}
	}

	// Codex's own sandbox can't nest inside the dashboard's; the dashboard's is the boundary
	// (the Runner never runs Codex outside it), so Codex's is off.
	args := append(program, "exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"-C", req.Workspace, "-s", "danger-full-access")
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, overrides...)
	// The message goes in on stdin ("-"): Codex reads one that starts with "-" as a flag even
	// after "--".
	if req.SessionID != "" && !strings.HasPrefix(req.SessionID, "-") {
		args = append(args, "resume", req.SessionID, "-")
	} else {
		args = append(args, "-")
	}
	return Invocation{Args: args, Dir: req.WorkDir, Stdin: firstMessage(req), Env: req.Env}, nil
}

// codexOffFeatures are the Codex features that reach the web or accounts other than the
// user's: browsing and computer use, connected apps and plugins, and installing things.
var codexOffFeatures = []string{
	"apps", "browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use",
	"in_app_browser", "in_app_local_automation", "plugins", "remote_plugin", "plugin_sharing",
	"skill_mcp_dependency_install", "skill_search", "tool_suggest", "workspace_dependencies",
	"image_generation", "hooks",
}

// firstMessage puts the instructions in front of a conversation's first message, for CLIs
// without a system-prompt flag.
func firstMessage(req Request) string {
	if req.SessionID != "" {
		return req.Message
	}
	return req.Instructions + "\n\n---\n\n" + req.Message
}

type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Error    struct {
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		ID        string         `json:"id"`
		Type      string         `json:"type"`
		Text      string         `json:"text"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
		Command   string         `json:"command"`
		Status    string         `json:"status"`
		Error     any            `json:"error"`
	} `json:"item"`
}

func (c *codex) ParseLine(line []byte) []Event {
	var ev codexLine
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	item := ev.Item
	switch {
	case ev.Type == "thread.started":
		return []Event{{Kind: EventSession, Text: ev.ThreadID}}
	case ev.Type == "item.started" && item.Type == "mcp_tool_call":
		name := item.Tool
		if name == "" {
			name = "tool"
		}
		return []Event{{Kind: EventTool, ToolID: item.ID, ToolName: name, ToolInput: item.Arguments}}
	case ev.Type == "item.started" && item.Type == "command_execution":
		return []Event{{Kind: EventTool, ToolID: item.ID, ToolName: "run", ToolInput: map[string]any{"command": item.Command}}}
	case ev.Type == "item.completed" && item.Type == "agent_message" && item.Text != "":
		// Codex sends whole messages, not deltas.
		return []Event{{Kind: EventText, Text: item.Text}, {Kind: EventBreak}}
	case ev.Type == "item.completed" && (item.Type == "mcp_tool_call" || item.Type == "command_execution") &&
		(item.Status == "failed" || item.Error != nil):
		return []Event{{Kind: EventToolError, ToolID: item.ID}}
	case ev.Type == "turn.failed":
		message := ev.Error.Message
		if message == "" {
			message = "failed"
		}
		return []Event{{Kind: EventError, Text: message}}
	}
	return nil
}

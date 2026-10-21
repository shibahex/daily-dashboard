package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// claude runs Claude Code: `claude -p` with streaming JSON, the given MCP servers and only the
// built-in tools the user's policy allows (claudeTools). --restricted ignores settings files
// and keeps the file tools in the workspace; dontAsk refuses any tool not allowed here.
type claude struct{ cli }

func newClaude() *claude {
	return &claude{cli{
		id: "claude-code", label: "Claude Code", program: "claude",
		signIn: "Add a Claude subscription token (run `claude setup-token`) or an Anthropic API key on the Account page.",
		models: []Model{{ID: "opus", Label: "Opus"}, {ID: "sonnet", Label: "Sonnet"}, {ID: "fable", Label: "Fable"}, {ID: "haiku", Label: "Haiku"}},
	}}
}

func (c *claude) Invocation(req Request) (Invocation, error) {
	program := c.command()
	if program == nil {
		return Invocation{}, errors.New("Claude Code isn't installed")
	}
	if err := os.MkdirAll(req.WorkDir, 0o700); err != nil {
		return Invocation{}, err
	}

	// --mcp-config file with just the turned-on servers (--strict-mcp-config ignores the user's own).
	servers := map[string]any{}
	allowed, blocked := []string{}, []string{}
	for _, s := range req.Servers {
		server := map[string]any{"type": "stdio", "command": s.Server.Command, "args": s.Server.Args}
		if len(s.Server.Env) > 0 {
			server["env"] = s.Server.Env
		}
		servers[s.Name] = server
		allowed = append(allowed, "mcp__"+s.Name)
		for _, tool := range BlockedTools[s.Name] {
			blocked = append(blocked, "mcp__"+s.Name+"__"+tool)
		}
	}
	configPath := filepath.Join(req.WorkDir, "claude-mcp.json")
	config, _ := json.Marshal(map[string]any{"mcpServers": servers})
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return Invocation{}, err
	}

	tools := claudeTools(req.Caps)
	allowed = append(allowed, tools...)
	args := append(program,
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--restricted", "--strict-mcp-config", "--mcp-config", req.inside(configPath),
		"--tools", strings.Join(tools, ","), "--permission-mode", "dontAsk",
	)
	if req.Instructions != "" {
		args = append(args, "--append-system-prompt", req.Instructions)
	}
	if len(allowed) > 0 {
		args = append(append(args, "--allowedTools"), allowed...)
	}
	if len(blocked) > 0 {
		args = append(append(args, "--disallowedTools"), blocked...)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	// After --, a message that starts with "-" is still the message, not a flag.
	args = append(args, "--", req.Message)
	return Invocation{Args: args, Dir: req.WorkDir, Env: req.Env}, nil
}

// claudeTools are the built-in tools the capabilities allow (none at all by default).
func claudeTools(caps Capabilities) []string {
	var tools []string
	if caps.Read {
		tools = append(tools, "Read", "Glob", "Grep")
	}
	if caps.Edit {
		tools = append(tools, "Edit", "Write")
	}
	if caps.Web {
		tools = append(tools, "WebSearch", "WebFetch")
	}
	return tools
}

// claudeLine is the part of Claude Code's stream-json events the dashboard reads.
type claudeLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	Result    any    `json:"result"`
	Event     struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	IsError   bool           `json:"is_error"`
}

func (c *claude) ParseLine(line []byte) []Event {
	var ev claudeLine
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	var blocks []claudeBlock
	json.Unmarshal(ev.Message.Content, &blocks) // content can also be a plain string; then there are no blocks

	var events []Event
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			events = append(events, Event{Kind: EventSession, Text: ev.SessionID})
		}
	case "stream_event": // text as it's written
		if ev.Event.Delta.Type == "text_delta" {
			events = append(events, Event{Kind: EventText, Text: ev.Event.Delta.Text})
		}
	case "assistant": // a finished message: its text was already streamed, so only note the break
		for _, b := range blocks {
			switch b.Type {
			case "text":
				events = append(events, Event{Kind: EventBreak})
			case "tool_use":
				events = append(events, Event{Kind: EventTool, ToolID: b.ID, ToolName: b.Name, ToolInput: b.Input})
			}
		}
	case "user": // tool results
		for _, b := range blocks {
			if b.Type == "tool_result" && b.IsError {
				events = append(events, Event{Kind: EventToolError, ToolID: b.ToolUseID})
			}
		}
	case "result":
		events = append(events, Event{Kind: EventSession, Text: ev.SessionID})
		if ev.IsError {
			message, _ := ev.Result.(string)
			if message == "" {
				message = "failed"
			}
			events = append(events, Event{Kind: EventError, Text: message})
		}
	}
	return events
}

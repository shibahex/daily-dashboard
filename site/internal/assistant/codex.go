package assistant

import (
	"encoding/json"
	"errors"
)

// codex runs OpenAI's Codex CLI: `codex exec --json` in a read-only sandbox, with the MCP
// servers passed as -c config overrides.
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
	overrides := []string{"-c", `approval_policy="never"`}
	for _, s := range req.Servers {
		prefix := "mcp_servers." + s.Name + "."
		overrides = append(overrides, "-c", prefix+"command="+toml(s.Server.Command), "-c", prefix+"args="+toml(s.Server.Args))
		for key, value := range s.Server.Env {
			overrides = append(overrides, "-c", prefix+"env."+key+"="+toml(value))
		}
	}

	args := append(program, "exec", "--json", "--skip-git-repo-check", "-C", req.RepoDir, "-s", "read-only")
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, overrides...)
	message := firstMessage(req)
	if req.SessionID != "" {
		args = append(args, "resume", req.SessionID, message)
	} else {
		args = append(args, message)
	}
	return Invocation{Args: args, Dir: req.RepoDir}, nil
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

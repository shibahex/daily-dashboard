package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// gemini runs Google's Gemini CLI: `gemini -p` with stream-json output.
type gemini struct{ cli }

func newGemini() *gemini {
	return &gemini{cli{
		id: "gemini", label: "Gemini CLI", program: "gemini", nixPackage: "nixpkgs#gemini-cli",
		signIn: "Run `gemini` in a terminal once and sign in with Google, or set GEMINI_API_KEY.",
		models: []Model{
			{ID: "gemini-3-pro-preview", Label: "Gemini 3 Pro (preview)"},
			{ID: "gemini-3-flash-preview", Label: "Gemini 3 Flash (preview)"},
			{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
			{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
			{ID: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash-Lite"},
		},
	}}
}

func (g *gemini) Invocation(req Request) (Invocation, error) {
	program := g.command()
	if program == nil {
		return Invocation{}, errors.New("Gemini CLI isn't installed")
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return Invocation{}, err
	}

	// A settings file the dashboard owns, given as Gemini's *system* settings so ~/.gemini is
	// left alone. Folder trust is off so the MCP servers and auto-approval apply, and only
	// read-only built-in tools are enabled.
	servers := map[string]any{}
	names := []string{}
	for _, s := range req.Servers {
		servers[s.Name] = s.Server
		names = append(names, s.Name)
	}
	settingsPath := filepath.Join(req.WorkDir, "gemini-settings.json")
	settings, _ := json.Marshal(map[string]any{
		"security":   map[string]any{"folderTrust": map[string]any{"enabled": false}},
		"tools":      map[string]any{"core": []string{"read_file", "read_many_files", "glob", "search_file_content", "list_directory"}},
		"mcpServers": servers,
	})
	if err := os.WriteFile(settingsPath, settings, 0o644); err != nil {
		return Invocation{}, err
	}

	args := append(program, "-p", firstMessage(req), "-o", "stream-json", "--approval-mode", "yolo")
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if len(names) > 0 {
		args = append(args, "--allowed-mcp-server-names")
		args = append(args, names...)
	}
	args = append(args, "--include-directories", req.RepoDir)
	if req.SessionID != "" {
		// Gemini resumes by "latest" or index, not id; this folder only holds dashboard chats.
		args = append(args, "--resume", "latest")
	}
	env := append(os.Environ(), "GEMINI_CLI_SYSTEM_SETTINGS_PATH="+settingsPath)
	return Invocation{Args: args, Dir: req.WorkDir, Env: env}, nil
}

type geminiLine struct {
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id"`
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolID     string         `json:"tool_id"`
	ToolName   string         `json:"tool_name"`
	Parameters map[string]any `json:"parameters"`
	Status     string         `json:"status"`
	Error      struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (g *gemini) ParseLine(line []byte) []Event {
	var ev geminiLine
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	switch {
	case ev.Type == "init":
		return []Event{{Kind: EventSession, Text: ev.SessionID}}
	case ev.Type == "message" && ev.Role == "assistant" && ev.Content != "":
		return []Event{{Kind: EventText, Text: ev.Content}}
	case ev.Type == "tool_use":
		name := ev.ToolName
		if name == "" {
			name = "tool"
		}
		return []Event{{Kind: EventTool, ToolID: ev.ToolID, ToolName: name, ToolInput: ev.Parameters}}
	case ev.Type == "tool_result" && ev.Status != "success":
		return []Event{{Kind: EventToolError, ToolID: ev.ToolID}}
	case ev.Type == "result" && ev.Status != "success":
		message := ev.Error.Message
		if message == "" {
			message = "failed"
		}
		return []Event{{Kind: EventError, Text: message}}
	}
	return nil
}

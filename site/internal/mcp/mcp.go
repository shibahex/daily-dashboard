// Package mcp describes how an AI agent starts an MCP server.
package mcp

// Server is the command an agent runs to start an MCP server over stdio. The JSON shape is
// the one Claude Code, Codex and Gemini CLI all accept in their config.
type Server struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

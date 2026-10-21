// Package mcp starts MCP servers and talks to them (client.go), for the AI's tools.
package mcp

// Server is the command that starts an MCP server over stdio.
type Server struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

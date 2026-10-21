// Package assistant is what the server knows about the MCPs tab's AI, which runs in each user's
// browser (web/js/assistant.js): the system prompt it gives it (the dashboard's instructions, the
// plan and data, never the notes), the MCP tools it may not use, and its saved settings.
package assistant

import "regexp"

// Model is one entry in the model menu.
type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ModelNamePattern is what a model name may look like ("claude-opus-5-5", "qwen3:8b").
var ModelNamePattern = regexp.MustCompile(`^[\w.:/@+-]{1,120}$`)

// BlockedTools are MCP tools the AI never gets, by server. The Anki gateway refuses what they do
// anyway; blocking them here too means the AI never sees them.
var BlockedTools = map[string][]string{
	"anki": {"storeMediaFile"},
}

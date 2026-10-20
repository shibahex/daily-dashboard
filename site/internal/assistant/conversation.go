package assistant

import (
	"dashboard/internal/auth"

	"sync"

	"dashboard/internal/jsonfile"
)

// The MCP servers the tab knows, in order. Both are on unless the user turned one off.
var KnownMCPs = auth.KnownMCPs

// Conversation is the chat kept in data/chat.json until "New chat".
type Conversation struct {
	Provider  string            `json:"provider"`
	SessionID string            `json:"session_id,omitempty"` // the CLI's id, to resume
	Messages  []Message         `json:"messages"`
	MCPs      *[]string         `json:"mcps,omitempty"`   // turned-on MCP servers; nil = all
	Models    map[string]string `json:"models,omitempty"` // chosen model per provider ("" = default)

	// NoInstructions runs the CLI as it is: no dashboard prompt, notes or data, just the MCPs.
	NoInstructions bool `json:"no_instructions,omitempty"`
}

// Message is one chat bubble.
type Message struct {
	Role     string  `json:"role"` // "user" or "assistant"
	Text     string  `json:"text"`
	At       float64 `json:"at"` // unix seconds
	Provider string  `json:"provider,omitempty"`
	Model    string  `json:"model,omitempty"`
	Tools    []Tool  `json:"tools,omitzero"`
	Error    string  `json:"error,omitempty"`
}

// Tool is one tool call shown above an answer.
type Tool struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Error  bool   `json:"error,omitempty"`
}

// EnabledMCPs is the turned-on MCP servers, in KnownMCPs order.
func (c *Conversation) EnabledMCPs() []string {
	if c.MCPs == nil {
		return KnownMCPs
	}
	on := map[string]bool{}
	for _, name := range *c.MCPs {
		on[name] = true
	}
	var enabled []string
	for _, name := range KnownMCPs {
		if on[name] {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

// Model is the chosen model for the conversation's provider ("" = its default).
func (c *Conversation) Model() string { return c.Models[c.Provider] }

func (c *Conversation) SetModel(model string) {
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	c.Models[c.Provider] = model
}

// Conversations loads and saves data/chat.json.
type Conversations struct {
	path      string
	providers Providers
	mu        sync.Mutex
}

func NewConversations(path string, providers Providers) *Conversations {
	return &Conversations{path: path, providers: providers}
}

// oldClaudeModels maps model names saved before the menu used Claude Code's own values.
var oldClaudeModels = map[string]string{"claude-opus-5-5": "opus", "claude-sonnet-5-5": "sonnet",
	"claude-fable-5-1": "fable", "claude-haiku-5-5": "haiku"}

// Load returns the saved conversation, or a new one with the default provider.
func (cs *Conversations) Load() *Conversation {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	conversation := &Conversation{Messages: []Message{}}
	jsonfile.Read(cs.path, conversation)
	if conversation.Messages == nil {
		conversation.Messages = []Message{}
	}
	if newer, ok := oldClaudeModels[conversation.Models["claude"]]; ok {
		conversation.Models["claude"] = newer
	}
	if _, known := cs.providers.Get(conversation.Provider); !known {
		conversation.Provider = cs.providers.Default().ID()
	}
	return conversation
}

func (cs *Conversations) Save(c *Conversation) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return jsonfile.Write(cs.path, c)
}

// StartNew replaces the conversation with an empty one for provider, keeping the MCP, model
// and instructions choices.
func (cs *Conversations) StartNew(provider string) error {
	old := cs.Load()
	mcps := old.EnabledMCPs()
	if old.MCPs != nil {
		mcps = *old.MCPs
	}
	return cs.Save(&Conversation{Provider: provider, Messages: []Message{}, MCPs: &mcps, Models: old.Models,
		NoInstructions: old.NoInstructions})
}

package assistant

import (
	"dashboard/internal/auth"

	"sync"

	"dashboard/internal/jsonfile"
)

// The MCP servers the tab knows, in order. Both are on unless the user turned one off.
var KnownMCPs = auth.KnownMCPs

// Conversation is data/chat.json: the AI's settings (which MCPs, "Attach prompt", the model). It
// also held the chat of the server-side AI there used to be, which the browser seals once
// (web/sealed.go) and the server then forgets.
type Conversation struct {
	Provider  string            `json:"provider"`
	SessionID string            `json:"session_id,omitempty"` // the CLI's id, to resume
	Messages  []Message         `json:"messages"`
	MCPs      *[]string         `json:"mcps,omitempty"`   // turned-on MCP servers; nil = all
	Models    map[string]string `json:"models,omitempty"` // chosen model per provider ("" = default)

	// NoInstructions leaves out the dashboard's guidance ("Attach prompt" off). The agent still
	// gets the user's notes and data.
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

// Conversations loads and saves data/chat.json.
type Conversations struct {
	path string
	mu   sync.Mutex
}

func NewConversations(path string) *Conversations { return &Conversations{path: path} }

// Load returns the saved conversation, or a new one.
func (cs *Conversations) Load() *Conversation {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	conversation := &Conversation{Messages: []Message{}}
	jsonfile.Read(cs.path, conversation)
	if conversation.Messages == nil {
		conversation.Messages = []Message{}
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

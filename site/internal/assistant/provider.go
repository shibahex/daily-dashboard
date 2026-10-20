// Package assistant runs an AI coding agent (Claude Code, Codex or Gemini CLI) headless for the
// MCPs tab, with only the MCP servers turned on there and read-only access to the repo.
package assistant

import (
	"encoding/json"
	"os/exec"
	"regexp"

	"dashboard/internal/mcp"
)

// Provider is one agent CLI. Each CLI takes its own flags and streams JSON in its own format;
// a Provider hides both behind Invocation and ParseLine.
type Provider interface {
	ID() string         // "claude", "codex", "gemini"
	Label() string      // "Claude Code"
	SignInHint() string // what to run when it isn't signed in
	Installed() bool    // on the PATH
	Available() bool    // on the PATH, or runnable through Nix
	Models() []Model    // offered in the model menu ("" = the CLI's default is always offered too)
	Invocation(Request) (Invocation, error)
	ParseLine(line []byte) []Event
}

// Request is one message to send.
type Request struct {
	Message      string
	SessionID    string // continue this conversation; "" starts a new one
	Model        string // "" = the CLI's default
	Servers      []NamedServer
	Instructions string // the system prompt (see Instructions)
	RepoDir      string // what the agent may read
	WorkDir      string // where the dashboard writes config files for the CLIs
}

// NamedServer is an MCP server under the name the agent will know it by.
type NamedServer struct {
	Name   string
	Server mcp.Server
}

// Invocation is the command to run for a Request.
type Invocation struct {
	Args []string // Args[0] is the program
	Dir  string
	Env  []string // nil = the server's own environment
}

// Model is one entry in the model menu.
type Model struct {
	ID          string
	Label       string
	Description string
}

// MarshalJSON writes [id, label] or [id, label, description], the shape the page reads.
func (m Model) MarshalJSON() ([]byte, error) {
	if m.Description == "" {
		return json.Marshal([]string{m.ID, m.Label})
	}
	return json.Marshal([]string{m.ID, m.Label, m.Description})
}

// ModelNamePattern is what a model name may look like ("claude-opus-5-5", "gpt-5.5").
var ModelNamePattern = regexp.MustCompile(`^[\w.:/-]{1,80}$`)

// ---------- events ----------

type EventKind int

const (
	EventSession   EventKind = iota // the CLI's id for this conversation (to resume it later)
	EventText                       // more answer text
	EventBreak                      // a text block ended; the next text starts a new paragraph
	EventTool                       // the agent called a tool
	EventToolError                  // that tool call failed
	EventError                      // the whole run failed
)

// Event is one thing a CLI reported, in the same form for every CLI.
type Event struct {
	Kind      EventKind
	Text      string         // EventText, EventError; the id for EventSession
	ToolID    string         // EventTool, EventToolError
	ToolName  string         // EventTool
	ToolInput map[string]any // EventTool
}

// ---------- the CLI binary ----------

// cli is what every provider shares: its names, and how to find its program.
type cli struct {
	id, label, signIn string
	program           string // name on the PATH
	nixPackage        string // run through `nix run` when not installed; "" = no fallback
	models            []Model
}

func (c cli) ID() string         { return c.id }
func (c cli) Label() string      { return c.label }
func (c cli) SignInHint() string { return c.signIn }
func (c cli) Models() []Model    { return c.models }

func (c cli) Installed() bool { _, err := exec.LookPath(c.program); return err == nil }
func (c cli) Available() bool { return c.command() != nil }

// command is the program to run: from the PATH, or `nix run <package> --` (which downloads it
// on first use), or nil when neither works.
func (c cli) command() []string {
	if path, err := exec.LookPath(c.program); err == nil {
		return []string{path}
	}
	if _, err := exec.LookPath("nix"); err == nil && c.nixPackage != "" {
		return []string{"nix", "run", c.nixPackage, "--"}
	}
	return nil
}

// ---------- the registry ----------

// Providers are the agent CLIs the tab offers, in menu order.
type Providers []Provider

func NewProviders(claudeModels *ModelCatalog) Providers {
	return Providers{newClaude(claudeModels), newCodex(), newGemini()}
}

// Get returns the provider with this id.
func (ps Providers) Get(id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID() == id {
			return p, true
		}
	}
	return nil, false
}

// Default is the first installed provider, or the first one.
func (ps Providers) Default() Provider {
	for _, p := range ps {
		if p.Installed() {
			return p
		}
	}
	return ps[0]
}

// ProviderInfo is a provider as the page lists it.
type ProviderInfo struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Installed  bool    `json:"installed"`
	Available  bool    `json:"available"`
	SignInHint string  `json:"signin"`
	Models     []Model `json:"models"`
}

func (ps Providers) Info() []ProviderInfo {
	infos := make([]ProviderInfo, 0, len(ps))
	for _, p := range ps {
		infos = append(infos, ProviderInfo{ID: p.ID(), Label: p.Label(), Installed: p.Installed(),
			Available: p.Available(), SignInHint: p.SignInHint(), Models: p.Models()})
	}
	return infos
}

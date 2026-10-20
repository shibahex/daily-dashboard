// Package assistant runs an AI coding agent (Claude Code, Codex or Gemini CLI) headless for the
// MCPs tab, with only the MCP servers turned on there and nothing that reaches the web or other
// accounts (Codex keeps its shell, sandboxed read-only and offline).
package assistant

import (
	"os"
	"os/exec"
	"path/filepath"
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
	Instructions string   // the system prompt, with the user's notes and data (see Instructions)
	WorkDir      string   // where the dashboard writes config files for the CLIs; the CLIs run here
	Home         string   // the CLI's HOME: the user's own, so it never sees the server's logins ("" = the server's)
	Env          []string // extra environment: the user's API key
}

// environment is what the CLI runs with: the server's environment with the user's HOME and
// API key on top (nil when there's nothing to change).
func (r Request) environment() []string {
	if r.Home == "" && len(r.Env) == 0 {
		return nil
	}
	env := os.Environ()
	if r.Home != "" {
		env = append(env, "HOME="+r.Home, "XDG_CONFIG_HOME="+filepath.Join(r.Home, ".config"),
			"XDG_DATA_HOME="+filepath.Join(r.Home, ".local", "share"), "XDG_CACHE_HOME="+filepath.Join(r.Home, ".cache"))
	}
	return append(env, r.Env...)
}

// BlockedTools are MCP tools no agent gets, by server. The Anki gateway refuses what they do
// anyway; blocking them here too means the agent never sees them.
var BlockedTools = map[string][]string{
	"anki": {"storeMediaFile"},
}

// NamedServer is an MCP server under the name the agent will know it by.
type NamedServer struct {
	Name   string
	Server mcp.Server
}

// Invocation is the command to run for a Request.
type Invocation struct {
	Args  []string // Args[0] is the program
	Dir   string
	Env   []string // nil = the server's own environment
	Stdin string   // sent on standard input ("" = none)
}

// Model is one entry in the model menu.
type Model struct {
	ID          string
	Label       string
	Description string
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

// APIKey is the key a provider's CLI needs: the name it's saved under in a user's secrets, the
// environment variables the CLI reads it from, and where to make one.
type APIKey struct {
	Name, Label, Env, Where string
	ExtraEnv                []string
}

// APIKeys are the keys users can add, by provider id.
var APIKeys = map[string]APIKey{
	"claude": {Name: "anthropic", Label: "Anthropic (Claude Code)", Env: "ANTHROPIC_API_KEY", Where: "https://console.anthropic.com/settings/keys"},
	"codex":  {Name: "openai", Label: "OpenAI (Codex)", Env: "OPENAI_API_KEY", ExtraEnv: []string{"CODEX_API_KEY"}, Where: "https://platform.openai.com/api-keys"},
	"gemini": {Name: "gemini", Label: "Google (Gemini CLI)", Env: "GEMINI_API_KEY", Where: "https://aistudio.google.com/apikey"},
}

// KeyEnv is the environment that gives the provider's CLI this key.
func (k APIKey) KeyEnv(value string) []string {
	env := []string{k.Env + "=" + value}
	for _, extra := range k.ExtraEnv {
		env = append(env, extra+"="+value)
	}
	return env
}

package assistant

// The AIs that run on the server, for people who turn that on (see web/serverai.go): an AI
// coding agent's CLI (Claude Code, Codex, Gemini CLI) run headless in the user's sandbox, with
// only the MCP servers turned on and nothing that reaches other accounts.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dashboard/internal/mcp"
)

// Provider is one agent CLI. Each CLI takes its own flags and streams JSON in its own format;
// a Provider hides both behind Invocation and ParseLine.
type Provider interface {
	ID() string         // "claude-code", "codex", "gemini"
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
	Instructions string       // the system prompt, with the user's notes and data (see Instructions)
	Caps         Capabilities // what the user's policy lets the agent do
	WorkDir      string       // where the dashboard writes config files for the CLI
	InnerWorkDir string       // WorkDir as the CLI sees it inside the sandbox
	Workspace    string       // the folder the CLI works in, as it sees it
	Env          []string     // extra environment for the CLI: the user's API key (see KeyEnv)
	Attachments  []Attachment // images and PDFs sent with the message (see AttachmentKinds)

	// Wrap puts the CLI in the user's sandbox. The Runner refuses to run without it.
	Wrap func(Invocation) (Invocation, error)
}

// Capabilities are the agent's tools beyond its MCP servers, from the user's policy.
type Capabilities struct {
	Web  bool // search and fetch web pages
	Read bool // read the user's files in the workspace
	Edit bool // change the user's notes and plan there
}

// inside is a path under WorkDir as the CLI sees it.
func (r Request) inside(hostPath string) string {
	if r.InnerWorkDir == "" {
		return hostPath
	}
	rel, err := filepath.Rel(r.WorkDir, hostPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return hostPath
	}
	return filepath.Join(r.InnerWorkDir, rel)
}

// Attachment is an image or a PDF sent with a message.
type Attachment struct {
	Kind      string // "image" or "pdf"
	Name      string
	MediaType string // image/jpeg, image/png, image/gif, image/webp or application/pdf
	Data      []byte
}

// AttachmentKinds are what each CLI can take with a message: Claude Code reads images and PDFs
// in its input, Codex images given as files. Gemini CLI has no way in without its file tools,
// which stay off. web/js/assistant.js keeps the same list.
var AttachmentKinds = map[string][]string{"claude-code": {"image", "pdf"}, "codex": {"image"}}

// attachmentExtensions name the files a CLI is given.
var attachmentExtensions = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp", "application/pdf": ".pdf"}

// AttachmentDir is where a run's attachments are written, under its WorkDir. The server removes
// it once the run is over.
const AttachmentDir = "attachments"

// saveAttachments writes the attachments into WorkDir for a CLI that reads them from files, and
// gives their paths as the CLI sees them.
func saveAttachments(req Request) ([]string, error) {
	dir := filepath.Join(req.WorkDir, AttachmentDir)
	if err := os.RemoveAll(dir); err != nil || len(req.Attachments) == 0 {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var paths []string
	for i, a := range req.Attachments {
		path := filepath.Join(dir, fmt.Sprintf("%d%s", i+1, attachmentExtensions[a.MediaType]))
		if err := os.WriteFile(path, a.Data, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, req.inside(path))
	}
	return paths, nil
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

// Available is cheap (pages ask on every render): on the PATH, or Nix can build it.
func (c cli) Available() bool {
	if c.Installed() {
		return true
	}
	_, err := exec.LookPath("nix")
	return err == nil && c.nixPackage != ""
}

// command is the program to run, as a real path a sandbox can see: from the PATH, or built
// with Nix (downloaded on first use), or nil when neither works.
func (c cli) command() []string {
	if path, err := exec.LookPath(c.program); err == nil {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return []string{real}
		}
		return []string{path}
	}
	if path := nixProgram(c.nixPackage, c.program); path != "" {
		return []string{path}
	}
	return nil
}

var (
	nixBuiltMu sync.Mutex
	nixBuilt   = map[string]string{}
)

// nixProgram builds a Nix package (once) and returns its program's path in the store, which is
// what a sandbox can run ("" when there's no Nix or the build fails).
func nixProgram(pkg, program string) string {
	if pkg == "" {
		return ""
	}
	nixBuiltMu.Lock()
	defer nixBuiltMu.Unlock()
	if path, ok := nixBuilt[pkg]; ok {
		return path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nix", "build", "--no-link", "--print-out-paths", pkg).Output()
	if err != nil {
		return ""
	}
	path := filepath.Join(strings.TrimSpace(strings.Split(string(out), "\n")[0]), "bin", program)
	nixBuilt[pkg] = path
	return path
}

// ---------- the registry ----------

// Providers are the agent CLIs, in menu order.
type Providers []Provider

func NewProviders() Providers { return Providers{newClaude(), newCodex(), newGemini()} }

// Get returns the provider with this id.
func (ps Providers) Get(id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID() == id {
			return p, true
		}
	}
	return nil, false
}

// KeyEnv is the environment that gives a provider's CLI the user's key. For Claude Code that is
// an API key or a subscription token (from `claude setup-token`, "sk-ant-oat…").
func KeyEnv(provider, key string) []string {
	switch {
	case key == "":
		return nil
	case provider == "claude-code" && strings.HasPrefix(key, "sk-ant-oat"):
		return []string{"CLAUDE_CODE_OAUTH_TOKEN=" + key}
	case provider == "claude-code":
		return []string{"ANTHROPIC_API_KEY=" + key}
	case provider == "codex":
		return []string{"OPENAI_API_KEY=" + key, "CODEX_API_KEY=" + key}
	case provider == "gemini":
		return []string{"GEMINI_API_KEY=" + key}
	}
	return nil
}

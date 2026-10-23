package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/mcp"
	"dashboard/internal/problem"
	"dashboard/internal/workspace"
)

// The AI in the browser: for everyone whose notes are sealed, the MCPs tab's AI runs in their
// browser (web/js/assistant.js), which talks to Anthropic directly with the user's key, so their
// notes never pass through the server. The key is kept sealed in the browser too ("settings"):
// the server never has it. The server gives it what isn't private (the dashboard's
// instructions, the plan and data), and runs the MCP tools it calls (MyFitnessPal, Anki), each
// in the user's sandbox like the server-side AI's.

// browserModels are offered in the browser AI's model menu (the API's own names).
var browserModels = []assistant.Model{
	{ID: "claude-sonnet-5-5", Label: "Sonnet 5.5"}, {ID: "claude-opus-5-5", Label: "Opus 5.5"},
	{ID: "claude-fable-5-1", Label: "Fable 5.1"}, {ID: "claude-haiku-5-5", Label: "Haiku 5.5"},
}

// toolSep joins an MCP's name and its tool's into the name the AI knows the tool by.
const toolSep = "__"

// ---------- setup ----------

type browserMCP struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
}

// oldKeys are where the server kept AI keys and the local model before they moved into the
// browser, and the browser's names for them (assistant.js settings).
var oldKeys = map[string]string{"anthropic": "anthropicKey", "local_key": "localKey", "local_url": "localURL", "local_model": "localModel"}

// assistantSetup tells the browser how to run the AI: which AIs it may use (Claude, a local
// model on the user's own computer, and for people who turned it on, the AIs on the server), the
// chosen one and its model, the system prompt (everything but the notes, which the browser adds),
// the MCPs, and whether it may save notes.
func (ui *UI) assistantSetup(w http.ResponseWriter, r *http.Request) {
	user, ws := ui.user(r), ui.ws(r)
	providers := allowedAIs(user)
	if len(providers) == 0 {
		writeNoStoreJSON(w, map[string]any{"problem": "Your admin hasn't turned on an AI for you"})
		return
	}
	conversation := ws.Conversations.Load()
	provider := conversation.Provider
	if !slices.Contains(providers, provider) {
		provider = providers[0]
	}
	answer := map[string]any{
		"providers":    providers,
		"provider":     provider,
		"models":       modelsFor(provider), // a local server's come from it, in the browser
		"model":        conversation.Models[provider],
		"server":       serverAIs(user), // the providers that run on the server
		"instructions": !conversation.NoInstructions,
		"edit":         user.Policy.EditFiles,
		"relayed":      ui.Auth.RelayedModelServers(), // local model servers reached through the server (modelrelay.go)
	}
	// Keys the server kept from before keys moved into the browser: handed over once, for the
	// browser to seal; they're deleted when it saves its settings (sealed=1).
	old := map[string]string{}
	for name, id := range oldKeys {
		if value := ws.Secrets.Get(name); value != "" {
			old[id] = value
		}
	}
	if len(old) > 0 {
		answer["oldKeys"] = old
	}
	enabled := allowedMCPs(user.Policy, conversation)
	var mcps []browserMCP
	var ready []string
	for _, m := range []browserMCP{
		{ID: "myfitnesspal", Label: "MyFitnessPal", Ready: ws.MyFitnessPal.Client().Setup().SignedIn},
		{ID: "anki", Label: "Anki", Ready: ws.Anki().Status().Connected},
	} {
		if user.Policy.AllowsMCP(m.ID) {
			m.Enabled = slices.Contains(enabled, m.ID)
			mcps = append(mcps, m)
			if m.Enabled && m.Ready {
				ready = append(ready, m.ID)
			}
		}
	}
	answer["mcps"] = mcps
	answer["system"] = assistant.Instructions(time.Now(), assistant.Context(ws.Paths.Plan(), ws.Paths.Data),
		ready, !conversation.NoInstructions)
	writeNoStoreJSON(w, answer)
}

// assistantSettings changes the browser AI's settings (none of them secret): an MCP on or off
// (mcp), "Attach prompt" (instructions=toggle), the AI (provider) or its model (model).
func (ui *UI) assistantSettings(w http.ResponseWriter, r *http.Request) {
	user, ws := ui.user(r), ui.ws(r)
	conversation := ws.Conversations.Load()
	r.ParseMultipartForm(1 << 20) // the page posts FormData; plain ParseForm would skip it
	switch {
	case r.FormValue("mcp") != "":
		name := r.FormValue("mcp")
		if !slices.Contains(assistant.KnownMCPs, name) || !user.Policy.AllowsMCP(name) {
			fail(w, problem.Invalidf("unknown MCP"))
			return
		}
		enabled := slices.Clone(conversation.EnabledMCPs())
		if i := slices.Index(enabled, name); i >= 0 {
			enabled = slices.Delete(enabled, i, i+1)
		} else {
			enabled = append(enabled, name)
		}
		if enabled == nil {
			enabled = []string{}
		}
		conversation.MCPs = &enabled
	case r.FormValue("instructions") != "":
		conversation.NoInstructions = !conversation.NoInstructions
	case r.FormValue("provider") != "":
		provider := r.FormValue("provider")
		if !slices.Contains(allowedAIs(user), provider) {
			fail(w, problem.Forbiddenf("Your admin hasn't turned on that AI for you"))
			return
		}
		conversation.Provider = provider
	case r.Form.Has("model"):
		model := formText(r, "model")
		if model != "" && !assistant.ModelNamePattern.MatchString(model) {
			fail(w, problem.Invalidf("that doesn't look like a model name"))
			return
		}
		if conversation.Models == nil {
			conversation.Models = map[string]string{}
		}
		conversation.Models[conversation.Provider] = model
	}
	if err := ws.Conversations.Save(conversation); err != nil {
		fail(w, err)
		return
	}
	ui.tools().close(user.ID) // the next tool list follows the new MCPs
	writeNoStoreJSON(w, map[string]bool{"ok": true})
}

// allowedAIs are the AIs the user may pick, in menu order.
func allowedAIs(user auth.User) []string {
	var ids []string
	for _, id := range []string{"claude", "local"} {
		if user.Policy.AllowsProvider(id) {
			ids = append(ids, id)
		}
	}
	return append(ids, serverAIs(user)...)
}

// modelsFor is the model menu of an AI ("local" lists its own, in the browser).
func modelsFor(provider string) []assistant.Model {
	if p, ok := serverProviders.Get(provider); ok {
		return p.Models()
	}
	if provider == "claude" {
		return browserModels
	}
	return []assistant.Model{}
}

// ---------- tools ----------

// assistantTools lists the tools of the user's turned-on, ready MCPs, starting them if needed.
func (ui *UI) assistantTools(w http.ResponseWriter, r *http.Request) {
	session, err := ui.tools().open(ui, ui.user(r), ui.ws(r))
	if err != nil {
		fail(w, fmt.Errorf("%w: %v", problem.Unavailablef("Couldn't start the tools"), err))
		return
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	tools := []tool{}
	for _, name := range session.order {
		for _, t := range session.lists[name] {
			tools = append(tools, tool{Name: name + toolSep + t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
	}
	writeNoStoreJSON(w, tools)
}

// assistantTool runs one tool call (name: "mcp__tool", input: its JSON) and answers its result.
func (ui *UI) assistantTool(w http.ResponseWriter, r *http.Request) {
	user, ws := ui.user(r), ui.ws(r)
	server, tool, found := strings.Cut(r.FormValue("name"), toolSep)
	session, err := ui.tools().open(ui, user, ws)
	if err != nil {
		fail(w, fmt.Errorf("%w: %v", problem.Unavailablef("Couldn't start the tools"), err))
		return
	}
	client := session.clients[server]
	if !found || client == nil || !slices.ContainsFunc(session.lists[server], func(t mcp.Tool) bool { return t.Name == tool }) {
		fail(w, problem.Invalidf("that tool isn't available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	result, err := client.Call(ctx, tool, json.RawMessage(r.FormValue("input")))
	if err != nil {
		ui.tools().close(user.ID) // a server that failed is started again next time
		result = mcp.Result{Text: problem.Public(err), IsError: true}
	}
	if assistant.ChangesDiary(server + toolSep + tool) {
		go ws.MyFitnessPal.Sync() // so the Today tab shows the change
	}
	writeNoStoreJSON(w, result)
}

// ---------- tool sessions ----------

// toolSession is one user's running MCP servers, each in its own sandbox.
type toolSession struct {
	key     string // the MCPs it runs, to spot a change
	order   []string
	clients map[string]*mcp.Client
	lists   map[string][]mcp.Tool
	box     *toolSandbox
	used    time.Time
}

type toolSessions struct {
	mu     sync.Mutex
	byUser map[string]*toolSession
}

var (
	toolsOnce sync.Once
	toolsAll  *toolSessions
)

// tools are the users' tool sessions; idle ones are stopped after 10 minutes.
func (ui *UI) tools() *toolSessions {
	toolsOnce.Do(func() {
		toolsAll = &toolSessions{byUser: map[string]*toolSession{}}
		go func() {
			for range time.Tick(time.Minute) {
				toolsAll.closeIdle(10 * time.Minute)
			}
		}()
		if ui.Workspaces != nil {
			ui.Workspaces.OnLock = toolsAll.close // locked data: its tools stop too
		}
	})
	return toolsAll
}

// open is the user's session for their turned-on, ready MCPs, started if needed.
func (ts *toolSessions) open(ui *UI, user auth.User, ws *workspace.Workspace) (*toolSession, error) {
	var names []string
	for _, name := range allowedMCPs(user.Policy, ws.Conversations.Load()) {
		ready := (name == "myfitnesspal" && ws.MyFitnessPal.Client().Setup().SignedIn) ||
			(name == "anki" && ws.Anki().Status().Connected)
		if ready {
			names = append(names, name)
		}
	}
	key := strings.Join(names, ",")
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if s := ts.byUser[user.ID]; s != nil {
		if s.key == key {
			s.used = time.Now()
			return s, nil
		}
		s.stop()
		delete(ts.byUser, user.ID)
	}
	s, err := startTools(ui, ws, names)
	if err != nil {
		return nil, err
	}
	s.key = key
	ts.byUser[user.ID] = s
	return s, nil
}

func (ts *toolSessions) close(userID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if s := ts.byUser[userID]; s != nil {
		s.stop()
		delete(ts.byUser, userID)
	}
}

func (ts *toolSessions) closeIdle(after time.Duration) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for id, s := range ts.byUser {
		if time.Since(s.used) > after {
			s.stop()
			delete(ts.byUser, id)
		}
	}
}

// startTools starts each MCP server in its own sandbox, as the server-side AI's would be.
func startTools(ui *UI, ws *workspace.Workspace, names []string) (*toolSession, error) {
	s := &toolSession{clients: map[string]*mcp.Client{}, lists: map[string][]mcp.Tool{}, used: time.Now(),
		box: &toolSandbox{ui: ui, ws: ws}}
	if len(names) == 0 {
		return s, nil
	}
	if ui.SandboxErr != nil {
		return nil, ui.SandboxErr
	}
	spec, err := s.box.open()
	if err != nil {
		s.stop()
		return nil, err
	}
	for _, name := range names {
		inner := spec
		inner.Mounts = slices.Clone(spec.Mounts)
		server, err := s.box.mcpServer(name, &inner)
		if err != nil {
			s.stop()
			return nil, err
		}
		for key, value := range server.Env {
			inner.Env = append(inner.Env, key+"="+value)
		}
		inner.Path = append(inner.Path, filepath.Dir(server.Command))
		args, err := ui.Sandbox.Command(inner, append([]string{server.Command}, server.Args...))
		if err != nil {
			s.stop()
			return nil, err
		}
		client, err := mcp.Start(args, nil, s.box.sockets)
		if err != nil {
			s.stop()
			return nil, err
		}
		s.clients[name] = client
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		list, err := client.Tools(ctx)
		cancel()
		if err != nil {
			s.stop()
			return nil, err
		}
		for _, t := range list {
			if !slices.Contains(assistant.BlockedTools[name], t.Name) {
				s.lists[name] = append(s.lists[name], t)
			}
		}
		s.order = append(s.order, name)
	}
	return s, nil
}

func (s *toolSession) stop() {
	for _, client := range s.clients {
		client.Close()
	}
	s.box.cleanUp()
}

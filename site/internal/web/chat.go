package web

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
	"dashboard/internal/sandbox"
	"dashboard/internal/workspace"
)

// The MCPs tab: chat with an AI agent (Claude Code, Codex or Gemini CLI) that can use the
// MyFitnessPal and Anki MCP servers and read the docs. An answer runs in the background; the
// page follows it over server-sent events, so a reload picks it up again.

// ---------- the page ----------

type mcpChoice struct {
	ID, Label string
	Enabled   bool
	Ready     bool
}

type providerChoice struct {
	ID, Label string
	Selected  bool
	Installed bool
	Available bool
}

type modelChoice struct {
	ID, Label, Description string
	Selected               bool
}

type suggestion struct{ Icon, Text string }

type chatView struct {
	Provider     assistant.Provider
	Providers    []providerChoice
	Models       []modelChoice
	CustomModel  bool // the "Custom…" box is open
	MCPs         []mcpChoice
	MyFitnessPal myfitnesspal.Setup
	Anki         anki.Status
	Messages     []assistant.Message
	Question     *assistant.Message // being answered
	Reply        *assistant.Message
	Busy         bool
	Status       string
	StatusLink   string // a tab that fixes what's wrong
	Ready        bool   // every turned-on MCP works
	Blocked      bool   // MCPs are on, but none works: nothing to chat with
	NoAI         bool   // the user's policy allows no AI at all
	Instructions bool   // the dashboard's prompt, notes and data go with each chat
}

// allowedMCPs is the conversation's turned-on MCPs that the user's policy allows.
func allowedMCPs(policy auth.Policy, conversation *assistant.Conversation) []string {
	var allowed []string
	for _, name := range conversation.EnabledMCPs() {
		if policy.AllowsMCP(name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func (v chatView) MCPOn(id string) bool {
	return slices.ContainsFunc(v.MCPs, func(m mcpChoice) bool { return m.ID == id && m.Enabled })
}

// NeedsMyFitnessPalSetup: the MyFitnessPal MCP is on but not signed in.
func (v chatView) NeedsMyFitnessPalSetup() bool {
	return v.MCPOn("myfitnesspal") && !v.MyFitnessPal.SignedIn
}

func (v chatView) EmptyTitle() string {
	if v.MCPOn("anki") && !v.MCPOn("myfitnesspal") {
		return "What do you want to study?"
	}
	return "What did you eat today?"
}

var (
	foodSuggestions = []suggestion{{"🍽️", "What should I eat for the rest of today?"}, {"🍳", "Log 2 eggs and a slice of toast for breakfast"},
		{"🧂", "Why is my sodium so high today?"}, {"💪", "How am I doing on protein this week?"}}
	ankiSuggestions = []suggestion{{"🗂️", "How many Anki cards are due today?"}, {"✍️", "Make 5 flashcards from my nutrition plan"}}
)

// Suggestions are example questions for the empty chat, for the turned-on MCPs.
func (v chatView) Suggestions() []suggestion {
	food, cards := v.MCPOn("myfitnesspal"), v.MCPOn("anki")
	switch {
	case food && cards:
		return append(slices.Clone(foodSuggestions[:2]), ankiSuggestions...)
	case food:
		return foodSuggestions
	case cards:
		return ankiSuggestions
	}
	return nil
}

func (v chatView) EmptyHint() string {
	hint := "Ask about your plan"
	if v.MCPOn("myfitnesspal") {
		hint += ", or tell it what you ate and it'll log it"
	}
	if v.MCPOn("anki") {
		hint += ", or have it make or find Anki cards"
	}
	return hint + "."
}

// ModelLabel is a model's menu label, for the line under an answer.
func (v chatView) ModelLabel(model string) string {
	for _, known := range v.Provider.Models() {
		if known.ID == model {
			return known.Label
		}
	}
	return model
}

func (ui *UI) chatView(r *http.Request, customModel bool) chatView {
	conversation := ui.ws(r).Conversations.Load()
	policy := ui.user(r).Policy
	provider, allowed := allowedProvider(policy, ui.Providers, conversation.Provider)
	if !allowed {
		provider = ui.Providers[0]
	}
	view := chatView{
		NoAI:         !allowed,
		Provider:     provider,
		CustomModel:  customModel,
		MyFitnessPal: ui.ws(r).MyFitnessPal.Client().Setup(),
		Anki:         ui.ws(r).Anki().Status(),
		Messages:     conversation.Messages,
		Instructions: !conversation.NoInstructions,
	}
	for _, p := range ui.Providers {
		if !policy.AllowsProvider(p.ID()) {
			continue
		}
		view.Providers = append(view.Providers, providerChoice{ID: p.ID(), Label: p.Label(), Selected: p.ID() == provider.ID(),
			Installed: p.Installed(), Available: p.Available()})
	}
	chosen := conversation.Model()
	defaultLabel := "Default model"
	if saved := ui.ws(r).Secrets.Get(localModelSecret); provider.ID() == "local" && saved != "" {
		defaultLabel = "Default (" + saved + ")"
	}
	view.Models = []modelChoice{{ID: "", Label: defaultLabel, Selected: chosen == ""}}
	knownModel := chosen == ""
	for _, model := range ui.modelsFor(r, provider) {
		view.Models = append(view.Models, modelChoice{ID: model.ID, Label: model.Label, Description: model.Description, Selected: model.ID == chosen})
		knownModel = knownModel || model.ID == chosen
	}
	if !knownModel {
		view.Models = append(view.Models, modelChoice{ID: chosen, Label: chosen, Selected: true})
	}

	enabled := allowedMCPs(policy, conversation)
	for _, m := range []mcpChoice{
		{ID: "myfitnesspal", Label: "MyFitnessPal", Enabled: slices.Contains(enabled, "myfitnesspal"), Ready: view.MyFitnessPal.SignedIn},
		{ID: "anki", Label: "Anki", Enabled: slices.Contains(enabled, "anki"), Ready: view.Anki.Connected},
	} {
		if policy.AllowsMCP(m.ID) {
			view.MCPs = append(view.MCPs, m)
		}
	}
	view.Ready = true
	anyReady, ankiOff := false, false
	for _, m := range view.MCPs {
		if m.Enabled {
			view.Ready = view.Ready && m.Ready
			anyReady = anyReady || m.Ready
			ankiOff = ankiOff || (m.ID == "anki" && !m.Ready)
		}
	}
	view.Blocked = (len(enabled) > 0 && !anyReady) || view.NoAI
	switch {
	case view.NoAI:
		view.Status = "Your admin hasn't turned on an AI for you"
	case len(enabled) == 0:
		view.Status = provider.Label() + " · notes only"
	case view.Ready:
		view.Status = provider.Label() + " · ready"
	case ankiOff:
		view.Status, view.StatusLink = provider.Label()+" · Anki isn't connected", "/anki"
	default:
		view.Status = provider.Label() + " · needs setup"
	}

	question, reply, running, _ := ui.ws(r).Live.Current()
	if running {
		view.Busy, view.Question, view.Reply = true, &question, &reply
	}
	return view
}

func (ui *UI) chatPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "mcps", "MCPs", "chat", ui.chatView(r, false))
}

func (ui *UI) chatCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "chat-card", ui.chatView(r, false))
}

// ---------- sending and following an answer ----------

func (ui *UI) sendMessage(w http.ResponseWriter, r *http.Request) {
	message := formText(r, "message")
	if message == "" || len([]rune(message)) > 4000 {
		fail(w, problem.Invalidf("type a message"))
		return
	}
	conversation := ui.ws(r).Conversations.Load()
	provider, allowed := allowedProvider(ui.user(r).Policy, ui.Providers, conversation.Provider)
	if !allowed {
		fail(w, problem.Forbiddenf("Your admin hasn't turned on an AI for you"))
		return
	}
	if !provider.Available() {
		fail(w, problem.Invalidf(provider.Label()+" isn't installed"))
		return
	}
	if ui.SandboxErr != nil {
		fail(w, problem.Unavailablef("The AI sandbox can't run on this server: "+ui.SandboxErr.Error()))
		return
	}
	ws, user := ui.ws(r), ui.user(r)
	env, err := agentKey(ws, provider)
	if err != nil {
		fail(w, err)
		return
	}
	if ws.Chat.Busy() || !ws.Live.Start(message) {
		fail(w, problem.Conflictf(assistant.ErrBusy.Error()))
		return
	}
	caps := capabilitiesFor(user.Policy)
	if provider.ID() == "local" {
		caps.Web = false // web search is Anthropic's own service, which a local server doesn't have
	}
	run := &agentRun{ui: ui, ws: ws, provider: provider, conversation: conversation, message: message,
		mcps: allowedMCPs(user.Policy, conversation), caps: caps, env: env}
	go run.answer()
	ui.render(w, "chat-card", ui.chatView(r, false))
}

// agentKey is the user's own API key for the provider, as the CLI's environment. Everyone
// brings their own: no chat spends the server's sign-in, and the sandbox holds no other login.
func agentKey(ws *workspace.Workspace, provider assistant.Provider) ([]string, error) {
	if provider.ID() == "local" {
		if ws.Secrets.Get(localURLSecret) == "" {
			return nil, problem.Invalidf("Add your local model server's address on the Account page first")
		}
		// Claude Code talks to the bridge inside the sandbox (see relayLocal), which holds the real
		// address and key; Claude Code only needs some token to send.
		return []string{"ANTHROPIC_BASE_URL=" + sandbox.LocalURL, "ANTHROPIC_AUTH_TOKEN=local"}, nil
	}
	key := assistant.APIKeys[provider.ID()]
	value := ws.Secrets.Get(key.Name)
	if value == "" {
		return nil, problem.Invalidf("Add your " + key.Label + " API key on the Account page first")
	}
	return key.KeyEnv(value), nil
}

// recordFailure keeps the question and why it got no answer, so the chat shows them.
func recordFailure(conversation *assistant.Conversation, question string, provider assistant.Provider, err error) {
	asked := len(conversation.Messages) > 0 && conversation.Messages[len(conversation.Messages)-1].Text == question
	if !asked {
		conversation.Messages = append(conversation.Messages, assistant.Message{Role: "user", Text: question})
	}
	conversation.Messages = append(conversation.Messages, assistant.Message{Role: "assistant", Provider: provider.ID(), Error: err.Error()})
}

// chatStream sends the reply as it grows ("reply" events with the bubble's HTML), then "done".
func (ui *UI) chatStream(w http.ResponseWriter, r *http.Request) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		http.Error(w, "streaming isn't supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	view := ui.chatView(r, false)
	for {
		_, reply, running, changed := ui.ws(r).Live.Current()
		html, err := ui.renderToHTML("chat-reply-body", chatReply{Message: reply, Live: running, Chat: &view})
		if err != nil {
			log.Printf("rendering the reply: %v", err)
			return
		}
		sendEvent(w, "reply", string(html))
		if !running {
			sendEvent(w, "done", "")
			flusher.Flush()
			return
		}
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

// chatReply is an answer's bubble: Live while it's still being written.
type chatReply struct {
	Message assistant.Message
	Live    bool
	Chat    *chatView
}

// sendEvent writes one server-sent event. Every line of the data gets its own "data:" prefix.
func sendEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

func (ui *UI) stopAnswer(w http.ResponseWriter, r *http.Request) {
	ui.ws(r).Chat.Stop()
	nothing(w)
}

// ---------- settings ----------

// newChat starts over, with the same AI or (from the AI menu) another one.
func (ui *UI) newChat(w http.ResponseWriter, r *http.Request) {
	ui.startOver(r, w, ui.ws(r).Conversations.Load().Provider)
}

func (ui *UI) chooseProvider(w http.ResponseWriter, r *http.Request) {
	provider, known := ui.Providers.Get(r.FormValue("provider"))
	if !known {
		fail(w, problem.Invalidf("unknown AI"))
		return
	}
	if !ui.user(r).Policy.AllowsProvider(provider.ID()) {
		fail(w, problem.Forbiddenf("Your admin hasn't turned on "+provider.Label()+" for you"))
		return
	}
	if !provider.Installed() {
		announce(w, say(provider.Label()+" downloads through Nix on first use, so the first answer is slower"))
	}
	ui.startOver(r, w, provider.ID())
}

func (ui *UI) startOver(r *http.Request, w http.ResponseWriter, provider string) {
	if ui.ws(r).Chat.Busy() {
		fail(w, problem.Conflictf("wait for the current answer, or press Stop"))
		return
	}
	if err := ui.ws(r).Conversations.StartNew(provider); err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "chat-card", ui.chatView(r, false))
}

// chooseModel sets the model for the current AI ("" = its default), from the next message.
// "Custom…" opens a box to type one.
func (ui *UI) chooseModel(w http.ResponseWriter, r *http.Request) {
	model := formText(r, "model")
	if model == "__custom" {
		model = formText(r, "custom_model")
		if model == "" {
			ui.render(w, "chat-card", ui.chatView(r, true))
			return
		}
	}
	if model != "" && !assistant.ModelNamePattern.MatchString(model) {
		fail(w, problem.Invalidf("that doesn't look like a model name"))
		return
	}
	ui.changeConversation(r, w, func(conversation *assistant.Conversation) { conversation.SetModel(model) })
}

// toggleMCP turns an MCP server on or off for the next message.
func (ui *UI) toggleMCP(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("mcp")
	if !slices.Contains(assistant.KnownMCPs, name) {
		fail(w, problem.Invalidf("unknown MCP"))
		return
	}
	if !ui.user(r).Policy.AllowsMCP(name) {
		fail(w, problem.Forbiddenf("Your admin hasn't turned on that MCP for you"))
		return
	}
	ui.changeConversation(r, w, func(conversation *assistant.Conversation) {
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
	})
}

// toggleInstructions turns the dashboard's prompt (with the user's notes and data) on or off.
func (ui *UI) toggleInstructions(w http.ResponseWriter, r *http.Request) {
	ui.changeConversation(r, w, func(conversation *assistant.Conversation) {
		conversation.NoInstructions = !conversation.NoInstructions
	})
}

func (ui *UI) changeConversation(r *http.Request, w http.ResponseWriter, change func(*assistant.Conversation)) {
	if ui.ws(r).Chat.Busy() {
		fail(w, problem.Conflictf("wait for the current answer"))
		return
	}
	conversation := ui.ws(r).Conversations.Load()
	change(conversation)
	if err := ui.ws(r).Conversations.Save(conversation); err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "chat-card", ui.chatView(r, false))
}

// modelsFor is the model menu: the provider's own, or for a local model what the user's server
// says it has.
func (ui *UI) modelsFor(r *http.Request, provider assistant.Provider) []assistant.Model {
	if provider.ID() != "local" {
		return provider.Models()
	}
	server := localServer(ui.ws(r))
	if server.Base == "" {
		return nil
	}
	names, _ := server.CachedModels(false)
	var models []assistant.Model
	for _, name := range names {
		if assistant.ModelNamePattern.MatchString(name) {
			models = append(models, assistant.Model{ID: name, Label: name})
		}
	}
	return models
}

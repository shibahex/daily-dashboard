package web

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
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
	}
	for _, p := range ui.Providers {
		if !policy.AllowsProvider(p.ID()) {
			continue
		}
		view.Providers = append(view.Providers, providerChoice{ID: p.ID(), Label: p.Label(), Selected: p.ID() == provider.ID(),
			Installed: p.Installed(), Available: p.Available()})
	}
	chosen := conversation.Model()
	view.Models = []modelChoice{{ID: "", Label: "Default model", Selected: chosen == ""}}
	knownModel := chosen == ""
	for _, model := range provider.Models() {
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
	ws := ui.ws(r)
	home, env, err := agentAccount(ws, provider)
	if err != nil {
		fail(w, err)
		return
	}
	if ws.Chat.Busy() || !ws.Live.Start(message) {
		fail(w, problem.Conflictf(assistant.ErrBusy.Error()))
		return
	}

	servers := ui.enabledServers(r, conversation)
	request := assistant.Request{
		Home:         home,
		Env:          env,
		Message:      message,
		SessionID:    conversation.SessionID,
		Model:        conversation.Model(),
		Servers:      servers,
		Instructions: assistant.Instructions(time.Now(), assistant.Context(ws.Paths.Docs, ws.Paths.Plan(), ws.Paths.Data), servers),
		WorkDir:      ws.AssistantDir(),
	}
	go answer(ws, provider, conversation, request)
	ui.render(w, "chat-card", ui.chatView(r, false))
}

// agentAccount is the HOME and API key the user's agent runs with. Everyone but the host must
// use their own key: their chats never spend the server's own sign-in, and their agent never
// sees the server's logins or anyone else's session history.
func agentAccount(ws *workspace.Workspace, provider assistant.Provider) (home string, env []string, err error) {
	key := assistant.APIKeys[provider.ID()]
	value := ws.Secrets.Get(key.Name)
	if ws.Host {
		if value == "" {
			return "", nil, nil
		}
		return "", key.KeyEnv(value), nil
	}
	if value == "" {
		return "", nil, problem.Invalidf("Add your " + key.Label + " API key on the Account page first")
	}
	home = ws.Paths.DataFile("agent-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", nil, err
	}
	return home, key.KeyEnv(value), nil
}

// answer runs in the background, so the answer is saved even if the page goes away.
func answer(ws *workspace.Workspace, provider assistant.Provider, conversation *assistant.Conversation, request assistant.Request) {
	defer ws.Live.Finish()
	outcome, err := ws.Chat.Answer(provider, conversation, request, ws.Live.Progress)
	if err != nil {
		recordFailure(conversation, request.Message, provider, err)
	}
	if err := ws.Conversations.Save(conversation); err != nil {
		log.Printf("saving the chat: %v", err)
	}
	if outcome.ChangedDiary {
		ws.MyFitnessPal.Sync() // so the Today tab shows the change right away
	}
}

// recordFailure keeps the question and why it got no answer, so the chat shows them.
func recordFailure(conversation *assistant.Conversation, question string, provider assistant.Provider, err error) {
	asked := len(conversation.Messages) > 0 && conversation.Messages[len(conversation.Messages)-1].Text == question
	if !asked {
		conversation.Messages = append(conversation.Messages, assistant.Message{Role: "user", Text: question})
	}
	conversation.Messages = append(conversation.Messages, assistant.Message{Role: "assistant", Provider: provider.ID(), Error: err.Error()})
}

// enabledServers is how the agent starts each turned-on MCP server.
func (ui *UI) enabledServers(r *http.Request, conversation *assistant.Conversation) []assistant.NamedServer {
	var servers []assistant.NamedServer
	for _, name := range allowedMCPs(ui.user(r).Policy, conversation) {
		switch name {
		case "myfitnesspal":
			servers = append(servers, assistant.NamedServer{Name: name, Server: ui.ws(r).MyFitnessPal.Client().MCPServer()})
		case "anki":
			if server, ok := ui.ws(r).Anki().MCPServer(); ok {
				servers = append(servers, assistant.NamedServer{Name: name, Server: server})
			}
		}
	}
	return servers
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

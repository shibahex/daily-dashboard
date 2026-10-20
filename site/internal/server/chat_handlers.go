package server

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"dashboard/internal/assistant"
	"dashboard/internal/myfitnesspal"
)

func (s *Server) routeChat(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat", s.chatState)
	mux.HandleFunc("POST /api/chat", handlePost(s.sendMessage))
	mux.HandleFunc("POST /api/chat/new", handlePost(s.newChat))
	mux.HandleFunc("POST /api/chat/model", handlePost(s.chooseModel))
	mux.HandleFunc("POST /api/chat/mcps", handlePost(s.chooseMCPs))
	mux.HandleFunc("POST /api/chat/stop", handlePost(s.stopAnswer))
}

// mcpChoice is one MCP server in the tab's toggles.
type mcpChoice struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	Setup   any    `json:"setup"`
}

func (s *Server) mcpChoices(conversation *assistant.Conversation) []mcpChoice {
	enabled := conversation.EnabledMCPs()
	mfpSetup, ankiStatus := s.MyFitnessPal.Client().Setup(), s.Anki.Status()
	return []mcpChoice{
		{ID: "myfitnesspal", Label: "MyFitnessPal", Enabled: slices.Contains(enabled, "myfitnesspal"), Ready: mfpSetup.SignedIn, Setup: mfpSetup},
		{ID: "anki", Label: "Anki", Enabled: slices.Contains(enabled, "anki"), Ready: ankiStatus.Connected, Setup: ankiStatus},
	}
}

// enabledServers is how the agent starts each turned-on MCP server.
func (s *Server) enabledServers(conversation *assistant.Conversation) []assistant.NamedServer {
	var servers []assistant.NamedServer
	for _, name := range conversation.EnabledMCPs() {
		switch name {
		case "myfitnesspal":
			servers = append(servers, assistant.NamedServer{Name: name, Server: s.MyFitnessPal.Client().MCPServer()})
		case "anki":
			servers = append(servers, assistant.NamedServer{Name: name, Server: s.Anki.MCPServer()})
		}
	}
	return servers
}

// chatState is everything the MCPs tab shows.
type chatState struct {
	Provider  string                   `json:"provider"`
	SessionID string                   `json:"session_id,omitempty"`
	Messages  []assistant.Message      `json:"messages"`
	Models    map[string]string        `json:"models,omitempty"`
	Busy      bool                     `json:"busy"`
	Providers []assistant.ProviderInfo `json:"providers"`
	MCP       myfitnesspal.Setup       `json:"mcp"`
	MCPs      []mcpChoice              `json:"mcps"`
}

func (s *Server) chatState(w http.ResponseWriter, r *http.Request) {
	conversation := s.Conversations.Load()
	writeJSON(w, http.StatusOK, chatState{
		Provider: conversation.Provider, SessionID: conversation.SessionID, Messages: conversation.Messages,
		Models: conversation.Models, Busy: s.Runner.Busy(), Providers: s.Providers.Info(),
		MCP: s.MyFitnessPal.Client().Setup(), MCPs: s.mcpChoices(conversation),
	})
}

// doneUpdate is the last line of a streamed answer.
type doneUpdate struct {
	Type    string  `json:"type"`    // "done"
	Changed bool    `json:"changed"` // the MyFitnessPal diary changed
	Anki    bool    `json:"anki"`    // the Anki collection changed
	Error   *string `json:"error"`
}

// sendMessage: {"message"} streams the answer as NDJSON lines (see assistant.Update), ending
// with a doneUpdate.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, body Body) {
	message := body.TrimmedString("message")
	if message == "" || len([]rune(message)) > 4000 {
		writeError(w, http.StatusBadRequest, "type a message")
		return
	}
	conversation := s.Conversations.Load()
	provider, _ := s.Providers.Get(conversation.Provider)
	if !provider.Available() {
		writeError(w, http.StatusBadRequest, provider.Label()+" isn't installed")
		return
	}
	if s.Runner.Busy() {
		writeError(w, http.StatusConflict, assistant.ErrBusy.Error())
		return
	}

	servers := s.enabledServers(conversation)
	request := assistant.Request{
		Message:      message,
		SessionID:    conversation.SessionID,
		Model:        conversation.Model(),
		Servers:      servers,
		Instructions: assistant.Instructions(time.Now(), s.Paths.Root, servers),
		RepoDir:      s.Paths.Root,
		WorkDir:      s.assistantWorkDir(),
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	stream := newLineStream(w)

	outcome, err := s.Runner.Answer(provider, conversation, request, func(u assistant.Update) { stream.send(u) })
	if err != nil {
		problem := err.Error()
		stream.send(doneUpdate{Type: "done", Error: &problem})
		return
	}
	if err := s.Conversations.Save(conversation); err != nil {
		log.Printf("saving the chat: %v", err)
	}
	if outcome.ChangedDiary {
		s.MyFitnessPal.Sync() // so the Today tab shows the change right away
	}
	done := doneUpdate{Type: "done", Changed: outcome.ChangedDiary, Anki: outcome.ChangedAnki}
	if outcome.Reply.Error != "" {
		done.Error = &outcome.Reply.Error
	}
	stream.send(done)
}

// lineStream writes one JSON value per line and flushes it. When the page goes away it stops
// writing, but the answer keeps going so it's still saved.
type lineStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	gone    bool
}

func newLineStream(w http.ResponseWriter) *lineStream {
	flusher, _ := w.(http.Flusher)
	return &lineStream{w: w, flusher: flusher}
}

func (l *lineStream) send(v any) {
	if l.gone {
		return
	}
	line, _ := json.Marshal(v)
	if _, err := l.w.Write(append(line, '\n')); err != nil {
		l.gone = true
		return
	}
	if l.flusher != nil {
		l.flusher.Flush()
	}
}

// newChat: {"provider"?} starts over, optionally with another AI.
func (s *Server) newChat(w http.ResponseWriter, r *http.Request, body Body) {
	if s.Runner.Busy() {
		writeError(w, http.StatusConflict, "wait for the current answer, or press Stop")
		return
	}
	provider := body.String("provider")
	if provider == "" {
		provider = s.Conversations.Load().Provider
	}
	if _, known := s.Providers.Get(provider); !known {
		writeError(w, http.StatusBadRequest, "unknown AI")
		return
	}
	if err := s.Conversations.StartNew(provider); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

// chooseModel: {"model"} sets the model for the current AI ("" = its default), from the next message.
func (s *Server) chooseModel(w http.ResponseWriter, r *http.Request, body Body) {
	model := body.TrimmedString("model")
	if model != "" && !assistant.ModelNamePattern.MatchString(model) {
		writeError(w, http.StatusBadRequest, "that doesn't look like a model name")
		return
	}
	if s.Runner.Busy() {
		writeError(w, http.StatusConflict, "wait for the current answer")
		return
	}
	conversation := s.Conversations.Load()
	conversation.SetModel(model)
	s.saveConversation(w, conversation, nil)
}

// chooseMCPs: {"mcps": ["myfitnesspal", ...]} sets which MCP servers the AI gets, from the
// next message.
func (s *Server) chooseMCPs(w http.ResponseWriter, r *http.Request, body Body) {
	if s.Runner.Busy() {
		writeError(w, http.StatusConflict, "wait for the current answer")
		return
	}
	chosen := []string{}
	for _, name := range body.Strings("mcps") {
		if slices.Contains(assistant.KnownMCPs, strings.TrimSpace(name)) {
			chosen = append(chosen, name)
		}
	}
	conversation := s.Conversations.Load()
	conversation.MCPs = &chosen
	s.saveConversation(w, conversation, map[string]any{"ok": true, "mcps": s.mcpChoices(conversation)})
}

func (s *Server) saveConversation(w http.ResponseWriter, conversation *assistant.Conversation, answer any) {
	if err := s.Conversations.Save(conversation); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if answer == nil {
		writeOK(w)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func (s *Server) stopAnswer(w http.ResponseWriter, r *http.Request, body Body) {
	s.Runner.Stop()
	writeOK(w)
}

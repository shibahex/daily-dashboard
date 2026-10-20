package server

import (
	"errors"
	"net/http"

	"dashboard/internal/anki"
)

func (s *Server) routeAnki(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/anki/status", s.ankiStatus)
	mux.HandleFunc("GET /api/anki/overview", s.ankiOverview)
	mux.HandleFunc("GET /api/anki/history", s.ankiHistory)
	mux.HandleFunc("POST /api/anki", handlePost(s.ankiAction))
	mux.HandleFunc("POST /api/anki/open", handlePost(s.openAnki))
}

func (s *Server) ankiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Anki.Status())
}

func (s *Server) ankiOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.Anki.Overview()
	switch {
	case anki.IsUnreachable(err):
		writeError(w, http.StatusBadGateway, err.Error()+". Is Anki open with AnkiConnect installed?")
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, overview)
	}
}

func (s *Server) ankiHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Anki.History(s.ankiHistoryFile()))
}

// ankiAction: {"action", "params"} runs one AnkiConnect action, if it's on the allowed list.
func (s *Server) ankiAction(w http.ResponseWriter, r *http.Request, body Body) {
	action := body.String("action")
	params := body.Raw("params")
	if !body.IsSet("params") {
		params = []byte("{}")
	}
	if _, isObject := decodeBody(params); !anki.AllowedActions[action] || !isObject {
		writeError(w, http.StatusBadRequest, "action not allowed: "+action)
		return
	}
	result, err := s.Anki.Call(action, params)
	var actionErr anki.ActionError
	switch {
	case errors.As(err, &actionErr):
		writeOutcome(w, false, actionErr.Message, http.StatusBadRequest)
	case err != nil:
		writeOutcome(w, false, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	}
}

// openAnki starts the Anki app unless it's already open.
func (s *Server) openAnki(w http.ResponseWriter, r *http.Request, body Body) {
	if anki.AppRunning() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": false})
		return
	}
	if !anki.AppInstalled() {
		writeError(w, http.StatusBadRequest, "Anki isn't installed")
		return
	}
	if err := anki.OpenApp(); err != nil {
		writeError(w, http.StatusInternalServerError, "couldn't start Anki: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true})
}

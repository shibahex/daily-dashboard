// Package server is the dashboard's HTTP API and page. It only listens on 127.0.0.1, and every
// change needs a JSON body plus an X-Dashboard header, so other websites open in the browser
// can't trigger one.
package server

import (
	"fmt"
	"net/http"
	"path/filepath"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/config"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/store"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

// Server holds everything the handlers use.
type Server struct {
	Port          int
	Paths         config.Paths
	Store         *store.Store
	Tasks         *taskwarrior.Client
	Time          timewarrior.Client
	MyFitnessPal  *myfitnesspal.Syncer
	Anki          anki.Client
	Providers     assistant.Providers
	Conversations *assistant.Conversations
	Runner        *assistant.Runner
}

func (s *Server) ankiHistoryFile() string  { return s.Paths.DataFile("anki-history.json") }
func (s *Server) assistantWorkDir() string { return filepath.Join(s.Paths.Data, "assistant") }

// Handler is the whole site: the page, its files, and the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routePage(mux)
	s.routeDaily(mux)
	s.routeTasks(mux)
	s.routeTime(mux)
	s.routeMyFitnessPal(mux)
	s.routeAnki(mux)
	s.routeChat(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown endpoint "+r.URL.Path)
	})
	return s.onlyLocalHost(mux)
}

// onlyLocalHost refuses requests whose Host isn't this server (DNS rebinding protection).
func (s *Server) onlyLocalHost(next http.Handler) http.Handler {
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.Port): true,
		fmt.Sprintf("localhost:%d", s.Port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			writeError(w, http.StatusForbidden, "bad host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

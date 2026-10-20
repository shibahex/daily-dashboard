package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// The page's own files: only plain names, so no other file can be reached.
var (
	stylesheetName = regexp.MustCompile(`^[\w-]+\.css$`)
	scriptName     = regexp.MustCompile(`^[\w-]+\.js$`)
)

func (s *Server) routePage(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.servePage)
	mux.HandleFunc("GET /index.html", s.servePage)
	mux.HandleFunc("GET /css/{file}", s.serveWebFile("css", stylesheetName, "text/css; charset=utf-8"))
	mux.HandleFunc("GET /js/{file}", s.serveWebFile("js", scriptName, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /api/docs", s.listDocs)
	mux.HandleFunc("GET /api/docs/{name}", s.serveDoc)
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	page, err := os.ReadFile(filepath.Join(s.Paths.Web, "index.html"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the page is missing")
		return
	}
	writeBytes(w, http.StatusOK, page, "text/html; charset=utf-8")
}

func (s *Server) serveWebFile(folder string, allowedName *regexp.Regexp, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		content, err := os.ReadFile(filepath.Join(s.Paths.Web, folder, name))
		if !allowedName.MatchString(name) || err != nil {
			writeError(w, http.StatusNotFound, "unknown endpoint "+r.URL.Path)
			return
		}
		writeBytes(w, http.StatusOK, content, contentType)
	}
}

func (s *Server) listDocs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.DocNames())
}

func (s *Server) serveDoc(w http.ResponseWriter, r *http.Request) {
	content, found := s.Store.Doc(r.PathValue("name"))
	if !found {
		writeError(w, http.StatusNotFound, "no such doc")
		return
	}
	writeBytes(w, http.StatusOK, content, "text/markdown; charset=utf-8")
}

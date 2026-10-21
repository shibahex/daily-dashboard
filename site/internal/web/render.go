package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"dashboard/internal/problem"
)

// ---------- templates ----------

// templateSet is every file in web/templates, parsed together so they can use each other's
// {{define}}s. It re-parses when a file changes, so editing a template only needs a reload.
type templateSet struct {
	dir      string
	mu       sync.Mutex
	parsed   *template.Template
	parsedAt time.Time
}

func newTemplateSet(webDir string) *templateSet {
	return &templateSet{dir: filepath.Join(webDir, "templates")}
}

func (t *templateSet) current() (*template.Template, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	files, _ := filepath.Glob(filepath.Join(t.dir, "*.html"))
	changed := t.parsed == nil
	for _, file := range files {
		if info, err := os.Stat(file); err == nil && info.ModTime().After(t.parsedAt) {
			changed = true
		}
	}
	if changed {
		parsed, err := template.New("").Funcs(templateFuncs).ParseFiles(files...)
		if err != nil {
			return nil, err
		}
		t.parsed, t.parsedAt = parsed, time.Now()
	}
	return t.parsed, nil
}

// render writes one template (a card, a row, a panel).
func (ui *UI) render(w http.ResponseWriter, name string, data any) {
	html, err := ui.renderToHTML(name, data)
	if err != nil {
		log.Printf("rendering %s: %v", name, err)
		http.Error(w, "couldn't show this: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(html))
}

func (ui *UI) renderToHTML(name string, data any) (template.HTML, error) {
	set, err := ui.templates.current()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// pageView is what the page frame (header, tabs) needs around a tab's content.
type pageView struct {
	Tab     string // "today", "tasks", ...
	Title   string
	Today   time.Time
	Content template.HTML
	Fill    bool // the content fills the window (the chat)
	User    string
	Admin   bool    // show the Admin link
	Nav     *dayNav // the Today tab's arrows around the date
	Sealed  bool    // the user's notes are encrypted in the browser (notes.js)
}

// renderPage renders a tab: its content template inside the page frame.
func (ui *UI) renderPage(r *http.Request, w http.ResponseWriter, tab, title, contentTemplate string, data any) {
	content, err := ui.renderToHTML(contentTemplate, data)
	if err != nil {
		log.Printf("rendering %s: %v", contentTemplate, err)
		http.Error(w, "couldn't show this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	view := pageView{Tab: tab, Title: title, Today: time.Now(), Content: content, Fill: tab == "mcps",
		User: ui.user(r).Username, Admin: ui.user(r).IsAdmin(), Sealed: sealedNotes(ui.user(r))}
	if tab == "today" {
		nav := newDayNav(pageDay(r))
		view.Nav = &nav
	}
	ui.render(w, "page", view)
}

// ---------- static files ----------

var staticName = regexp.MustCompile(`^(css|js)/[\w-]+\.(css|js)$`)

// staticFiles serves web/css and web/js, and nothing else.
func staticFiles(webDir string) http.Handler {
	files := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !staticName.MatchString(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// ---------- answering changes ----------

// Events other cards listen for (hx-trigger="tasks-changed from:body") to reload themselves.
const (
	tasksChanged     = "tasks-changed"
	timeChanged      = "time-changed"
	checklistChanged = "checklist-changed"
	planChanged      = "plan-changed"
	nutritionChanged = "nutrition-changed"
	ankiChanged      = "anki-changed"
)

// notice is a toast message. Undo adds a button that undoes the last Taskwarrior change.
type notice struct {
	Message string `json:"message"`
	Bad     bool   `json:"bad,omitempty"`
	Undo    bool   `json:"undo,omitempty"`
}

// announce tells the page what changed (so cards reload) and what to say. Call it before
// writing the body.
func announce(w http.ResponseWriter, message *notice, events ...string) {
	triggers := map[string]any{}
	for _, event := range events {
		triggers[event] = true
	}
	if message != nil {
		triggers["toast"] = message
	}
	if len(triggers) == 0 {
		return
	}
	encoded, _ := json.Marshal(triggers)
	w.Header().Set("HX-Trigger", asciiJSON(encoded))
}

// asciiJSON escapes everything past ASCII (✓, “quotes”) as \uXXXX: browsers read headers as
// Latin-1, so UTF-8 in one comes out garbled.
func asciiJSON(encoded []byte) string {
	var out strings.Builder
	for _, r := range string(encoded) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r > 0xFFFF:
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
		default:
			fmt.Fprintf(&out, "\\u%04x", r)
		}
	}
	return out.String()
}

func say(message string) *notice         { return &notice{Message: message} }
func sayUndoable(message string) *notice { return &notice{Message: message, Undo: true} }

// fail answers with the error's status and message; the page shows it as a toast.
func fail(w http.ResponseWriter, err error) {
	if problem.KindOf(err) == problem.Failed {
		log.Printf("failed: %v", err)
	}
	http.Error(w, err.Error(), problem.Status(err))
}

// nothing answers with no content (the target is emptied, e.g. a closed panel).
func nothing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

// formText is a form value without surrounding whitespace.
func formText(r *http.Request, key string) string { return strings.TrimSpace(r.FormValue(key)) }

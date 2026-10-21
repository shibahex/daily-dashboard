package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"dashboard/internal/auth"
	"dashboard/internal/httpx"
	"dashboard/internal/problem"
	"dashboard/internal/store"
)

// Sealed notes: for everyone but the host, notes are encrypted end to end. The browser holds the
// key (web/js/keys.js) and does everything with them (web/js/notes.js); the server only stores
// the encrypted blob and the user's wrapped key, and can read neither.

// sealedNotes reports whether the user's notes are sealed: every account but the host's, once
// its browser has made the key (at its first sign-in since).
func sealedNotes(user auth.User) bool { return !user.Host && user.NotesKey != nil }

// notesKey answers with the user's wrapped notes key, for their browser to open.
func (ui *UI) notesKey(w http.ResponseWriter, r *http.Request) {
	user := ui.user(r)
	if !sealedNotes(user) {
		httpx.WriteError(w, http.StatusNotFound, "this account's notes aren't sealed")
		return
	}
	writeNoStoreJSON(w, user.NotesKey)
}

// notesData answers with the sealed notes and their version. Before the first save it also sends
// the notes still in plain files, for the browser to seal (they're deleted once it has).
func (ui *UI) notesData(w http.ResponseWriter, r *http.Request) {
	if !sealedNotes(ui.user(r)) {
		httpx.WriteError(w, http.StatusNotFound, "this account's notes aren't sealed")
		return
	}
	st := ui.ws(r).Store
	version, blob := st.SealedNotes()
	answer := map[string]any{"version": version, "blob": blob}
	if version == 0 {
		answer["plain"] = st.PlainNotes()
	}
	writeNoStoreJSON(w, answer)
}

// saveNotesData stores a new sealed blob (version: the one it was made from). sealed lists the
// plain notes it now holds, which are deleted.
func (ui *UI) saveNotesData(w http.ResponseWriter, r *http.Request) {
	if !sealedNotes(ui.user(r)) {
		httpx.WriteError(w, http.StatusNotFound, "this account's notes aren't sealed")
		return
	}
	version, err := strconv.Atoi(r.FormValue("version"))
	if err != nil || version < 0 {
		fail(w, problem.Invalidf("which version of your notes is this?"))
		return
	}
	st := ui.ws(r).Store
	saved, err := st.SaveSealedNotes(version, r.FormValue("blob"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(problem.Status(err))
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "version": saved})
		return
	}
	// One name a line (a form post may turn the line breaks into \r\n); names have no spaces.
	if names := strings.Fields(r.FormValue("sealed")); len(names) > 0 {
		st.DropPlainNotes(names)
	}
	writeNoStoreJSON(w, map[string]int{"version": saved})
}

// browserNotes reads the notes a chat message brings (decrypted by the browser for that one
// answer): [{"title", "text"}], at most store.SealedNotesMax.
func browserNotes(r *http.Request) []store.PlainNote {
	raw := r.FormValue("notes")
	if raw == "" || len(raw) > store.SealedNotesMax {
		return nil
	}
	var notes []struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &notes) != nil {
		return nil
	}
	var out []store.PlainNote
	for _, n := range notes {
		name, err := store.NoteFileName(n.Title)
		if err != nil {
			name = "note.md"
		}
		out = append(out, store.PlainNote{Name: name, Text: n.Text})
	}
	return out
}

func writeNoStoreJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"dashboard/internal/auth"
	"dashboard/internal/httpx"
	"dashboard/internal/problem"
)

// Sealed data: everyone's notes, AI chat and AI keys are encrypted end to end. The
// browser holds the key (web/js/keys.js) and does everything with them (notes.js, assistant.js);
// the server only stores the encrypted blobs and the user's wrapped key, and can read neither.

// sealedNotes reports whether the user's notes (and AI chat) are sealed: every account, once its
// browser has made the key (at its first sign-in since; until then it's asked to sign in).
func sealedNotes(user auth.User) bool { return user.NotesKey != nil }

// notesKey answers with the user's wrapped notes key, for their browser to open.
func (ui *UI) notesKey(w http.ResponseWriter, r *http.Request) {
	user := ui.user(r)
	if !sealedNotes(user) {
		httpx.WriteError(w, http.StatusNotFound, "this account's notes aren't sealed")
		return
	}
	writeNoStoreJSON(w, user.NotesKey)
}

// sealedData answers with a sealed blob and its version. Before the first save it also sends
// what's still in plain form on the server, for the browser to seal (it's deleted once it has):
// the notes' files, or the chat the server-side AI kept.
func (ui *UI) sealedData(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sealedNotes(ui.user(r)) {
			httpx.WriteError(w, http.StatusNotFound, "this account's data isn't sealed")
			return
		}
		ws := ui.ws(r)
		version, blob := ws.Store.Sealed(kind)
		answer := map[string]any{"version": version, "blob": blob}
		if version == 0 {
			switch kind {
			case "notes":
				answer["plain"] = ws.Store.PlainNotes()
			case "chat":
				answer["plain"] = ws.Conversations.Load().Messages
			}
		}
		writeNoStoreJSON(w, answer)
	}
}

// saveSealedData stores a new blob (version: the one it was made from). With sealed, it also
// deletes the plain copies the blob now holds: the notes named there, or the old chat.
func (ui *UI) saveSealedData(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sealedNotes(ui.user(r)) {
			httpx.WriteError(w, http.StatusNotFound, "this account's data isn't sealed")
			return
		}
		version, err := strconv.Atoi(r.FormValue("version"))
		if err != nil || version < 0 {
			fail(w, problem.Invalidf("which version is this?"))
			return
		}
		ws := ui.ws(r)
		saved, err := ws.Store.SaveSealed(kind, version, r.FormValue("blob"))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(problem.Status(err))
			json.NewEncoder(w).Encode(map[string]any{"error": problem.Public(err), "version": saved})
			return
		}
		switch sealed := r.FormValue("sealed"); {
		case kind == "notes" && sealed != "":
			// One name a line (a form post may make the breaks \r\n); names have no spaces.
			ws.Store.DropPlainNotes(strings.Fields(sealed))
		case kind == "chat" && sealed != "":
			// The old chat, and the AI programs' own copies of it (their resumable sessions).
			ws.Conversations.StartNew(ws.Conversations.Load().Provider)
			os.RemoveAll(ws.Paths.DataFile("agent-home"))
		case kind == "settings" && sealed != "":
			// Keys saved here before keys moved into the browser: it has sealed them.
			ws.Secrets.Clear()
		}
		writeNoStoreJSON(w, map[string]int{"version": saved})
	}
}

func writeNoStoreJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

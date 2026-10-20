package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxBodyBytes = 256_000

// writeJSON sends v as JSON. Nothing the dashboard serves should be cached.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"couldn't encode the answer"}`)
	}
	writeBytes(w, status, data, "application/json")
}

func writeBytes(w http.ResponseWriter, status int, data []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeOK sends {"ok": true}.
func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// outcome is the {"ok": ..., "error": ...} answer most changes send.
type outcome struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// writeOutcome sends ok/error with 200 for success and failStatus otherwise.
func writeOutcome(w http.ResponseWriter, ok bool, errorText string, failStatus int) {
	status := http.StatusOK
	if !ok {
		status = failStatus
	}
	writeJSON(w, status, outcome{OK: ok, Error: errorText})
}

// handlePost wraps a handler for a change: it checks the headers only this page sends, then
// hands over the JSON object.
func handlePost(handler func(http.ResponseWriter, *http.Request, Body)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dashboard") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, http.StatusForbidden, "missing dashboard headers")
			return
		}
		if r.ContentLength > maxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "too large")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		if err != nil || len(data) > maxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "too large")
			return
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			data = []byte("{}")
		}
		if !json.Valid(data) {
			writeError(w, http.StatusBadRequest, "bad json")
			return
		}
		body, isObject := decodeBody(data)
		if !isObject {
			writeError(w, http.StatusBadRequest, "expected an object")
			return
		}
		handler(w, r, body)
	}
}

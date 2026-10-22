package web

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"dashboard/internal/problem"
)

// The model relay, an admin's testing switch (Admin page, "Skip certificate checks"): browsers
// can't be told to accept a bad certificate, so for the https model servers an admin listed, the
// browser AI sends its requests here and the server passes them on without checking the
// server's certificate. Only the model list and chat completions, only to those servers. While
// it's on, people's chats and notes pass through this server (assistant.js asks them first).

var relayClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, // the point of the relay
		ResponseHeaderTimeout: 5 * time.Minute,                       // a slow model's first token
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// modelRelay passes ?to= on: GET for ".../models", POST for ".../chat/completions". The model
// server's key comes in X-Model-Key (Authorization is the plugin's token here).
func (ui *UI) modelRelay(w http.ResponseWriter, r *http.Request) {
	target, err := ui.relayTarget(r)
	if err != nil {
		fail(w, err)
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		body = http.MaxBytesReader(w, r.Body, 16<<20)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		fail(w, problem.Invalidf("That isn't a model server address"))
		return
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key := r.Header.Get("X-Model-Key"); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := relayClient.Do(request)
	if err != nil {
		fail(w, problem.Unavailablef("Couldn't reach "+target+" from the server"))
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	// Streamed answers go on as they come.
	flusher := http.NewResponseController(w)
	buffer := make([]byte, 32<<10)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, werr := w.Write(buffer[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

// relayTarget is the address to pass the request on to, if the relay may.
func (ui *UI) relayTarget(r *http.Request) (string, error) {
	if !ui.user(r).Policy.AllowsProvider("local") {
		return "", problem.Forbiddenf("Your admin hasn't turned on the local model for you")
	}
	target, err := url.Parse(r.URL.Query().Get("to"))
	if err != nil || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		!slices.Contains(ui.Auth.RelayedModelServers(), target.Scheme+"://"+strings.ToLower(target.Host)) {
		return "", problem.Forbiddenf("The server only passes requests on to the model servers an admin listed, while certificate checks are off")
	}
	path := target.EscapedPath()
	if strings.Contains(path, "..") || !(r.Method == http.MethodGet && strings.HasSuffix(path, "/models") ||
		r.Method == http.MethodPost && strings.HasSuffix(path, "/chat/completions")) {
		return "", problem.Forbiddenf("The server only passes on the model list and chats")
	}
	return target.String(), nil
}

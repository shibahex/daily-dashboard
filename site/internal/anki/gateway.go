package anki

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Gateway is the only way the chat agents' Anki MCP reaches AnkiConnect. AnkiConnect can
// download URLs, read files anywhere on the computer, and store card HTML that loads remote
// content or runs scripts whenever the card is shown. An agent following instructions planted
// in a shared deck could use any of those to fetch or leak data, so the gateway passes only
// the actions the MCP needs and refuses anything that points outside the collection.
//
// Each user's Anki MCP gets its own key (ANKI_CONNECT_API_KEY), and the key decides whose
// AnkiConnect the request goes to, so one user's agent can never reach another user's Anki.
type Gateway struct {
	URL string // http://127.0.0.1:<port>, for ANKI_CONNECT_URL

	host string
	mu   sync.Mutex
	keys map[string]route // key → whose Anki
}

type route struct {
	owner    string
	upstream Client
}

// gatewayActions are the AnkiConnect actions the Anki MCP uses, minus storeMediaFile (it
// downloads URLs and copies local files). Anything else, including `multi`, is refused.
var gatewayActions = map[string]bool{
	"version": true, "addNote": true, "addNotes": true, "addTags": true, "answerCards": true,
	"areSuspended": true, "cardReviews": true, "cardsInfo": true, "cardsModTime": true,
	"changeDeck": true, "clearUnusedTags": true, "createDeck": true, "createModel": true,
	"deckNames": true, "deckNamesAndIds": true, "deleteMediaFile": true, "deleteNotes": true,
	"findCards": true, "findNotes": true, "forgetCards": true, "getDeckStats": true,
	"getEaseFactors": true, "getIntervals": true, "getMediaFilesNames": true,
	"getReviewsOfCards": true, "getTags": true, "guiAddCards": true, "guiBrowse": true,
	"guiCurrentCard": true, "guiDeckBrowser": true, "guiDeckOverview": true, "guiEditNote": true,
	"guiSelectCard": true, "guiSelectedNotes": true, "guiShowAnswer": true,
	"guiShowQuestion": true, "guiUndo": true, "modelFieldAdd": true, "modelFieldNames": true,
	"modelFieldRemove": true, "modelFieldRename": true, "modelFieldReposition": true,
	"modelNames": true, "modelStyling": true, "modelTemplates": true, "notesInfo": true,
	"removeTags": true, "replaceTags": true, "retrieveMediaFile": true, "setDueDate": true,
	"suspend": true, "sync": true, "unsuspend": true, "updateModelStyling": true,
	"updateModelTemplates": true, "updateNoteFields": true,
}

// Parameters that make AnkiConnect download or read a file: picture/audio/video lists on
// notes, and url/path/data on media.
var mediaSourceParams = map[string]bool{"picture": true, "audio": true, "video": true, "url": true, "path": true, "data": true}

// remoteContent is what makes Anki's card view reach outside the collection: a URL with a
// scheme or a protocol-relative one (//host), CSS url() and @import, and scripts, frames,
// event handlers and javascript: links.
var remoteContent = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://|(^|[^:/])//[^/\s]|url\s*\(|@import|` +
	`<\s*/?\s*(script|iframe|frame|object|embed|link|meta|base|form|svg|math)\b|\bon[a-z]+\s*=|javascript:`)

// StartGateway listens on a random port on 127.0.0.1. Register adds each user's AnkiConnect.
func StartGateway() (*Gateway, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &Gateway{URL: "http://" + listener.Addr().String(), host: listener.Addr().String(), keys: map[string]route{}}
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	return g, nil
}

// ServeOn answers on listener too: a unix socket the dashboard opens for one sandboxed run.
// Requests still need the owner's key. It returns when the listener is closed.
func (g *Gateway) ServeOn(listener net.Listener) {
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = g.host // the sandbox calls it 127.0.0.1:8765; the socket itself is the address
		g.ServeHTTP(w, r)
	})}
	server.Serve(listener)
}

// Register sends owner's requests to upstream (their AnkiConnect) and returns the key their
// MCP uses. An owner keeps one key; registering again only changes where it goes.
func (g *Gateway) Register(owner string, upstream Client) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	upstream.Gateway, upstream.GatewayKey = nil, ""
	for key, r := range g.keys {
		if r.owner == owner {
			g.keys[key] = route{owner: owner, upstream: upstream}
			return key
		}
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	key := base64.RawURLEncoding.EncodeToString(secret)
	g.keys[key] = route{owner: owner, upstream: upstream}
	return key
}

// Unregister stops owner's key from working (a deleted user).
func (g *Gateway) Unregister(owner string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, r := range g.keys {
		if r.owner == owner {
			delete(g.keys, key)
		}
	}
}

// Knows reports whether key still works (it's gone once its owner is unregistered).
func (g *Gateway) Knows(key string) bool {
	_, ok := g.upstreamFor(key)
	return ok
}

// upstreamFor is the AnkiConnect a key goes to.
func (g *Gateway) upstreamFor(key string) (Client, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for known, r := range g.keys {
		if subtle.ConstantTimeCompare([]byte(known), []byte(key)) == 1 {
			return r.upstream, true
		}
	}
	return Client{}, false
}

const maxGatewayBody = 8 << 20 // addNotes sends whole batches

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Host != g.host {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayBody+1))
	if err != nil || len(data) > maxGatewayBody {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(data, &request) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	upstream, known := g.upstreamFor(request.Key)
	if !known {
		answer(w, nil, errors.New("valid api key must be provided"))
		return
	}
	if upstream.URL == "" {
		answer(w, nil, errors.New("Anki isn't set up for this account yet (see the Anki tab)"))
		return
	}
	if err := checkRequest(request.Action, request.Params); err != nil {
		answer(w, nil, err)
		return
	}
	var params any = map[string]any{}
	if len(bytes.TrimSpace(request.Params)) > 0 && string(bytes.TrimSpace(request.Params)) != "null" {
		params = request.Params
	}
	result, err := upstream.Call(request.Action, params)
	answer(w, result, err)
}

// checkRequest refuses actions outside the list and parameters that reach outside the collection.
func checkRequest(action string, rawParams json.RawMessage) error {
	if !gatewayActions[action] {
		return fmt.Errorf("the dashboard doesn't let the assistant use the AnkiConnect action %q", action)
	}
	if len(bytes.TrimSpace(rawParams)) == 0 {
		return nil
	}
	var params any
	decoder := json.NewDecoder(bytes.NewReader(rawParams))
	decoder.UseNumber()
	if decoder.Decode(&params) != nil {
		return errors.New("bad params")
	}
	return checkValue(params, "")
}

func checkValue(value any, key string) error {
	switch v := value.(type) {
	case map[string]any:
		for k, inner := range v {
			if mediaSourceParams[k] {
				return fmt.Errorf("the dashboard doesn't let the assistant add media from URLs or files (%q)", k)
			}
			if err := checkValue(inner, k); err != nil {
				return err
			}
		}
	case []any:
		for _, inner := range v {
			if err := checkValue(inner, key); err != nil {
				return err
			}
		}
	case string:
		if key == "filename" && (strings.ContainsAny(v, `/\`) || strings.Contains(v, "..")) {
			return errors.New("media file names can't contain folders")
		}
		if remoteContent.MatchString(v) {
			return errors.New("the dashboard doesn't let the assistant save links, scripts or remote content in Anki")
		}
	}
	return nil
}

// answer replies the way AnkiConnect does: {"result": …, "error": …}.
func answer(w http.ResponseWriter, result json.RawMessage, err error) {
	reply := map[string]any{"result": result, "error": nil}
	if err != nil {
		reply["error"] = err.Error()
	}
	data, _ := json.Marshal(reply)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

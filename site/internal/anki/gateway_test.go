package anki

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayRefusesWhatReachesOutsideTheCollection(t *testing.T) {
	refused := map[string]string{
		"unknown action":      `{"action": "multi", "params": {"actions": []}}`,
		"media from a URL":    `{"action": "storeMediaFile", "params": {"filename": "a.png", "url": "https://x.example/a.png"}}`,
		"picture on a note":   `{"action": "addNote", "params": {"note": {"deckName": "D", "fields": {"Front": "a"}, "picture": [{"url": "https://x.example"}]}}}`,
		"remote image":        `{"action": "addNote", "params": {"note": {"fields": {"Front": "<img src=\"https://x.example/?q=secret\">"}}}}`,
		"protocol-relative":   `{"action": "updateNoteFields", "params": {"note": {"id": 1, "fields": {"Back": "<img src=//x.example/a>"}}}}`,
		"css url":             `{"action": "updateModelStyling", "params": {"model": {"name": "Basic", "css": ".card { background: url(x.png) }"}}}`,
		"script in template":  `{"action": "updateModelTemplates", "params": {"model": {"name": "Basic", "templates": {"Card 1": {"Front": "<script>fetch('x')</script>"}}}}}`,
		"event handler":       `{"action": "addNote", "params": {"note": {"fields": {"Front": "<b onmouseover=alert(1)>x</b>"}}}}`,
		"media path outside":  `{"action": "retrieveMediaFile", "params": {"filename": "../../.ssh/id_ed25519"}}`,
		"media from a file":   `{"action": "addNote", "params": {"note": {"fields": {"Front": "a"}, "audio": [{"path": "/etc/passwd", "filename": "a.mp3"}]}}}`,
		"javascript link":     `{"action": "addNote", "params": {"note": {"fields": {"Front": "<a href='javascript:x'>a</a>"}}}}`,
		"link with a scheme":  `{"action": "addNote", "params": {"note": {"fields": {"Front": "see ftp://x.example"}}}}`,
		"import in a style":   `{"action": "updateModelStyling", "params": {"model": {"css": "@import 'x.css';"}}}`,
		"iframe in a field":   `{"action": "addNote", "params": {"note": {"fields": {"Front": "<iframe src=a.html>"}}}}`,
		"remote in a batch":   `{"action": "addNotes", "params": {"notes": [{"fields": {"Front": "ok"}}, {"fields": {"Front": "<img src='http://x'>"}}]}}`,
		"remote image inline": `{"action": "addNote", "params": {"note": {"fields": {"Front": "<svg><image href=a /></svg>"}}}}`,
	}
	for name, body := range refused {
		var request struct {
			Action string          `json:"action"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal([]byte(body), &request)
		if err := checkRequest(request.Action, request.Params); err == nil {
			t.Errorf("%s: let through", name)
		}
	}

	allowed := map[string]string{
		"plain note":     `{"action": "addNote", "params": {"note": {"deckName": "Networking", "modelName": "Basic", "fields": {"Front": "What port is SSH?", "Back": "22<br>(TCP)"}}}}`,
		"local image":    `{"action": "updateNoteFields", "params": {"note": {"id": 1, "fields": {"Back": "<img src=\"subnet.png\">"}}}}`,
		"search":         `{"action": "findNotes", "params": {"query": "deck:Networking tag:ports"}}`,
		"no params":      `{"action": "deckNames"}`,
		"media by name":  `{"action": "retrieveMediaFile", "params": {"filename": "subnet.png"}}`,
		"plain template": `{"action": "updateModelTemplates", "params": {"model": {"name": "Basic", "templates": {"Card 1": {"Front": "{{Front}}", "Back": "{{FrontSide}}<hr id=answer>{{Back}}"}}}}}`,
	}
	for name, body := range allowed {
		var request struct {
			Action string          `json:"action"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal([]byte(body), &request)
		if err := checkRequest(request.Action, request.Params); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

func TestGatewayNeedsItsKeyAndForwards(t *testing.T) {
	var forwarded map[string]any
	anki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&forwarded)
		io.WriteString(w, `{"result": ["Default"], "error": null}`)
	}))
	defer anki.Close()
	gateway, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	key := gateway.Register("me", Client{URL: anki.URL, APIKey: "anki-key", http: anki.Client()})
	if again := gateway.Register("me", Client{URL: anki.URL, APIKey: "anki-key", http: anki.Client()}); again != key {
		t.Fatal("registering again should keep the key")
	}
	other := gateway.Register("someone else", Client{})

	post := func(body string) map[string]any {
		resp, err := http.Post(gateway.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var reply map[string]any
		json.NewDecoder(resp.Body).Decode(&reply)
		return reply
	}

	if reply := post(`{"action": "deckNames", "version": 6, "key": "wrong"}`); reply["error"] == nil || forwarded != nil {
		t.Fatalf("wrong key: got %v, forwarded %v", reply, forwarded)
	}
	if reply := post(`{"action": "deckNames", "version": 6, "key": "` + other + `"}`); reply["error"] == nil || forwarded != nil {
		t.Fatalf("another user's key must not reach this Anki: got %v, forwarded %v", reply, forwarded)
	}
	reply := post(`{"action": "deckNames", "version": 6, "key": "` + key + `"}`)
	if reply["error"] != nil || forwarded["action"] != "deckNames" || forwarded["key"] != "anki-key" {
		t.Fatalf("right key: got %v, forwarded %v", reply, forwarded)
	}
	gateway.Unregister("me")
	forwarded = nil
	if reply := post(`{"action": "deckNames", "version": 6, "key": "` + key + `"}`); reply["error"] == nil || forwarded != nil {
		t.Fatal("an unregistered key must stop working")
	}
}

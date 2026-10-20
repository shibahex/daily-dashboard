package anki

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAnkiConnect answers "version" only with the key ("" = it needs none).
func fakeAnkiConnect(t *testing.T, key string) string {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		if key != "" && request["key"] != key {
			io.WriteString(w, `{"result": null, "error": "valid api key must be provided"}`)
			return
		}
		io.WriteString(w, `{"result": 6, "error": null}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestCheckConnection(t *testing.T) {
	keyed := fakeAnkiConnect(t, "my-key")
	open := fakeAnkiConnect(t, "")

	if cleaned, err := CheckConnection(keyed+"/some/path", "my-key", true); err != nil || cleaned != keyed {
		t.Fatalf("a keyed AnkiConnect should be accepted: %q %v", cleaned, err)
	}
	if _, err := CheckConnection(keyed, "wrong", true); err == nil {
		t.Error("a wrong key should be refused")
	}
	if _, err := CheckConnection(keyed, "", true); err == nil {
		t.Error("an empty key should be refused")
	}
	if _, err := CheckConnection(open, "anything", true); err == nil {
		t.Error("an AnkiConnect that answers without a key should be refused")
	}
	if _, err := CheckConnection(keyed, "my-key", false); err == nil {
		t.Error("only the host may use an address on the server itself")
	}
	for _, bad := range []string{"", "ftp://x", "192.168.1.20:8765", "http://"} {
		if _, err := CheckConnection(bad, "my-key", true); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if _, err := CheckConnection("http://127.0.0.1:1", "my-key", true); err == nil {
		t.Error("an address nothing answers on should be refused")
	}
}

func TestNotSetUp(t *testing.T) {
	if _, err := NewClient("", "").Call("version", nil); err != ErrNotSetUp {
		t.Fatalf("got %v", err)
	}
	if status := NewClient("", "").Status(); !status.Remote || !status.NotSetUp {
		t.Fatalf("a user without an address should be told to set it up: %+v", status)
	}
}

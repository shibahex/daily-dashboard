package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dashboard/internal/auth"
)

func TestModelRelay(t *testing.T) {
	// A model server with a certificate nobody trusts.
	var gotKey string
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data": [{"id": "made-up-model"}]}`)
	}))
	defer model.Close()

	accounts, err := auth.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery"
	session, err := accounts.Setup(accounts.SetupCode(), "me", password, password)
	if err != nil {
		t.Fatal(err)
	}
	ui := &UI{Auth: accounts}
	handler := accounts.Guard(http.HandlerFunc(ui.modelRelay))
	ask := func(method, to string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/assistant/model-relay?to="+url.QueryEscape(to), strings.NewReader("{}"))
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})
		r.Header.Set("X-Model-Key", "model-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if err := accounts.SetModelServers([]string{model.URL}); err != nil {
		t.Fatal(err)
	}
	if w := ask("GET", model.URL+"/v1/models"); w.Code != http.StatusForbidden {
		t.Fatalf("with certificate checks on, the server mustn't relay: %d", w.Code)
	}

	accounts.SetSkipModelCerts(true)
	w := ask("GET", model.URL+"/v1/models")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "made-up-model") || gotKey != "Bearer model-key" {
		t.Fatalf("relay: %d %s (key %q)", w.Code, w.Body, gotKey)
	}
	for _, c := range []struct{ method, to string }{
		{"GET", "https://elsewhere.example.lan/v1/models"},
		{"GET", model.URL + "/v1/admin"},
		{"GET", model.URL + "/v1/chat/completions"},
		{"POST", model.URL + "/v1/models"},
		{"GET", model.URL + "/v1/../secret/models"},
		{"GET", model.URL + "/v1/models?x=1"},
	} {
		if w := ask(c.method, c.to); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want refused", c.method, c.to, w.Code)
		}
	}
}

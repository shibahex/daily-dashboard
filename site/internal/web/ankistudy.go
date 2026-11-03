package web

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"dashboard/internal/anki"
	"dashboard/internal/problem"
)

// Studying in the browser, for a user whose Anki runs on the server (anki.Client.Server): the
// next card's front, then its back and the four answers, scheduled by Anki itself.
//
// A card's HTML comes from the user's decks, shared ones included, so it's untrusted: it's
// served on its own URL (ankiReviewCard) with scripts, frames and links to elsewhere removed, its
// images and sounds put inline, a policy that allows nothing else, and a sandbox, and shown in a
// sandboxed frame. It never runs in the dashboard's pages.

type ankiReviewView struct {
	Deck    string
	Study   anki.Study
	Side    string // "q" (the front) or "a" (the back, with the answers)
	Problem string
}

// CardURL is the frame's address for this card's side.
func (v ankiReviewView) CardURL() string {
	if v.Study.Card == nil {
		return ""
	}
	return "/anki/review/card?" + url.Values{"deck": {v.Deck}, "id": {strconv.FormatInt(v.Study.Card.ID, 10)},
		"reps": {strconv.Itoa(v.Study.Card.Reps)}, "side": {v.Side}}.Encode()
}

func (ui *UI) ankiReview(r *http.Request, deck, side string) ankiReviewView {
	view := ankiReviewView{Deck: deck, Side: "q"}
	client := ui.ws(r).Anki()
	if !client.Server {
		view.Problem = "Studying here needs your Anki on the server: your own Anki is connected instead."
		return view
	}
	study, err := client.StudyNext(deck)
	if err != nil {
		view.Problem = problem.Public(err)
		return view
	}
	view.Study, view.Deck = study, study.Deck
	// The back only for the card that was showing: another one came up in between otherwise.
	if side == "a" && study.Card != nil && formInt(r, "id") == study.Card.ID && int(formInt(r, "reps")) == study.Card.Reps {
		view.Side = "a"
	}
	return view
}

func (ui *UI) ankiReviewPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "anki", "Study", "anki-review", ui.ankiReview(r, r.FormValue("deck"), "q"))
}

// ankiReviewPanel shows the card's back (side=a) or the next card's front.
func (ui *UI) ankiReviewPanel(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "anki-review-panel", ui.ankiReview(r, r.FormValue("deck"), r.FormValue("side")))
}

func (ui *UI) ankiReviewAnswer(w http.ResponseWriter, r *http.Request) {
	ease, _ := strconv.Atoi(r.FormValue("ease"))
	if err := ui.ws(r).Anki().Answer(formInt(r, "id"), int(formInt(r, "reps")), ease); err != nil {
		fail(w, err)
		return
	}
	announce(w, nil, ankiChanged)
	ui.render(w, "anki-review-panel", ui.ankiReview(r, r.FormValue("deck"), "q"))
}

func (ui *UI) ankiReviewUndo(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Anki().Undo(); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Undone"), ankiChanged)
	ui.render(w, "anki-review-panel", ui.ankiReview(r, r.FormValue("deck"), "q"))
}

// cardPolicy lets a card's page show only what's inside it: no scripts (sandbox), nothing
// loaded from anywhere, framed only by the dashboard.
const cardPolicy = "default-src 'none'; img-src data:; media-src data:; font-src data:; style-src 'unsafe-inline'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox"

// ankiReviewCard is one side of the card being studied, as its own page for the frame.
func (ui *UI) ankiReviewCard(w http.ResponseWriter, r *http.Request) {
	client := ui.ws(r).Anki()
	if !client.Server {
		http.NotFound(w, r)
		return
	}
	study, err := client.StudyNext(r.FormValue("deck"))
	card := study.Card
	if err != nil || card == nil || card.ID != formInt(r, "id") || card.Reps != int(formInt(r, "reps")) {
		http.Error(w, "This card isn't the one being studied any more.", http.StatusNotFound)
		return
	}
	content := card.Question
	if r.FormValue("side") == "a" {
		content = card.Answer
	}
	page := cardDocument(content, card.CSS, card.Ord, client.MediaFile)
	header := w.Header()
	header.Set("Content-Security-Policy", cardPolicy)
	header.Del("X-Frame-Options")
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.Write(page)
}

// Media put inline: at most this much per file and per card side.
const (
	maxInlineFile = 8 << 20
	maxInlineCard = 24 << 20
)

// mediaTypes are the media a card may show, by extension.
var mediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".svg": "image/svg+xml", ".avif": "image/avif", ".bmp": "image/bmp",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac", ".webm": "audio/webm",
}

// droppedElements are removed with everything inside them.
var droppedElements = map[atom.Atom]bool{
	atom.Script: true, atom.Iframe: true, atom.Frame: true, atom.Frameset: true, atom.Object: true,
	atom.Embed: true, atom.Applet: true, atom.Link: true, atom.Meta: true, atom.Base: true,
	atom.Form: true, atom.Noscript: true, atom.Template: true,
}

// cardDocument is a card side as a page of its own: like Anki's own reviewer (the note type's
// CSS, the body classed "card cardN"), with what can't be shown safely taken out and the
// collection's images and sounds put inline. media fetches a file from the collection.
func cardDocument(content, css string, ord int, media func(string) ([]byte, error)) []byte {
	body := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
		Attr: []html.Attribute{{Key: "id", Val: "qa"}}}
	nodes, err := html.ParseFragment(strings.NewReader(content), body)
	if err != nil {
		nodes = []*html.Node{{Type: html.TextNode, Data: content}}
	}
	budget := maxInlineCard
	for _, n := range nodes {
		body.AppendChild(n)
	}
	cleanNode(body, media, &budget)

	var out bytes.Buffer
	out.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>`)
	out.WriteString(`html{-webkit-text-size-adjust:100%}body{margin:0;padding:20px;overflow-wrap:break-word}img{max-width:100%;height:auto}audio{display:block;margin:8px auto}` + "\n")
	out.WriteString(strings.ReplaceAll(css, "</", `<\/`))
	out.WriteString(`</style></head><body class="card card` + strconv.Itoa(ord+1) + `">`)
	html.Render(&out, body)
	out.WriteString(`</body></html>`)
	return out.Bytes()
}

// cleanNode removes from n what could run or reach elsewhere, and inlines the collection's media.
func cleanNode(n *html.Node, media func(string) ([]byte, error), budget *int) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && droppedElements[c.DataAtom] {
			n.RemoveChild(c)
		} else if c.Type == html.CommentNode {
			n.RemoveChild(c)
		} else if c.Type == html.ElementNode {
			cleanAttributes(c, media, budget)
			cleanNode(c, media, budget)
		}
		c = next
	}
}

func cleanAttributes(n *html.Node, media func(string) ([]byte, error), budget *int) {
	var kept []html.Attribute
	sound := ""
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		switch {
		case strings.HasPrefix(key, "on"), key == "srcset", key == "formaction", key == "action",
			key == "xlink:href", key == "background", key == "poster", key == "ping":
			continue
		case key == "data-sound":
			sound = a.Val
			continue
		case key == "href":
			continue // a card's links lead nowhere from here
		case key == "src":
			if inline := inlineMedia(a.Val, media, budget); inline != "" {
				kept = append(kept, html.Attribute{Key: "src", Val: inline})
			}
			continue
		case key == "style" && strings.Contains(strings.ToLower(a.Val), "url("):
			continue // only data: would load anyway; keep the page simple
		}
		kept = append(kept, a)
	}
	if n.DataAtom == atom.Audio && sound != "" {
		if inline := inlineMedia(sound, media, budget); inline != "" {
			kept = append(kept, html.Attribute{Key: "src", Val: inline})
		}
	}
	n.Attr = kept
}

// inlineMedia is a collection media file (a plain file name) as a data: URL, or "" when it isn't
// one, isn't a type a card may show, is missing, or is too big.
func inlineMedia(name string, media func(string) ([]byte, error), budget *int) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\:?#`) || strings.Contains(name, "..") {
		return ""
	}
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	kind, ok := mediaTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return ""
	}
	data, err := media(name)
	if err != nil || len(data) == 0 || len(data) > maxInlineFile || len(data) > *budget {
		return ""
	}
	*budget -= len(data)
	return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func formInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.FormValue(key), 10, 64)
	return n
}

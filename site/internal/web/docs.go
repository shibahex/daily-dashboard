package web

import (
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"dashboard/internal/store"
)

// The Docs tab: the user's notes, which are sealed (only their browser can read them: notes.js
// shows and edits them), and the app's guides, which the server shows.

var docTitles = map[string]string{"README.md": "Getting started", "dashboard.md": "Dashboard guide", "noctalia-plugin.md": "Noctalia plugin"}

type docLink struct {
	Name, Title string
	Open        bool
	FirstGuide  bool // the guides start here
}

type docsView struct {
	Docs      []docLink
	Open      string // the open guide's file name ("" = a note, which the browser shows)
	Title     string
	Content   template.HTML
	HasGuides bool
}

func (ui *UI) docsPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "docs", "Docs", "docs", ui.docsView(r))
}

// docsView lists the guides (and shows one when it's asked for); the browser fills in the notes.
func (ui *UI) docsView(r *http.Request) docsView {
	view := docsView{}
	open := r.FormValue("doc")
	for _, name := range store.GuideNames {
		if _, found := ui.ws(r).Store.Doc(name); found {
			view.Docs = append(view.Docs, docLink{Name: name, Title: docTitle(name), Open: name == open, FirstGuide: !view.HasGuides})
			view.HasGuides = true
		}
	}
	if content, found := ui.ws(r).Store.Doc(open); found && slices.Contains(store.GuideNames, open) {
		view.Open, view.Title = open, docTitle(open)
		view.Content = linkDocs(renderMarkdown(string(content)))
	}
	return view
}

// docTitle is "Nutrition plan" for nutrition-plan.md.
func docTitle(name string) string {
	if title, ok := docTitles[name]; ok {
		return title
	}
	title := strings.ReplaceAll(strings.TrimSuffix(name, ".md"), "-", " ")
	return capitalize(title)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

var docHref = regexp.MustCompile(`href="([\w.-]+\.md)"`)

// linkDocs points links to other docs ("dashboard.md") at the Docs tab.
func linkDocs(html template.HTML) template.HTML {
	return template.HTML(docHref.ReplaceAllStringFunc(string(html), func(href string) string {
		name := docHref.FindStringSubmatch(href)[1]
		return `href="/docs?doc=` + url.QueryEscape(name) + `"`
	}))
}

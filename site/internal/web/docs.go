package web

import (
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dashboard/internal/store"
)

// The Docs tab shows the Markdown files in docs/: your notes first, the app's guides last.

var (
	usualDocs = []string{"overview.md", "background.md", "nutrition-plan.md", "workout-plan.md", "food-history.md"}
	docTitles = map[string]string{"README.md": "Getting started", "dashboard.md": "Dashboard guide", "noctalia-plugin.md": "Noctalia plugin"}
)

const docCookie = "doc"

type docLink struct {
	Name, Title string
	Open        bool
}

type docsView struct {
	Docs    []docLink
	Content template.HTML
}

func (ui *UI) docsPage(w http.ResponseWriter, r *http.Request) {
	names := ui.ws(r).Store.DocNames()
	sort.SliceStable(names, func(i, j int) bool { return docRank(names[i]) < docRank(names[j]) })

	open := r.FormValue("doc")
	if open == "" {
		if cookie, err := r.Cookie(docCookie); err == nil {
			open = cookie.Value
		}
	}
	if !slices.Contains(names, open) && len(names) > 0 {
		open = names[0]
	}
	http.SetCookie(w, &http.Cookie{Name: docCookie, Value: open, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})

	view := docsView{}
	for _, name := range names {
		view.Docs = append(view.Docs, docLink{Name: name, Title: docTitle(name), Open: name == open})
	}
	if content, found := ui.ws(r).Store.Doc(open); found {
		view.Content = linkDocs(renderMarkdown(string(content)))
	}
	ui.renderPage(w, "docs", "Docs", "docs", view)
}

// docRank puts the usual notes first (in order), then other notes, then the guides.
func docRank(name string) int {
	if i := slices.Index(store.GuideNames, name); i >= 0 {
		return 100 + i
	}
	if i := slices.Index(usualDocs, name); i >= 0 {
		return i
	}
	return 50
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

// linkDocs points links to other docs ("workout-plan.md") at the Docs tab.
func linkDocs(html template.HTML) template.HTML {
	return template.HTML(docHref.ReplaceAllStringFunc(string(html), func(href string) string {
		name := docHref.FindStringSubmatch(href)[1]
		return `href="/docs?doc=` + url.QueryEscape(name) + `"`
	}))
}

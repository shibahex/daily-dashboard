package web

import (
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dashboard/internal/problem"
	"dashboard/internal/store"
)

// The Docs tab shows the Markdown files in docs/: your notes first, the app's guides last.
// Notes can be edited (Edit), added, renamed, deleted and moved up or down; guides are read-only.
// Every change answers with the whole tab (#docs) and puts the open note in the address.

var (
	usualDocs = []string{"overview.md", "background.md", "nutrition-plan.md", "workout-plan.md", "food-history.md"}
	docTitles = map[string]string{"README.md": "Getting started", "dashboard.md": "Dashboard guide", "noctalia-plugin.md": "Noctalia plugin"}
)

const docCookie = "doc"

type docLink struct {
	Name, Title string
	Open        bool
	Note        bool // one of the user's notes (editable), not a guide
	FirstGuide  bool // the guides start here
	First, Last bool // among the notes, for the move buttons
}

type docsView struct {
	Docs      []docLink
	Open      string // the open doc's file name
	Title     string // its title
	Note      bool   // it's an editable note
	Editing   bool
	Source    string // its Markdown, while editing
	Content   template.HTML
	HasNotes  bool
	HasGuides bool
	Sealed    bool // the notes are sealed: the browser shows them (see sealed.go)
}

func (ui *UI) docsPage(w http.ResponseWriter, r *http.Request) {
	if sealedNotes(ui.user(r)) {
		ui.renderPage(r, w, "docs", "Docs", "docs", ui.sealedDocsView(r))
		return
	}
	view, err := ui.docsView(r, r.FormValue("doc"), false)
	if err != nil {
		fail(w, err)
		return
	}
	ui.rememberDoc(w, view.Open)
	ui.renderPage(r, w, "docs", "Docs", "docs", view)
}

// docsView is the tab with open showing ("" = the last one opened, or the first).
func (ui *UI) docsView(r *http.Request, open string, editing bool) (docsView, error) {
	names, err := ui.orderedDocs(r)
	if err != nil {
		return docsView{}, err
	}
	if open == "" {
		if cookie, err := r.Cookie(docCookie); err == nil {
			open = cookie.Value
		}
	}
	if !slices.Contains(names, open) && len(names) > 0 {
		open = names[0]
	}
	st := ui.ws(r).Store
	view := docsView{Open: open, Title: docTitle(open), Note: st.IsNote(open)}
	view.Editing = editing && view.Note
	notes := 0
	for _, name := range names {
		if !slices.Contains(store.GuideNames, name) {
			notes++
		}
	}
	view.HasNotes, view.HasGuides = notes > 0, notes < len(names)
	for i, name := range names {
		link := docLink{Name: name, Title: docTitle(name), Open: name == open, Note: i < notes}
		link.FirstGuide = i == notes
		link.First, link.Last = i == 0, i == notes-1
		view.Docs = append(view.Docs, link)
	}
	if content, found := st.Doc(open); found {
		if view.Editing {
			view.Source = string(content)
		} else {
			view.Content = linkDocs(renderMarkdown(string(content)))
		}
	}
	return view, nil
}

// orderedDocs is the notes in the user's order (any not placed yet by the usual order), then
// the guides.
func (ui *UI) orderedDocs(r *http.Request) ([]string, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return nil, err
	}
	names := ui.ws(r).Store.DocNames()
	rank := func(name string) int {
		if i := slices.Index(plan.DocOrder, name); i >= 0 && !slices.Contains(store.GuideNames, name) {
			return i - 1000 // placed by the user: before the rest
		}
		return docRank(name)
	}
	sort.SliceStable(names, func(i, j int) bool { return rank(names[i]) < rank(names[j]) })
	return names, nil
}

func (ui *UI) rememberDoc(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: docCookie, Value: name, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
}

// renderDocs answers a change: the tab with name open, and the address pointing at it.
func (ui *UI) renderDocs(w http.ResponseWriter, r *http.Request, name string, editing bool) {
	view, err := ui.docsView(r, name, editing)
	if err != nil {
		fail(w, err)
		return
	}
	ui.rememberDoc(w, view.Open)
	w.Header().Set("HX-Push-Url", "/docs?doc="+url.QueryEscape(view.Open))
	ui.render(w, "docs", view)
}

// docsCard shows a doc (?doc=), or its editor (?edit=1).
func (ui *UI) docsCard(w http.ResponseWriter, r *http.Request) {
	ui.renderDocs(w, r, r.FormValue("doc"), isEditing(r))
}

// newNote adds a note from a title and opens it in the editor.
func (ui *UI) newNote(w http.ResponseWriter, r *http.Request) {
	name, err := ui.ws(r).Store.CreateNote(formText(r, "title"))
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Note added"))
	ui.renderDocs(w, r, name, true)
}

// saveNote saves the editor: the text, and the title, which renames the file.
func (ui *UI) saveNote(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	newName := name
	if title := formText(r, "title"); title != "" && title != docTitle(name) {
		var err error
		if newName, err = store.NoteFileName(title); err != nil {
			fail(w, err)
			return
		}
	}
	// Browsers send textarea text with \r\n line ends; the files use \n.
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	saved, err := ui.ws(r).Store.SaveNote(name, newName, []byte(content))
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Note saved"))
	ui.renderDocs(w, r, saved, false)
}

func (ui *UI) deleteNote(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if err := ui.ws(r).Store.DeleteNote(name); err != nil {
		fail(w, err)
		return
	}
	announce(w, say(docTitle(name)+" deleted"))
	ui.renderDocs(w, r, "", false)
}

// moveNote moves a note one place up or down (dir=up|down) among the notes.
func (ui *UI) moveNote(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if !ui.ws(r).Store.IsNote(name) {
		fail(w, problem.NotFoundf("that note doesn't exist"))
		return
	}
	names, err := ui.orderedDocs(r)
	if err != nil {
		fail(w, err)
		return
	}
	notes := slices.DeleteFunc(names, func(n string) bool { return slices.Contains(store.GuideNames, n) })
	i := slices.Index(notes, name)
	j := i - 1
	if r.FormValue("dir") == "down" {
		j = i + 1
	}
	if j >= 0 && j < len(notes) {
		notes[i], notes[j] = notes[j], notes[i]
		if err := ui.ws(r).Store.SaveNoteOrder(notes); err != nil {
			fail(w, err)
			return
		}
	}
	ui.renderDocs(w, r, name, false)
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

// sealedDocsView is the Docs tab for sealed notes: the server lists only the guides (and shows
// one when it's asked for); the browser fills in the notes, which only it can read.
func (ui *UI) sealedDocsView(r *http.Request) docsView {
	view := docsView{Sealed: true}
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

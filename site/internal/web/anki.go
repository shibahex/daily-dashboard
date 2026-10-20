package web

import (
	"html"
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/problem"
)

// The Anki tab talks to Anki through AnkiConnect, so Anki has to be open. Reviewing happens in
// Anki itself: Study opens the deck there.

type ankiPageView struct {
	Status anki.Status
	Body   ankiBodyView
}

// ankiBodyView is the tab below its header: the setup steps, or the decks, add form and browser.
type ankiBodyView struct {
	Status  anki.Status
	Problem string
	Decks   ankiDecksView
	AddForm ankiAddForm
	Browse  ankiNotesView
}

func (ui *UI) ankiPage(w http.ResponseWriter, r *http.Request) {
	body := ui.ankiBody(r)
	ui.renderPage(w, "anki", "Anki", "anki", ankiPageView{Status: body.Status, Body: body})
}

// ankiBodyPart answers the setup panel's polling: once Anki opens, the whole tab appears.
func (ui *UI) ankiBodyPart(w http.ResponseWriter, r *http.Request) {
	body := ui.ankiBody(r)
	if body.Status.Connected {
		w.Header().Set("HX-Refresh", "true")
	}
	ui.render(w, "anki-body", body)
}

func (ui *UI) ankiBody(r *http.Request) ankiBodyView {
	body := ankiBodyView{Status: ui.ws(r).Anki.Status()}
	if !body.Status.Connected {
		return body
	}
	overview, err := ui.ws(r).Anki.Overview()
	if err != nil {
		body.Problem = err.Error()
		return body
	}
	body.Decks = decksView(overview)
	body.AddForm = ui.ankiAddFormFor(r, overview, rememberedChoice(r, deckCookie), rememberedChoice(r, modelCookie))
	body.Browse = ui.ankiNotesFor(r, defaultAnkiSearch)
	return body
}

// ---------- decks ----------

type reviewBar struct {
	Day     time.Time
	Reviews int
	Height  float64 // percent of the busiest day
}

type ankiDecksView struct {
	Overview anki.Overview
	DueToday int
	LastDays []reviewBar
}

func decksView(overview anki.Overview) ankiDecksView {
	view := ankiDecksView{Overview: overview, LastDays: reviewBars(overview.Reviews, 30)}
	for _, deck := range overview.TopLevelDecks() {
		view.DueToday += deck.Due()
	}
	return view
}

// reviewBars are the last `days` days of reviews, for a small bar chart.
func reviewBars(reviews anki.ReviewsByDay, days int) []reviewBar {
	today := startOfDay(time.Now())
	busiest := 1
	var bars []reviewBar
	for i := days - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i)
		bars = append(bars, reviewBar{Day: day, Reviews: reviews.On(day)})
		busiest = max(busiest, reviews.On(day))
	}
	for i := range bars {
		bars[i].Height = max(4, float64(bars[i].Reviews)/float64(busiest)*100)
	}
	return bars
}

func (ui *UI) studyDeck(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Anki.OpenDeck(r.FormValue("deck")); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Opened in Anki"))
	nothing(w)
}

func (ui *UI) openAnki(w http.ResponseWriter, r *http.Request) {
	if anki.AppRunning() {
		announce(w, say("Anki is already open"))
		nothing(w)
		return
	}
	if err := anki.OpenApp(); err != nil {
		fail(w, problem.Failedf(err.Error()))
		return
	}
	announce(w, say("Opening Anki…"))
	nothing(w)
}

func (ui *UI) syncAnki(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Anki.Sync(); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Anki synced"), ankiChanged)
	nothing(w)
}

func (ui *UI) ankiDecks(w http.ResponseWriter, r *http.Request) {
	overview, err := ui.ws(r).Anki.Overview()
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "anki-decks", decksView(overview))
}

// ---------- adding cards ----------

const (
	deckCookie  = "anki-deck"
	modelCookie = "anki-model"
	newDeck     = "__new"
)

type ankiAddForm struct {
	Decks  []string
	Deck   string
	Models []string
	Model  string
	Fields []string
}

func rememberedChoice(r *http.Request, cookie string) string {
	if value := r.FormValue(strings.TrimPrefix(cookie, "anki-")); value != "" {
		return value
	}
	if saved, err := r.Cookie(cookie); err == nil {
		return saved.Value
	}
	return ""
}

func remember(w http.ResponseWriter, cookie, value string) {
	http.SetCookie(w, &http.Cookie{Name: cookie, Value: value, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
}

// ankiAddFormFor fills the add form: the chosen deck and note type, or Basic.
func (ui *UI) ankiAddFormFor(r *http.Request, overview anki.Overview, deck, model string) ankiAddForm {
	form := ankiAddForm{Models: overview.Models, Deck: deck, Model: model}
	for _, d := range overview.Decks {
		form.Decks = append(form.Decks, d.Name)
	}
	if !slices.Contains(form.Models, form.Model) {
		form.Model = ""
		if slices.Contains(form.Models, "Basic") {
			form.Model = "Basic"
		} else if len(form.Models) > 0 {
			form.Model = form.Models[0]
		}
	}
	if form.Model != "" {
		form.Fields, _ = ui.ws(r).Anki.FieldNames(form.Model)
	}
	return form
}

// ankiAddFormPart redraws the add form when the note type changes (its fields differ).
func (ui *UI) ankiAddFormPart(w http.ResponseWriter, r *http.Request) {
	overview, err := ui.ws(r).Anki.Overview()
	if err != nil {
		fail(w, err)
		return
	}
	remember(w, modelCookie, r.FormValue("model"))
	ui.render(w, "anki-add", ui.ankiAddFormFor(r, overview, r.FormValue("deck"), r.FormValue("model")))
}

func (ui *UI) addAnkiNote(w http.ResponseWriter, r *http.Request) {
	deck, model := formText(r, "deck"), r.FormValue("model")
	if deck == newDeck {
		deck = formText(r, "new_deck")
		if err := ui.ws(r).Anki.CreateDeck(deck); err != nil {
			fail(w, err)
			return
		}
	}
	names, err := ui.ws(r).Anki.FieldNames(model)
	if err != nil {
		fail(w, err)
		return
	}
	fields := fieldsFromForm(r, names)
	if len(names) == 0 || fields[names[0]] == "" {
		fail(w, problem.Invalidf("Fill in the first field"))
		return
	}
	if err := ui.ws(r).Anki.AddNote(deck, model, fields, strings.Fields(r.FormValue("tags"))); err != nil {
		fail(w, err)
		return
	}
	remember(w, deckCookie, deck)
	remember(w, modelCookie, model)
	overview, err := ui.ws(r).Anki.Overview()
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Added to "+deck), ankiChanged)
	ui.render(w, "anki-add", ui.ankiAddFormFor(r, overview, deck, model))
}

// fieldsFromForm reads the "field" boxes, in the note type's order. Line breaks become <br>.
func fieldsFromForm(r *http.Request, names []string) map[string]string {
	r.ParseForm()
	values := r.Form["field"]
	fields := map[string]string{}
	for i, name := range names {
		if i < len(values) {
			fields[name] = textToField(strings.TrimSpace(values[i]))
		}
	}
	return fields
}

// ---------- browsing notes ----------

const (
	defaultAnkiSearch = "added:7"
	notesShown        = 60
)

// ankiNote is a note as the browser shows it.
type ankiNote struct {
	anki.Note
}

// Question and Answer are the first two fields as plain text.
func (n ankiNote) Question() string { return n.fieldText(0, 0) }
func (n ankiNote) Answer() string   { return n.fieldText(1, 220) }

func (n ankiNote) fieldText(i, limit int) string {
	if i >= len(n.Fields) {
		return ""
	}
	plain := fieldToPlainText(n.Fields[i].Value)
	if limit > 0 && len([]rune(plain)) > limit {
		plain = string([]rune(plain)[:limit])
	}
	return plain
}

func (n ankiNote) EditableFields() []anki.Field {
	fields := make([]anki.Field, len(n.Fields))
	for i, field := range n.Fields {
		fields[i] = anki.Field{Name: field.Name, Value: fieldToText(field.Value)}
	}
	return fields
}

func (n ankiNote) TagText() string { return strings.Join(n.Tags, " ") }

type ankiNotesView struct {
	Query   string
	Notes   []ankiNote
	Total   int
	Problem string
}

func (v ankiNotesView) Count() string {
	switch {
	case v.Total == 0:
		return ""
	case v.Total > notesShown:
		return countOf(v.Total, "note", "notes") + " · newest " + strconv.Itoa(notesShown) + " shown"
	}
	return countOf(v.Total, "note", "notes")
}

func (ui *UI) ankiNotesFor(r *http.Request, query string) ankiNotesView {
	view := ankiNotesView{Query: query}
	search := query
	if strings.TrimSpace(search) == "" {
		search = "deck:*"
	}
	notes, total, err := ui.ws(r).Anki.SearchNotes(search, notesShown)
	if err != nil {
		view.Problem = err.Error()
		return view
	}
	view.Total = total
	for _, note := range notes {
		view.Notes = append(view.Notes, ankiNote{note})
	}
	return view
}

func (ui *UI) ankiNotes(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "anki-notes", ui.ankiNotesFor(r, r.FormValue("q")))
}

func noteID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, problem.Invalidf("bad note id")
	}
	return id, nil
}

func (ui *UI) findNote(r *http.Request) (ankiNote, error) {
	id, err := noteID(r)
	if err != nil {
		return ankiNote{}, err
	}
	notes, _, err := ui.ws(r).Anki.SearchNotes("nid:"+strconv.FormatInt(id, 10), 1)
	if err != nil {
		return ankiNote{}, err
	}
	if len(notes) == 0 {
		return ankiNote{}, problem.NotFoundf("that note is gone")
	}
	return ankiNote{notes[0]}, nil
}

func (ui *UI) ankiNoteEditor(w http.ResponseWriter, r *http.Request) {
	note, err := ui.findNote(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "anki-note-edit", note)
}

func (ui *UI) saveAnkiNote(w http.ResponseWriter, r *http.Request) {
	note, err := ui.findNote(r)
	if err != nil {
		fail(w, err)
		return
	}
	var names []string
	for _, field := range note.Fields {
		names = append(names, field.Name)
	}
	if err := ui.ws(r).Anki.UpdateNote(note.ID, fieldsFromForm(r, names), strings.Fields(r.FormValue("tags"))); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Saved"), ankiChanged)
	nothing(w)
}

func (ui *UI) toggleAnkiSuspended(w http.ResponseWriter, r *http.Request) {
	note, err := ui.findNote(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := ui.ws(r).Anki.SetSuspended(note.CardIDs, !note.Suspended); err != nil {
		fail(w, err)
		return
	}
	message := "Suspended"
	if note.Suspended {
		message = "Unsuspended"
	}
	announce(w, say(message), ankiChanged)
	nothing(w)
}

func (ui *UI) deleteAnkiNote(w http.ResponseWriter, r *http.Request) {
	id, err := noteID(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := ui.ws(r).Anki.DeleteNote(id); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Deleted"), ankiChanged)
	nothing(w)
}

func (ui *UI) browseAnkiNote(w http.ResponseWriter, r *http.Request) {
	id, err := noteID(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := ui.ws(r).Anki.OpenNoteInBrowser(id); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Opened in Anki"))
	nothing(w)
}

// ---------- field text ----------

var (
	lineBreak = regexp.MustCompile(`(?i)<br\s*/?>`)
	image     = regexp.MustCompile(`(?i)<img[^>]*>`)
	anyTag    = regexp.MustCompile(`<[^>]*>`)
)

// fieldToPlainText is a field's HTML as one line of text, images as 🖼.
func fieldToPlainText(field string) string {
	field = image.ReplaceAllString(field, " 🖼 ")
	field = lineBreak.ReplaceAllString(field, " ")
	return strings.TrimSpace(html.UnescapeString(anyTag.ReplaceAllString(field, "")))
}

// fieldToText is a field's HTML for a text box: line breaks as new lines, other HTML kept.
func fieldToText(field string) string { return lineBreak.ReplaceAllString(field, "\n") }

// textToField is a text box's text as field HTML.
func textToField(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "<br>")
}

// ---------- the Today card ----------

type ankiTodayView struct {
	Status        anki.Status
	Due           int
	New, Learn    int
	Review        int
	ReviewedToday int
	Streak        int
	LastDays      []reviewBar
	Busiest       []anki.Deck
}

func (ui *UI) renderAnkiTodayCard(r *http.Request) (template.HTML, error) {
	return ui.renderToHTML("card-anki", ui.ankiTodayView(r))
}

func (ui *UI) ankiTodayView(r *http.Request) ankiTodayView {
	view := ankiTodayView{Status: ui.ws(r).Anki.Status()}
	if !view.Status.Connected {
		return view
	}
	overview, err := ui.ws(r).Anki.Overview()
	if err != nil {
		view.Status.Connected = false
		return view
	}
	top := overview.TopLevelDecks()
	for _, deck := range top {
		view.New += deck.New
		view.Learn += deck.Learn
		view.Review += deck.Review
	}
	view.Due = view.New + view.Learn + view.Review
	view.ReviewedToday = overview.ReviewedToday
	view.LastDays = reviewBars(overview.Reviews, 14)

	day := startOfDay(time.Now())
	if overview.Reviews.On(day) == 0 {
		day = day.AddDate(0, 0, -1) // not studied yet today: count up to yesterday
	}
	for overview.Reviews.On(day) > 0 {
		view.Streak++
		day = day.AddDate(0, 0, -1)
	}

	for _, deck := range top {
		if deck.Due() > 0 {
			view.Busiest = append(view.Busiest, deck)
		}
	}
	sort.SliceStable(view.Busiest, func(i, j int) bool { return view.Busiest[i].Due() > view.Busiest[j].Due() })
	view.Busiest = view.Busiest[:min(3, len(view.Busiest))]
	return view
}

func (ui *UI) ankiTodayCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "card-anki", ui.ankiTodayView(r))
}

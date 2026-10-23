package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/store"
)

// todayCard is one card on the Today tab: where it goes by default, its place in the
// one-column phone layout, and how to render it.
type todayCard struct {
	ID         string
	Column     int
	PhoneOrder int
	render     func(ui *UI, r *http.Request) (template.HTML, error)
}

var todayCards = []todayCard{
	{"card-session", 0, 1, (*UI).renderSessionCard},
	{"card-goals", 0, 5, (*UI).renderGoalsCard},
	{"card-notes", 0, 7, (*UI).renderNotesCard},
	{"card-checklist", 1, 2, (*UI).renderChecklistCard},
	{"card-tasks", 1, 4, (*UI).renderDueTasksCard},
	{"card-strength", 1, 7, (*UI).renderStrengthCard},
	{"card-weight", 2, 3, (*UI).renderWeightCard},
	{"card-time", 2, 4, (*UI).renderTimeCard},
	{"card-week", 2, 6, (*UI).renderWeekCard},
	{"card-anki", 2, 5, (*UI).renderAnkiTodayCard},
}

const columnCount = 3

// placedCard is a card's HTML in its column.
type placedCard struct {
	ID         string
	PhoneOrder int
	HTML       template.HTML
}

type todayView struct {
	Columns     [][]placedCard
	LayoutMoved bool
}

func (ui *UI) todayPage(w http.ResponseWriter, r *http.Request) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		fail(w, err)
		return
	}
	view := todayView{Columns: make([][]placedCard, columnCount), LayoutMoved: plan.Layout != nil}
	for column, ids := range cardColumns(plan.Layout) {
		for _, id := range ids {
			card := findTodayCard(id)
			html, err := card.render(ui, r)
			if err != nil {
				html = template.HTML(`<div class="card" id="` + template.HTMLEscapeString(card.ID) + `"><p class="err">` +
					template.HTMLEscapeString(problem.Public(err)) + `</p></div>`)
			}
			view.Columns[column] = append(view.Columns[column], placedCard{ID: card.ID, PhoneOrder: card.PhoneOrder, HTML: html})
		}
	}
	ui.renderPage(r, w, "today", "Today", "today", view)
}

// cardColumns is the saved layout, or the default one. Cards missing from a saved layout
// (added since it was saved) go to their default column.
func cardColumns(saved [][]string) [][]string {
	columns := make([][]string, columnCount)
	placed := map[string]bool{}
	if len(saved) == columnCount {
		for i, ids := range saved {
			for _, id := range ids {
				if findTodayCard(id).ID != "" && !placed[id] {
					columns[i] = append(columns[i], id)
					placed[id] = true
				}
			}
		}
	}
	for _, card := range todayCards {
		if !placed[card.ID] {
			columns[card.Column] = append(columns[card.Column], card.ID)
		}
	}
	return columns
}

func findTodayCard(id string) todayCard {
	for _, card := range todayCards {
		if card.ID == id {
			return card
		}
	}
	return todayCard{}
}

// saveLayout stores the card order after a drag: layout=[["card-session", ...], [...], [...]].
func (ui *UI) saveLayout(w http.ResponseWriter, r *http.Request) {
	var layout [][]string
	if json.Unmarshal([]byte(r.FormValue("layout")), &layout) != nil || len(layout) != columnCount {
		fail(w, errBadLayout)
		return
	}
	cleaned := make([][]string, columnCount)
	for i, ids := range layout {
		for _, id := range ids {
			if findTodayCard(id).ID != "" && !slices.Contains(cleaned[i], id) {
				cleaned[i] = append(cleaned[i], id)
			}
		}
	}
	if err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) { plan.Layout = cleaned }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Layout saved"))
	nothing(w)
}

func (ui *UI) resetLayout(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) { plan.Layout = nil }); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	nothing(w)
}

// ---------- streak ----------

// streakBadge is the header's "🔥 4-day streak": days in a row with the whole checklist done
// or skipped (today counts once it's complete).
func (ui *UI) streakBadge(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "streak", ui.checklistStreak(r))
}

func (ui *UI) checklistStreak(r *http.Request) int {
	plan, _ := ui.ws(r).Store.LoadPlan()
	days := ui.ws(r).Store.Checklist()
	day := time.Now()
	if dayRatio(plan.Checklist, days, day) < 1 {
		day = day.AddDate(0, 0, -1) // today is still in progress
	}
	streak := 0
	for dayRatio(plan.Checklist, days, day) >= 1 {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	return streak
}

// dayRatio is how much of the checklist was done or skipped that day (0 to 1).
func dayRatio(items []string, days map[string]map[string]any, day time.Time) float64 {
	if len(items) == 0 {
		return 0
	}
	states := days[day.Format("2006-01-02")]
	handled := 0
	for _, item := range items {
		if states[item] != nil {
			handled++
		}
	}
	return float64(handled) / float64(len(items))
}

// ---------- which day ----------
// The Today tab can show an earlier day (/?day=2026-10-19), to see it or fix it after midnight.
// Its cards read the day from the request, or from the page's address, which htmx sends with
// every request as HX-Current-URL, so a card's own requests need not carry it.

// pageDay is the day the page shows: today unless its address asks for an earlier day. A request
// for the page itself reads its own address: links are boosted, so moving to another day is an
// htmx request whose HX-Current-URL is still the day being left.
func pageDay(r *http.Request) time.Time {
	value := r.URL.Query().Get("day")
	if r.URL.Path == "/" {
		return pastDay(value)
	}
	if page, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && r.Header.Get("HX-Request") != "" {
		value = ""
		if page.Path == "/" {
			value = page.Query().Get("day")
		}
	}
	return pastDay(value)
}

// shownDay is the day a card shows: the request's own day (the checklist's dots pick one), or
// the page's.
func shownDay(r *http.Request) time.Time {
	if value := r.FormValue("day"); value != "" {
		return pastDay(value)
	}
	return pageDay(r)
}

// pastDay reads a YYYY-MM-DD day: today when it's missing, not a day, or in the future.
func pastDay(value string) time.Time {
	today := startOfDay(time.Now())
	day, err := time.ParseInLocation(dayLayout, value, time.Local)
	if err != nil || day.After(today) {
		return today
	}
	return day
}

func isToday(day time.Time) bool { return day.Equal(startOfDay(time.Now())) }

// dayName is how a card names its day: "today", "yesterday" or "Mon, Oct 19".
func dayName(day time.Time) string {
	today := startOfDay(time.Now())
	switch {
	case day.Equal(today):
		return "today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "yesterday"
	}
	return day.Format("Mon, Jan 2")
}

// dayNav is the arrows around the date at the top of the Today tab.
type dayNav struct {
	Day                  time.Time
	IsToday, NextIsToday bool
	Prev, Next           string // YYYY-MM-DD
}

func newDayNav(day time.Time) dayNav {
	next := day.AddDate(0, 0, 1)
	return dayNav{Day: day, IsToday: isToday(day), NextIsToday: isToday(next),
		Prev: day.AddDate(0, 0, -1).Format(dayLayout), Next: next.Format(dayLayout)}
}

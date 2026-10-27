package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/store"
)

var errBadLayout = problem.Invalidf("bad layout")

// isEditing reports whether a card was asked for in edit mode (?edit=1).
func isEditing(r *http.Request) bool { return r.URL.Query().Get("edit") == "1" }

// ---------- today's training ----------

type sessionCardView struct {
	Editing  bool
	Heading  string // "Today's training", or the earlier day's
	Key      string
	Session  store.Session
	Planned  bool
	UsedOn   []string // weekdays this session is planned on
	Exercise rowEditor
}

const sessionURL = "/cards/session"

func (ui *UI) renderSessionCard(r *http.Request) (template.HTML, error) {
	view, err := ui.sessionView(r, isEditing(r), nil)
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-session", view)
}

func (ui *UI) sessionView(r *http.Request, editing bool, exercises [][]string) (sessionCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return sessionCardView{}, err
	}
	day := shownDay(r)
	key, session, planned := plan.SessionFor(day.Weekday().String())
	if !planned {
		session = store.Session{Title: "Nothing scheduled"}
	}
	if exercises == nil {
		for _, exercise := range session.Exercises {
			exercises = append(exercises, []string{exercise[0], exercise[1]})
		}
	}
	heading := "Today's training"
	if !isToday(day) {
		heading = "Training · " + dayName(day)
	}
	return sessionCardView{
		Editing: editing, Heading: heading, Key: key, Session: session, Planned: planned, UsedOn: plan.DaysUsing(key),
		Exercise: rowEditor{URL: sessionURL, Rows: exercises, AddLabel: "Add exercise",
			Columns: []rowColumn{{Placeholder: "Exercise"}, {Placeholder: "Sets × reps", Narrow: true}}},
	}, nil
}

func (ui *UI) sessionCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.sessionView(r, isEditing(r), nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-session", view)
}

// saveSession saves the shown day's session (title, note, exercises), or applies a row operation.
func (ui *UI) saveSession(w http.ResponseWriter, r *http.Request) {
	rows := rowsFromForm(r, 2)
	if operation := rowOperation(r); operation != "" {
		view, err := ui.sessionView(r, true, applyRowOperation(rows, operation, 2))
		if err != nil {
			fail(w, err)
			return
		}
		view.Session.Title, view.Session.Note = r.FormValue("title"), r.FormValue("note")
		ui.render(w, "card-session", view)
		return
	}
	weekday := shownDay(r).Weekday().String()
	err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
		key, session, _ := plan.SessionFor(weekday)
		if title := formText(r, "title"); title != "" {
			session.Title = title
		}
		session.Note = formText(r, "note")
		session.Exercises = nil
		for _, row := range cleanRows(rows) {
			session.Exercises = append(session.Exercises, [2]string{row[0], row[1]})
		}
		plan.Sessions[key] = session
	})
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Workout saved"), planChanged)
	view, err := ui.sessionView(r, false, nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-session", view)
}

// ---------- daily checklist ----------

type checklistItem struct {
	Name     string
	State    string // "done", "skip" or ""
	Note     string
	NoteOpen bool // the skip-reason box is open
}

type checklistDot struct {
	Day   time.Time
	Ratio float64
	Today bool
	Shown bool // the day the card shows
}

// checklistCardView is one day's checklist: the page's day, or an earlier one picked from the dots.
type checklistCardView struct {
	Editing                     bool
	Day                         time.Time
	OnPageDay                   bool   // the card shows the page's day (not one picked from the dots)
	PageDay                     string // the page's day, for "Back to …"
	Items                       []checklistItem
	Done, Skipped, Total        int
	DonePercent, SkippedPercent float64
	Complete                    bool
	LastTwoWeeks                []checklistDot
	ItemsEditor                 rowEditor
}

const checklistURL = "/cards/checklist"

func (v checklistCardView) DayKey() string { return v.Day.Format(dayLayout) }

func (ui *UI) renderChecklistCard(r *http.Request) (template.HTML, error) {
	view, err := ui.checklistView(r, isEditing(r), shownDay(r), r.URL.Query().Get("note"), nil)
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-checklist", view)
}

func (ui *UI) checklistView(r *http.Request, editing bool, shown time.Time, noteOpenFor string, editedItems [][]string) (checklistCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return checklistCardView{}, err
	}
	days := ui.ws(r).Store.Checklist()
	now := time.Now()
	states := days[shown.Format(dayLayout)]
	page := pageDay(r)
	view := checklistCardView{Editing: editing, Day: shown, OnPageDay: shown.Equal(page), PageDay: dayName(page), Total: len(plan.Checklist)}
	for _, name := range plan.Checklist {
		state, note := checkState(states[name])
		view.Items = append(view.Items, checklistItem{Name: name, State: state, Note: note, NoteOpen: state == "skip" && name == noteOpenFor})
		switch state {
		case "done":
			view.Done++
		case "skip":
			view.Skipped++
		}
	}
	if view.Total > 0 {
		view.DonePercent = float64(view.Done) / float64(view.Total) * 100
		view.SkippedPercent = float64(view.Skipped) / float64(view.Total) * 100
		view.Complete = view.Done+view.Skipped == view.Total
	}
	for i := 13; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		view.LastTwoWeeks = append(view.LastTwoWeeks, checklistDot{Day: day, Ratio: dayRatio(plan.Checklist, days, day), Today: i == 0,
			Shown: day.Format(dayLayout) == shown.Format(dayLayout)})
	}
	if editedItems == nil {
		editedItems = asRows(plan.Checklist)
	}
	view.ItemsEditor = rowEditor{URL: checklistURL, Rows: editedItems, AddLabel: "Add item", Columns: []rowColumn{{Placeholder: "Checklist item"}}}
	return view, nil
}

// checkState reads a stored checklist value: "done", "skip" or {"state": "skip", "note": ...}.
func checkState(value any) (state, note string) {
	switch v := value.(type) {
	case string:
		return v, ""
	case map[string]any:
		state, _ = v["state"].(string)
		note, _ = v["note"].(string)
		return state, note
	}
	return "", ""
}

func (ui *UI) checklistCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.checklistView(r, isEditing(r), shownDay(r), r.URL.Query().Get("note"), nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-checklist", view)
}

// saveChecklistItems saves the list of checklist items, or applies a row operation.
func (ui *UI) saveChecklistItems(w http.ResponseWriter, r *http.Request) {
	rows := rowsFromForm(r, 1)
	if operation := rowOperation(r); operation != "" {
		view, err := ui.checklistView(r, true, shownDay(r), "", applyRowOperation(rows, operation, 1))
		if err != nil {
			fail(w, err)
			return
		}
		ui.render(w, "card-checklist", view)
		return
	}
	if err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) { plan.Checklist = firstColumn(cleanRows(rows)) }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Checklist saved"), planChanged, checklistChanged)
	ui.renderChecklistAfterChange(r, w, shownDay(r), "")
}

// setChecklistItem ticks, skips or clears an item on the card's day (today, or an earlier day):
// item, state ("done", "skip", "none" or, from the checkbox, done=1 when ticked), and an
// optional skip reason.
func (ui *UI) setChecklistItem(w http.ResponseWriter, r *http.Request) {
	item := r.FormValue("item")
	state := r.FormValue("state")
	if state == "" { // the checkbox: ticked sends done=1, unticked sends nothing
		state = "none"
		if r.FormValue("done") != "" {
			state = "done"
		}
	}
	shown := shownDay(r)
	day := shown.Format(dayLayout)
	items := ui.ws(r).Store.Checklist()[day]
	if items == nil {
		items = map[string]any{}
	}
	note := formText(r, "note")
	switch state {
	case "done":
		items[item] = "done"
	case "skip":
		items[item] = "skip"
		if note != "" {
			items[item] = map[string]any{"state": "skip", "note": note}
		}
	default:
		delete(items, item)
	}
	if err := ui.ws(r).Store.SaveChecklistDay(day, items); err != nil {
		fail(w, err)
		return
	}
	// Skipping opens the reason box; saving a reason closes it.
	noteOpen := ""
	if state == "skip" && !r.Form.Has("note") {
		noteOpen = item
	}
	var message *notice
	if r.Form.Has("note") {
		message = say("Reason saved")
	}
	announce(w, message, checklistChanged)
	ui.renderChecklistAfterChange(r, w, shown, noteOpen)
}

func (ui *UI) renderChecklistAfterChange(r *http.Request, w http.ResponseWriter, shown time.Time, noteOpenFor string) {
	view, err := ui.checklistView(r, false, shown, noteOpenFor, nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-checklist", view)
}

// ---------- weight ----------

type weightCardView struct {
	Editing      bool
	Plan         store.WeightPlan
	Current      float64
	LastWeighIn  time.Time
	HasWeighIns  bool
	WeighedToday bool
	TodayLB      float64 // the shown day's weigh-in
	DayName      string  // "today", "yesterday", "Mon, Oct 19"
	Change       float64 // since the plan's start
	StartDate    time.Time
	Trend        *sparkline
	Reached      bool    // the latest weight meets the target
	ToGo         float64 // lb left to the target (0 when reached or there's none)
	MyFitnessPal bool    // MyFitnessPal is connected, so the card asks whether to send weigh-ins there
}

// sparkline is the weight trend over the last 90 days.
type sparkline struct {
	Path     string
	From, To time.Time
	Points   []sparkPoint // each weigh-in, to hover over
}

// sparkPoint is one weigh-in on the trend, placed in percent of the chart's width and height.
type sparkPoint struct {
	Left, Top float64
	Day       time.Time
	LB        float64
}

func (ui *UI) renderWeightCard(r *http.Request) (template.HTML, error) {
	view, err := ui.weightView(r, isEditing(r))
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-weight", view)
}

func (ui *UI) weightView(r *http.Request, editing bool) (weightCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return weightCardView{}, err
	}
	weights := ui.ws(r).Store.Weights()
	today := time.Now().Format("2006-01-02")
	shown := shownDay(r)
	view := weightCardView{Editing: editing, Plan: plan.Weight, Current: plan.Weight.Start, DayName: dayName(shown),
		MyFitnessPal: ui.ws(r).MyFitnessPal.Client().Setup().SignedIn}
	view.StartDate, _ = time.ParseInLocation("2006-01-02", plan.Weight.StartDate, time.Local)
	if len(weights) > 0 {
		last := weights[len(weights)-1]
		view.Current, view.HasWeighIns = last.LB, true
		view.LastWeighIn, _ = time.ParseInLocation("2006-01-02", last.Date, time.Local)
		view.WeighedToday = last.Date == today
	}
	for _, w := range weights {
		if w.Date == shown.Format(dayLayout) {
			view.TodayLB = w.LB
		}
	}
	view.Change = math.Round((view.Current-plan.Weight.Start)*10) / 10
	view.Reached, view.ToGo = store.TargetReached(view.Current, plan.Weight), store.ToGo(view.Current, plan.Weight)
	view.Trend = weightTrend(weights, time.Now().AddDate(0, 0, -90).Format("2006-01-02"))
	return view, nil
}

// weightTrend draws the weigh-ins since `since` as an SVG path (300×60), or nil with fewer than two.
func weightTrend(weights []store.Weight, since string) *sparkline {
	var points []store.Weight
	for _, w := range weights {
		if w.Date >= since {
			points = append(points, w)
		}
	}
	if len(points) < 2 {
		return nil
	}
	low, high := points[0].LB, points[0].LB
	for _, p := range points {
		low, high = math.Min(low, p.LB), math.Max(high, p.LB)
	}
	low, high = low-1, high+1
	const width, height = 300.0, 60.0
	var path strings.Builder
	trend := &sparkline{}
	for i, p := range points {
		x := float64(i) / float64(len(points)-1) * width
		y := height - (p.LB-low)/(high-low)*height
		command := "L"
		if i == 0 {
			command = "M"
		}
		fmt.Fprintf(&path, "%s%.1f,%.1f ", command, x, y)
		day, _ := time.ParseInLocation("2006-01-02", p.Date, time.Local)
		trend.Points = append(trend.Points, sparkPoint{Left: x / width * 100, Top: y / height * 100, Day: day, LB: p.LB})
	}
	trend.Path = strings.TrimSpace(path.String())
	trend.From, trend.To = trend.Points[0].Day, trend.Points[len(trend.Points)-1].Day
	return trend
}

func (ui *UI) weightCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.weightView(r, isEditing(r))
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-weight", view)
}

// saveWeightPlan saves the starting weight, start date, target and lowest weight.
func (ui *UI) saveWeightPlan(w http.ResponseWriter, r *http.Request) {
	err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
		if start, err := strconv.ParseFloat(formText(r, "start"), 64); err == nil && start > 0 {
			plan.Weight.Start = start
		}
		if date := formText(r, "startDate"); date != "" {
			plan.Weight.StartDate = date
		}
		plan.Weight.Target = store.FlexText(formText(r, "target"))
		if best, err := strconv.ParseFloat(formText(r, "best"), 64); err == nil && best > 0 {
			plan.Weight.Best = best
		}
	})
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Weight settings saved"), planChanged)
	ui.renderWeightAfterChange(r, w)
}

// logWeight logs the shown day's weigh-in (lb), or removes it (remove=1), and ticks the "Weigh in"
// checklist item. When MyFitnessPal is connected the form asks (mfp) whether to send it there too,
// and the answer is kept as the default for next time.
func (ui *UI) logWeight(w http.ResponseWriter, r *http.Request) {
	day := shownDay(r).Format(dayLayout)
	if r.FormValue("remove") != "" {
		if err := ui.ws(r).Store.SetWeight(day, nil); err != nil {
			fail(w, err)
			return
		}
		announce(w, say("Removed here. Remove it in MyFitnessPal too, or the next sync brings it back."))
		ui.renderWeightAfterChange(r, w)
		return
	}
	lb, err := strconv.ParseFloat(formText(r, "lb"), 64)
	if err != nil || lb <= 50 || lb >= 700 {
		fail(w, problem.Invalidf("Enter a weight in lb"))
		return
	}
	lb = math.Round(lb*10) / 10
	if err := ui.ws(r).Store.SetWeight(day, &lb); err != nil {
		fail(w, err)
		return
	}
	ui.ws(r).Store.TickWeighIn(day)
	message := say("Weight logged")
	if r.FormValue("mfpAsked") != "" {
		send := r.FormValue("mfp") != ""
		if err := ui.rememberMyFitnessPalChoice(r, send); err != nil {
			fail(w, err)
			return
		}
		if send {
			message = say("Weight logged · sent to MyFitnessPal")
			if err := ui.ws(r).MyFitnessPal.LogWeight(day, lb); err != nil {
				message = &notice{Message: "Saved here, but not in MyFitnessPal: " + problem.Public(err), Bad: true}
			}
		}
	}
	announce(w, message, checklistChanged)
	ui.renderWeightAfterChange(r, w)
}

// rememberMyFitnessPalChoice saves whether weigh-ins go to MyFitnessPal, when that changed.
func (ui *UI) rememberMyFitnessPalChoice(r *http.Request, send bool) error {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil || plan.Weight.NoMyFitnessPal == !send {
		return err
	}
	return ui.ws(r).Store.UpdatePlan(func(p *store.Plan) { p.Weight.NoMyFitnessPal = !send })
}

func (ui *UI) renderWeightAfterChange(r *http.Request, w http.ResponseWriter) {
	view, err := ui.weightView(r, false)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-weight", view)
}

// ---------- this week ----------

type weekRow struct {
	Day     string
	Session string // its title, or "—"
	Key     string
	IsToday bool
}

type sessionChoice struct {
	Key, Title string
}

type weekCardView struct {
	Editing  bool
	Days     []weekRow
	Sessions []sessionChoice
}

func (ui *UI) renderWeekCard(r *http.Request) (template.HTML, error) {
	view, err := ui.weekView(r, isEditing(r), nil)
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-week", view)
}

// weekView is the week's sessions; chosen overrides the plan's picks while editing.
func (ui *UI) weekView(r *http.Request, editing bool, chosen map[string]string) (weekCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return weekCardView{}, err
	}
	view := weekCardView{Editing: editing}
	today := shownDay(r).Weekday().String()
	for _, day := range store.Weekdays {
		key := plan.Schedule[day]
		if picked, ok := chosen[day]; ok {
			key = picked
		}
		title := "—"
		if session, ok := plan.Sessions[key]; ok {
			title = session.Title
		}
		view.Days = append(view.Days, weekRow{Day: day, Session: title, Key: key, IsToday: day == today})
	}
	for key, session := range plan.Sessions {
		view.Sessions = append(view.Sessions, sessionChoice{Key: key, Title: session.Title})
	}
	sortSessions(view.Sessions)
	return view, nil
}

func sortSessions(choices []sessionChoice) {
	sort.Slice(choices, func(i, j int) bool { return strings.ToLower(choices[i].Title) < strings.ToLower(choices[j].Title) })
}

func (ui *UI) weekCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.weekView(r, isEditing(r), nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-week", view)
}

// chosenSessions reads the editor's picks: one select per weekday.
func chosenSessions(r *http.Request) map[string]string {
	r.ParseForm()
	chosen := map[string]string{}
	for _, day := range store.Weekdays {
		if r.Form.Has(day) {
			chosen[day] = r.FormValue(day)
		}
	}
	return chosen
}

func (ui *UI) saveWeek(w http.ResponseWriter, r *http.Request) {
	chosen := chosenSessions(r)
	err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
		for day, key := range chosen {
			plan.Schedule[day] = key
		}
	})
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Schedule saved"), planChanged)
	view, _ := ui.weekView(r, false, nil)
	ui.render(w, "card-week", view)
}

var nonWordRun = regexp.MustCompile(`[^a-z0-9]+`)

// addSession adds a new kind of training day (e.g. "Mountain bike"), keeping the editor's picks.
func (ui *UI) addSession(w http.ResponseWriter, r *http.Request) {
	title := formText(r, "title")
	chosen := chosenSessions(r)
	if title != "" {
		err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
			key := strings.Trim(nonWordRun.ReplaceAllString(strings.ToLower(title), "-"), "-")
			if key == "" {
				key = "session"
			}
			for plan.Sessions[key].Title != "" {
				key += "-2"
			}
			plan.Sessions[key] = store.Session{Title: title}
		})
		if err != nil {
			fail(w, err)
			return
		}
		announce(w, say(fmt.Sprintf("Added “%s”. Pick it for a day, then Save.", title)))
	}
	view, err := ui.weekView(r, true, chosen)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-week", view)
}

// ---------- reminders ----------

type notesCardView struct {
	Editing bool
	Notes   []string
	Editor  rowEditor
}

const notesURL = "/cards/notes"

func (ui *UI) renderNotesCard(r *http.Request) (template.HTML, error) {
	view, err := ui.notesView(r, isEditing(r), nil)
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-notes", view)
}

func (ui *UI) notesView(r *http.Request, editing bool, edited [][]string) (notesCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return notesCardView{}, err
	}
	if edited == nil {
		edited = asRows(plan.Notes)
	}
	return notesCardView{Editing: editing, Notes: plan.Notes,
		Editor: rowEditor{URL: notesURL, Rows: edited, AddLabel: "Add reminder", Columns: []rowColumn{{Placeholder: "Reminder"}}}}, nil
}

func (ui *UI) notesCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.notesView(r, isEditing(r), nil)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-notes", view)
}

func (ui *UI) saveNotes(w http.ResponseWriter, r *http.Request) {
	rows := rowsFromForm(r, 1)
	if operation := rowOperation(r); operation != "" {
		view, _ := ui.notesView(r, true, applyRowOperation(rows, operation, 1))
		ui.render(w, "card-notes", view)
		return
	}
	if err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) { plan.Notes = firstColumn(cleanRows(rows)) }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Reminders saved"), planChanged)
	view, _ := ui.notesView(r, false, nil)
	ui.render(w, "card-notes", view)
}

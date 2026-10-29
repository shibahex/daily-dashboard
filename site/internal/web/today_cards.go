package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"regexp"
	"slices"
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
	Change       float64 // from the range's first weigh-in to its last
	HasStart     bool    // there's a weigh-in in the range
	Lowest       float64
	StartDate    time.Time // the range's first weigh-in
	Trend        *sparkline
	Range        choice // what the card covers
	Shorter      string // the ranges the ‹ and › arrows step to ("" at either end)
	Longer       string
	Reached      bool    // the latest weight meets the target
	ToGo         float64 // lb left to the target (0 when reached or there's none)
	MyFitnessPal bool    // MyFitnessPal is connected, so the card asks whether to send weigh-ins there
	Average      weightAverage
}

// weightRanges are what the Weight card covers: its chart, the change, the average and the trend.
var weightRanges = []choice{{"7d", "Past week"}, {"14d", "Past 2 weeks"}, {"30d", "Last 30 days"},
	{"3m", "Last 3 months"}, {"6m", "Last 6 months"}, {"1y", "Last year"}, {"3y", "Last 3 years"}, {"all", "All time"}}

// weightRangeDays are the range's first and last days (YYYY-MM-DD; from is "" for all of them).
// Each ends today: the past week is today and the 6 days before.
func weightRangeDays(id string, now time.Time) (from, to string) {
	today := startOfDay(now)
	start := today.AddDate(0, -3, 0)
	switch id {
	case "7d":
		start = today.AddDate(0, 0, -6)
	case "14d":
		start = today.AddDate(0, 0, -13)
	case "30d":
		start = today.AddDate(0, 0, -29)
	case "6m":
		start = today.AddDate(0, -6, 0)
	case "1y":
		start = today.AddDate(-1, 0, 0)
	case "3y":
		start = today.AddDate(-3, 0, 0)
	case "all":
		return "", today.Format(dayLayout)
	}
	return start.Format(dayLayout), today.Format(dayLayout)
}

// weighInsBetween are the weigh-ins from one day to another, both included.
func weighInsBetween(weights []store.Weight, from, to string) []store.Weight {
	var kept []store.Weight
	for _, w := range weights {
		if w.Date >= from && w.Date <= to {
			kept = append(kept, w)
		}
	}
	return kept
}

// weightAverage is a range's weigh-ins: their average and which way they're heading.
type weightAverage struct {
	Count   int
	Average float64
	Weekly  bool    // PerWeek is known: there are weigh-ins in two different weeks
	PerWeek float64 // the average change a week, lb
	Change  float64 // without Weekly: the change from the first weigh-in to the last...
	Days    int     // ...this many days apart
	Trend   string  // "stable", "losing" or "gaining"; "" with weigh-ins on fewer than two days
}

// The trend is stable when it moves less than stableWeek lb a week (less than a day's water)
// and less than stableTotal lb over the whole range (a long one adds up); within a week, when
// it moves less than stableDays lb.
const stableWeek, stableTotal, stableDays = 0.3, 2.0, 1.0

// weightAverageOf averages the weigh-ins and works out how much they change a week on average.
// It averages each week's weigh-ins (weeks counted back from the last one) and fits a straight
// line through those weekly averages, each placed at the average day of its weigh-ins. A week
// without weigh-ins is left out, not counted as no change, and the time it spans still counts.
// Within a single week there's no weekly number: it gives the change over those days instead.
func weightAverageOf(points []store.Weight) weightAverage {
	view := weightAverage{Count: len(points)}
	if view.Count == 0 {
		return view
	}
	dayOf := func(w store.Weight) time.Time {
		day, _ := time.ParseInLocation(dayLayout, w.Date, time.Local)
		return day
	}
	last := dayOf(points[len(points)-1])
	daysBefore := func(w store.Weight) int { return int(math.Round(last.Sub(dayOf(w)).Hours() / 24)) }
	type week struct {
		days, lbs float64
		n         int
	}
	weeks := map[int]*week{}
	sum := 0.0
	for _, w := range points {
		sum += w.LB
		ago := daysBefore(w)
		if weeks[ago/7] == nil {
			weeks[ago/7] = &week{}
		}
		wk := weeks[ago/7]
		wk.days, wk.lbs, wk.n = wk.days-float64(ago), wk.lbs+w.LB, wk.n+1
	}
	view.Average = math.Round(sum/float64(view.Count)*10) / 10

	if len(weeks) >= 2 {
		var at, lbs []float64 // each week's place (in weeks) and average weight
		for _, wk := range weeks {
			at = append(at, wk.days/float64(wk.n)/7)
			lbs = append(lbs, wk.lbs/float64(wk.n))
		}
		perWeek, span := slope(at, lbs), slices.Max(at)-slices.Min(at)
		view.Weekly, view.PerWeek = true, math.Round(perWeek*10)/10
		view.Trend = trendOf(perWeek, math.Abs(perWeek) < stableWeek && math.Abs(perWeek*span) < stableTotal)
		return view
	}
	view.Days = daysBefore(points[0])
	if view.Days == 0 { // one day
		return view
	}
	view.Change = math.Round((points[len(points)-1].LB-points[0].LB)*10) / 10
	view.Trend = trendOf(view.Change, math.Abs(view.Change) < stableDays)
	return view
}

// slope is the slope of the straight line that best fits the points (least squares).
func slope(xs, ys []float64) float64 {
	meanX, meanY := 0.0, 0.0
	for i := range xs {
		meanX += xs[i] / float64(len(xs))
		meanY += ys[i] / float64(len(ys))
	}
	spread, together := 0.0, 0.0
	for i := range xs {
		spread += (xs[i] - meanX) * (xs[i] - meanX)
		together += (xs[i] - meanX) * (ys[i] - meanY)
	}
	if spread == 0 {
		return 0
	}
	return together / spread
}

func trendOf(change float64, stable bool) string {
	switch {
	case stable:
		return "stable"
	case change < 0:
		return "losing"
	}
	return "gaining"
}

// sparkline is the weight trend over the chart's range.
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
	start := plan.Weight.WithStart(weights) // which way the target is
	view := weightCardView{Editing: editing, Plan: plan.Weight, Current: start.Start, DayName: dayName(shown),
		MyFitnessPal: ui.ws(r).MyFitnessPal.Client().Setup().SignedIn, Lowest: plan.Weight.Lowest(weights),
	}
	at := slices.IndexFunc(weightRanges, func(c choice) bool { return c.ID == plan.Weight.Range })
	if at < 0 {
		at = slices.IndexFunc(weightRanges, func(c choice) bool { return c.ID == "3m" })
	}
	view.Range = weightRanges[at]
	if at > 0 {
		view.Shorter = weightRanges[at-1].ID
	}
	if at < len(weightRanges)-1 {
		view.Longer = weightRanges[at+1].ID
	}
	from, to := weightRangeDays(view.Range.ID, time.Now())
	points := weighInsBetween(weights, from, to)
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
	if len(points) > 0 {
		first, last := points[0], points[len(points)-1]
		view.HasStart = true
		view.Change = math.Round((last.LB-first.LB)*10) / 10
		view.StartDate, _ = time.ParseInLocation(dayLayout, first.Date, time.Local)
	}
	view.Reached, view.ToGo = store.TargetReached(view.Current, start), store.ToGo(view.Current, start)
	view.Trend = weightTrend(points)
	view.Average = weightAverageOf(points)
	return view, nil
}

// setWeightRange picks what the Weight card covers, and remembers it.
func (ui *UI) setWeightRange(w http.ResponseWriter, r *http.Request) {
	id := formText(r, "range")
	if !slices.ContainsFunc(weightRanges, func(c choice) bool { return c.ID == id }) {
		fail(w, problem.Invalidf("pick how far back the card goes"))
		return
	}
	if err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) { plan.Weight.Range = id }); err != nil {
		fail(w, err)
		return
	}
	ui.weightCard(w, r)
}

// weightTrend draws the weigh-ins as an SVG path (300×60), or nil with fewer than two.
func weightTrend(points []store.Weight) *sparkline {
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
	// Placed by date, so a stretch without weigh-ins shows as one.
	dayOf := func(w store.Weight) time.Time {
		day, _ := time.ParseInLocation(dayLayout, w.Date, time.Local)
		return day
	}
	first, span := dayOf(points[0]), dayOf(points[len(points)-1]).Sub(dayOf(points[0]))
	for i, p := range points {
		day := dayOf(p)
		x := float64(i) / float64(len(points)-1) * width
		if span > 0 {
			x = float64(day.Sub(first)) / float64(span) * width
		}
		y := height - (p.LB-low)/(high-low)*height
		command := "L"
		if i == 0 {
			command = "M"
		}
		fmt.Fprintf(&path, "%s%.1f,%.1f ", command, x, y)
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

// saveWeightPlan saves the starting weight, start date, target and lowest weight. A blank one is
// cleared: the card then goes by the weigh-ins.
func (ui *UI) saveWeightPlan(w http.ResponseWriter, r *http.Request) {
	weight := func(field string) float64 {
		value, err := strconv.ParseFloat(formText(r, field), 64)
		if err != nil || value <= 0 {
			return 0
		}
		return value
	}
	err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
		plan.Weight.Start, plan.Weight.Best = weight("start"), weight("best")
		plan.Weight.StartDate = ""
		if date := formText(r, "startDate"); isDay(date) {
			plan.Weight.StartDate = date
		}
		plan.Weight.Target = store.FlexText(formText(r, "target"))
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

// isDay says whether text is a YYYY-MM-DD day.
func isDay(text string) bool {
	_, err := time.Parse(dayLayout, text)
	return err == nil
}

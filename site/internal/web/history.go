package web

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
)

// The History tab: a year of activity as a heatmap, what happened each day, why things were
// skipped, and the time log.

type historyPageView struct {
	Activity activityView
	Skips    skipsView
	TimeLog  timeLogView
}

func (ui *UI) historyPage(w http.ResponseWriter, r *http.Request) {
	activity, err := ui.activityView(r)
	if err != nil {
		fail(w, err)
		return
	}
	skips, err := ui.skipsView(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.renderPage(r, w, "history", "History", "history", historyPageView{Activity: activity, Skips: skips, TimeLog: ui.timeLogView(r, 0)})
}

// ---------- what happened ----------

// activityData is everything the heatmap and the feed count, by local day ("2026-10-20").
type activityData struct {
	Today          time.Time
	Events         map[string][]taskEvent
	Hours          map[string]float64
	Reviews        anki.ReviewsByDay
	AnkiLive       bool
	Checklist      map[string]map[string]any
	ChecklistItems []string
}

// taskEvent is one thing that happened to a task.
type taskEvent struct {
	Kind   string // done, started, added, repeat, skip, dismiss, deleted
	Task   taskwarrior.Task
	Reason string
}

func (ui *UI) loadActivity(r *http.Request) (activityData, error) {
	all, err := ui.ws(r).Tasks.All()
	if err != nil {
		return activityData{}, err
	}
	plan, _ := ui.ws(r).Store.LoadPlan()
	now := time.Now()
	data := activityData{
		Today:          startOfDay(now),
		Events:         taskEventsByDay(all),
		Hours:          map[string]float64{},
		Checklist:      ui.ws(r).Store.Checklist(),
		ChecklistItems: plan.Checklist,
	}
	history := ui.ws(r).Anki().History(ui.ws(r).Paths.DataFile("anki_history.json"))
	data.Reviews, data.AnkiLive = history.Reviews, history.Live
	if intervals, err := ui.ws(r).Time.IntervalsBetween(data.Today.AddDate(-1, 0, -7), data.Today.AddDate(0, 0, 1)); err == nil {
		for _, interval := range intervals {
			data.Hours[interval.StartTime().Format(dayLayout)] += interval.Duration(now).Hours()
		}
	}
	return data, nil
}

func taskEventsByDay(all []taskwarrior.Task) map[string][]taskEvent {
	byDay := map[string][]taskEvent{}
	add := func(when time.Time, event taskEvent) {
		day := when.Format(dayLayout)
		byDay[day] = append(byDay[day], event)
	}
	for _, task := range all {
		end, ended := task.EndTime()
		switch {
		case task.Status() == "completed" && ended:
			add(end, taskEvent{Kind: "done", Task: task})
		case task.Status() == "deleted" && ended:
			add(end, deletedEvent(task))
		}
		if entry, ok := task.EntryTime(); ok && task.Parent() == "" {
			kind := "added"
			if task.IsRecurringTemplate() {
				kind = "repeat"
			}
			add(entry, taskEvent{Kind: kind, Task: task})
		}
		if start, ok := task.StartTime(); ok && task.Status() == "pending" {
			add(start, taskEvent{Kind: "started", Task: task})
		}
	}
	return byDay
}

// deletedEvent is a skip or dismissal when the task has a skip note, a plain delete otherwise.
func deletedEvent(task taskwarrior.Task) taskEvent {
	for _, note := range task.Annotations() {
		if skip, isSkip := tasks.ParseSkipNote(note.Description); isSkip {
			kind := "skip"
			if skip.IsDismiss() {
				kind = "dismiss"
			}
			return taskEvent{Kind: kind, Task: task, Reason: skip.Reason}
		}
	}
	return taskEvent{Kind: "deleted", Task: task}
}

func (a activityData) completedOn(day string) int {
	done := 0
	for _, event := range a.Events[day] {
		if event.Kind == "done" {
			done++
		}
	}
	return done
}

func (a activityData) checklistRatio(day time.Time) float64 {
	return dayRatio(a.ChecklistItems, a.Checklist, day)
}

// ---------- the heatmap ----------

type heatmapMode struct {
	Key, Label string
}

var heatmapModes = []heatmapMode{
	{"tasks", "Tasks done"}, {"hours", "Hours tracked"}, {"checklist", "Checklist"}, {"anki", "Anki reviews"},
}

const heatmapCookie = "heatmap"

type heatCell struct {
	Day      time.Time
	Row      int // weekday, Sunday first
	Column   int
	Level    int // 0 (nothing) to 4
	Label    string
	Selected bool
}

type monthLabel struct {
	Column int
	Name   string
}

type heatmapView struct {
	Mode    string
	Modes   []heatmapMode
	Columns int
	Months  []monthLabel
	Cells   []heatCell
	Summary string
}

// heatmap is 53 weeks ending this week, one column per week like GitHub's.
func (a activityData) heatmap(mode, selected string) heatmapView {
	view := heatmapView{Mode: mode, Modes: heatmapModes}
	start := a.Today.AddDate(0, 0, -364-int(a.Today.Weekday()))
	values := map[string]float64{}
	for day := start; !day.After(a.Today); day = day.AddDate(0, 0, 1) {
		if value := a.valueOn(mode, day); value > 0 {
			values[day.Format(dayLayout)] = value
		}
	}
	levels := levelsFor(mode, values)

	column, lastMonth := 0, time.Month(0)
	for day := start; !day.After(a.Today); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Sunday {
			column++
			if day.Month() != lastMonth {
				view.Months = append(view.Months, monthLabel{Column: column, Name: day.Format("Jan")})
				lastMonth = day.Month()
			}
		}
		key := day.Format(dayLayout)
		view.Cells = append(view.Cells, heatCell{
			Day: day, Row: int(day.Weekday()) + 1, Column: column, Level: levels(values[key]),
			Label: day.Format("Mon, Jan 2") + ": " + describeValue(mode, values[key]), Selected: key == selected,
		})
	}
	view.Columns = column
	view.Summary = a.heatmapSummary(mode, values)
	return view
}

func (a activityData) valueOn(mode string, day time.Time) float64 {
	key := day.Format(dayLayout)
	switch mode {
	case "hours":
		return a.Hours[key]
	case "anki":
		return float64(a.Reviews[key])
	case "checklist":
		return a.checklistRatio(day)
	}
	return float64(a.completedOn(key))
}

// levelsFor shades by quartiles of the active days, so one huge day doesn't wash out the rest.
// Checklist days are shaded by how much was done.
func levelsFor(mode string, values map[string]float64) func(float64) int {
	if mode == "checklist" {
		return func(v float64) int {
			switch {
			case v <= 0:
				return 0
			case v >= 1:
				return 4
			case v >= 0.66:
				return 3
			case v >= 0.33:
				return 2
			}
			return 1
		}
	}
	sorted := make([]float64, 0, len(values))
	for _, v := range values {
		sorted = append(sorted, v)
	}
	sort.Float64s(sorted)
	quartile := func(f float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		return sorted[min(len(sorted)-1, int(f*float64(len(sorted))))]
	}
	q1, q2, q3 := quartile(0.25), quartile(0.5), quartile(0.75)
	return func(v float64) int {
		switch {
		case v <= 0:
			return 0
		case v > q3:
			return 4
		case v > q2:
			return 3
		case v > q1:
			return 2
		}
		return 1
	}
}

func describeValue(mode string, v float64) string {
	switch mode {
	case "hours":
		return formatDuration(hoursToDuration(v)) + " tracked"
	case "anki":
		return countOf(int(v), "Anki review", "Anki reviews")
	case "checklist":
		return "checklist " + strconv.Itoa(int(v*100+0.5)) + "%"
	}
	return countOf(int(v), "task", "tasks") + " done"
}

func (a activityData) heatmapSummary(mode string, values map[string]float64) string {
	total, complete := 0.0, 0
	for _, v := range values {
		total += v
		if v >= 1 {
			complete++
		}
	}
	activeDays := len(values)
	var summary string
	switch mode {
	case "hours":
		summary = formatDuration(hoursToDuration(total)) + " tracked in the last year · " + countOf(activeDays, "day", "days")
	case "anki":
		summary = formatNumber(total) + " Anki reviews in the last year · " + countOf(activeDays, "day", "days") + " studied"
		if !a.AnkiLive {
			summary += " · saved copy (Anki is closed)"
		}
	case "checklist":
		summary = strconv.Itoa(complete) + " complete checklist days · " + strconv.Itoa(activeDays) + " days with progress"
	default:
		summary = countOf(int(total), "task", "tasks") + " completed in the last year · " + countOf(activeDays, "active day", "active days")
	}
	streak := 0
	day := a.Today
	if values[day.Format(dayLayout)] == 0 {
		day = day.AddDate(0, 0, -1) // today hasn't happened yet
	}
	for values[day.Format(dayLayout)] > 0 {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	if streak > 0 {
		summary += " · current streak " + countOf(streak, "day", "days")
	}
	return summary
}

func hoursToDuration(hours float64) time.Duration { return time.Duration(hours * float64(time.Hour)) }

// countOf is "1 task" or "3 tasks".
func countOf(n int, singular, pluralForm string) string {
	return strconv.Itoa(n) + " " + plural(n, singular, pluralForm)
}

// ---------- the feed ----------

type feedEntry struct {
	Kind        string
	Description string
	Project     string
	Reason      string
	Count       int
}

var feedLabels = map[string]struct{ Label, Icon string }{
	"done": {"Completed", "✓"}, "started": {"Started", "▶"}, "added": {"Added", "+"}, "repeat": {"Added repeating", "↻"},
	"skip": {"Skipped", "↷"}, "dismiss": {"Dismissed", "✕"}, "deleted": {"Deleted", "✕"},
}

var feedOrder = []string{"done", "started", "added", "repeat", "skip", "dismiss", "deleted"}

func (e feedEntry) Label() string { return feedLabels[e.Kind].Label }
func (e feedEntry) Icon() string  { return feedLabels[e.Kind].Icon }

type feedDay struct {
	Day     time.Time
	Stats   []string // "3 done", "1h 20m tracked", "checklist 80%", "31 Anki"
	Reviews int
	Entries []feedEntry
}

const feedStep = 10

type activityView struct {
	Heatmap  heatmapView
	Selected string // a day picked on the heatmap, or ""
	Shown    int    // how many days the feed shows
	Days     []feedDay
	HasMore  bool
	Partial  bool // answering a click: the feed goes out of band
}

func (v activityView) MoreDays() int { return v.Shown + feedStep }

func (v activityView) SelectedDay() time.Time {
	day, _ := time.ParseInLocation(dayLayout, v.Selected, time.Local)
	return day
}

// activityView reads ?mode= (remembered in a cookie), ?day= and ?days=.
func (ui *UI) activityView(r *http.Request) (activityView, error) {
	data, err := ui.loadActivity(r)
	if err != nil {
		return activityView{}, err
	}
	mode := r.FormValue("mode")
	if mode == "" {
		if cookie, err := r.Cookie(heatmapCookie); err == nil {
			mode = cookie.Value
		}
	}
	if !slices.ContainsFunc(heatmapModes, func(m heatmapMode) bool { return m.Key == mode }) {
		mode = "tasks"
	}
	shown, err := strconv.Atoi(r.FormValue("days"))
	if err != nil || shown < feedStep {
		shown = feedStep
	}
	selected := r.FormValue("day")
	view := activityView{Heatmap: data.heatmap(mode, selected), Selected: selected, Shown: shown}

	days := data.activeDays()
	if selected != "" {
		days = []string{selected}
	}
	view.HasMore = len(days) > shown
	for _, day := range days[:min(shown, len(days))] {
		view.Days = append(view.Days, data.feedDay(day))
	}
	return view, nil
}

// activeDays are the days anything happened, newest first.
func (a activityData) activeDays() []string {
	seen := map[string]bool{}
	for day := range a.Events {
		seen[day] = true
	}
	for day, items := range a.Checklist {
		if len(items) > 0 {
			seen[day] = true
		}
	}
	for day, reviews := range a.Reviews {
		if reviews > 0 {
			seen[day] = true
		}
	}
	today := a.Today.Format(dayLayout)
	var days []string
	for day := range seen {
		if day <= today {
			days = append(days, day)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days
}

// feedDay is one day of the feed. Repeats (30 skipped copies of one task) are one line.
func (a activityData) feedDay(key string) feedDay {
	day, _ := time.ParseInLocation(dayLayout, key, time.Local)
	entry := feedDay{Day: day, Reviews: a.Reviews[key]}
	byLine := map[string]*feedEntry{}
	var lines []string
	for _, event := range a.Events[key] {
		line := event.Kind + "|" + event.Task.Description() + "|" + event.Reason
		if byLine[line] == nil {
			byLine[line] = &feedEntry{Kind: event.Kind, Description: event.Task.Description(), Project: event.Task.Project(), Reason: event.Reason}
			lines = append(lines, line)
		}
		byLine[line].Count++
	}
	for _, line := range lines {
		entry.Entries = append(entry.Entries, *byLine[line])
	}
	sort.SliceStable(entry.Entries, func(i, j int) bool {
		return slices.Index(feedOrder, entry.Entries[i].Kind) < slices.Index(feedOrder, entry.Entries[j].Kind)
	})

	if done := a.completedOn(key); done > 0 {
		entry.Stats = append(entry.Stats, strconv.Itoa(done)+" done")
	}
	if hours := a.Hours[key]; hours > 0 {
		entry.Stats = append(entry.Stats, formatDuration(hoursToDuration(hours))+" tracked")
	}
	if _, logged := a.Checklist[key]; logged {
		entry.Stats = append(entry.Stats, "checklist "+strconv.Itoa(int(a.checklistRatio(day)*100+0.5))+"%")
	}
	if entry.Reviews > 0 {
		entry.Stats = append(entry.Stats, strconv.Itoa(entry.Reviews)+" Anki")
	}
	return entry
}

// activityCard answers a mode change, a heatmap click or "Show older": the heatmap, plus the
// feed swapped in out of band.
func (ui *UI) activityCard(w http.ResponseWriter, r *http.Request) {
	if mode := r.FormValue("mode"); mode != "" {
		http.SetCookie(w, &http.Cookie{Name: heatmapCookie, Value: mode, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	}
	view, err := ui.activityView(r)
	if err != nil {
		fail(w, err)
		return
	}
	view.Partial = true
	ui.render(w, "activity", view)
}

// ---------- skipped and why ----------

type skippedItem struct {
	Name   string
	Reason reasonView
}

type skippedTask struct {
	Description string
	Dismissed   bool
	Count       int
	FirstDue    time.Time
	LastDue     time.Time
	Reason      reasonView
}

type skipDay struct {
	Day            time.Time
	HasChecklist   bool
	Done, Skipped  int
	Complete       bool
	ChecklistSkips []skippedItem
	TaskSkips      []skippedTask
}

type skipsView struct {
	Days []skipDay
}

// skipsView lists, by day, the checklist items skipped and the tasks skipped or dismissed, with
// their reasons. Task copies skipped together are one line.
func (ui *UI) skipsView(r *http.Request) (skipsView, error) {
	deleted, err := ui.ws(r).Tasks.Deleted()
	if err != nil {
		return skipsView{}, err
	}
	plan, _ := ui.ws(r).Store.LoadPlan()
	checklist := ui.ws(r).Store.Checklist()
	days := map[string]*skipDay{}
	dayFor := func(key string) *skipDay {
		if days[key] == nil {
			day, _ := time.ParseInLocation(dayLayout, key, time.Local)
			days[key] = &skipDay{Day: day}
		}
		return days[key]
	}

	for key, items := range checklist {
		for name, value := range items {
			if state, note := checkState(value); state == "skip" {
				dayFor(key).ChecklistSkips = append(dayFor(key).ChecklistSkips,
					skippedItem{Name: name, Reason: reasonView{Day: key, Item: name, Reason: note}})
			}
		}
	}
	// Copies skipped together (same task, note and moment) are one line.
	lines := map[string]*skippedTask{}
	lineDay := map[string]string{}
	var lineOrder []string
	for _, task := range deleted {
		for _, annotation := range task.Annotations() {
			skip, isSkip := tasks.ParseSkipNote(annotation.Description)
			when, ok := annotation.When()
			if !isSkip || !ok {
				continue
			}
			line := task.Description() + "|" + annotation.Description + "|" + annotation.Entry
			entry := lines[line]
			if entry == nil {
				entry = &skippedTask{Description: task.Description(), Dismissed: skip.IsDismiss(), Reason: reasonView{Reason: skip.Reason}}
				lines[line], lineDay[line] = entry, when.Format(dayLayout)
				lineOrder = append(lineOrder, line)
			}
			entry.Count++
			entry.Reason.addUUID(task.UUID())
			if due, ok := task.DueTime(); ok {
				if entry.FirstDue.IsZero() || due.Before(entry.FirstDue) {
					entry.FirstDue = due
				}
				if due.After(entry.LastDue) {
					entry.LastDue = due
				}
			}
		}
	}
	for _, line := range lineOrder {
		day := dayFor(lineDay[line])
		day.TaskSkips = append(day.TaskSkips, *lines[line])
	}

	for key, day := range days {
		sort.Slice(day.ChecklistSkips, func(i, j int) bool { return day.ChecklistSkips[i].Name < day.ChecklistSkips[j].Name })
		if items, logged := checklist[key]; logged {
			day.HasChecklist = true
			for _, item := range plan.Checklist {
				switch state, _ := checkState(items[item]); state {
				case "done":
					day.Done++
				case "skip":
					day.Skipped++
				}
			}
			day.Complete = dayRatio(plan.Checklist, checklist, day.Day) >= 1
		}
	}

	var view skipsView
	for _, day := range days {
		view.Days = append(view.Days, *day)
	}
	sort.Slice(view.Days, func(i, j int) bool { return view.Days[i].Day.After(view.Days[j].Day) })
	return view, nil
}

func (ui *UI) skipsCard(w http.ResponseWriter, r *http.Request) {
	view, err := ui.skipsView(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-skips", view)
}

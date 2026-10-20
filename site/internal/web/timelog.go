package web

import (
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

// ---------- the Today card ----------

type trackedLabel struct {
	Label   string
	Tracked time.Duration
	Percent float64 // of the biggest
}

type timeCardView struct {
	Available bool
	Running   *trackedLabel // what's being tracked now, and for how long
	Today     time.Duration
	Week      time.Duration
	Labels    []trackedLabel
}

func (ui *UI) renderTimeCard(r *http.Request) (template.HTML, error) {
	view, err := ui.timeCardView()
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-time", view)
}

func (ui *UI) timeCardView() (timeCardView, error) {
	intervals, err := ui.Time.IntervalsThisWeek()
	if problem.KindOf(err) == problem.Unavailable {
		return timeCardView{}, nil
	}
	if err != nil {
		return timeCardView{}, err
	}
	now := time.Now()
	dayStart := startOfDay(now)
	view := timeCardView{Available: true}
	byLabel := map[string]time.Duration{}
	for _, interval := range intervals {
		label := strings.Join(interval.Tags, " · ")
		if label == "" {
			label = "untagged"
		}
		tracked := interval.Duration(now)
		byLabel[label] += tracked
		view.Week += tracked
		end, running := interval.EndTime()
		if running {
			end = now
			view.Running = &trackedLabel{Label: label, Tracked: tracked}
		}
		if start := interval.StartTime(); end.After(dayStart) {
			view.Today += end.Sub(maxTime(start, dayStart))
		}
	}
	for label, tracked := range byLabel {
		view.Labels = append(view.Labels, trackedLabel{Label: label, Tracked: tracked})
	}
	sort.Slice(view.Labels, func(i, j int) bool { return view.Labels[i].Tracked > view.Labels[j].Tracked })
	for i := range view.Labels {
		view.Labels[i].Percent = float64(view.Labels[i].Tracked) / float64(view.Labels[0].Tracked) * 100
	}
	return view, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (ui *UI) timeCard(w http.ResponseWriter, r *http.Request) {
	html, err := ui.renderTimeCard(r)
	if err != nil {
		fail(w, err)
		return
	}
	writeHTML(w, html)
}

// ---------- the time log (History tab) ----------

// timeEntry is one Timewarrior interval as the log shows it. Ref (id and start) is how a change
// finds it again, since ids shift as entries are added.
type timeEntry struct {
	timewarrior.Interval
	Begin   time.Time
	End     time.Time
	Running bool
	Length  time.Duration
}

func (e timeEntry) StartInput() string { return e.Begin.Format(localMinuteLayout) }
func (e timeEntry) EndInput() string   { return e.End.Format(localMinuteLayout) }
func (e timeEntry) TagText() string    { return strings.Join(e.Tags, ", ") }
func (e timeEntry) Ref() string        { return strconv.Itoa(e.ID) + "|" + e.Start }

const localMinuteLayout = "2006-01-02T15:04"

type timeLogDay struct {
	Day     time.Time
	Tracked time.Duration
	Entries []timeEntry
}

// taskChoice is a task in the "Pick a task…" box: picking it fills in the tags its timer uses.
type taskChoice struct {
	Tags  string
	Label string
}

type taskChoiceGroup struct {
	Label   string
	Choices []taskChoice
}

type timeLogView struct {
	Week       int // 0 is this week, -1 last week...
	Monday     time.Time
	Sunday     time.Time
	Available  bool
	Problem    string
	Tracked    time.Duration
	Days       []timeLogDay
	AddDay     string
	TaskGroups []taskChoiceGroup
	Names      []string // suggestions for the "what" box
	Editing    string   // the entry being edited: "id|start"
}

func (v timeLogView) Previous() int { return v.Week - 1 }
func (v timeLogView) Next() int     { return min(0, v.Week+1) }

func (ui *UI) timeLogView(week int) timeLogView {
	now := time.Now()
	monday := startOfDay(now).AddDate(0, 0, -((int(now.Weekday())+6)%7)+7*week)
	view := timeLogView{Week: week, Monday: monday, Sunday: monday.AddDate(0, 0, 6), AddDay: monday.Format(dayLayout)}
	if week == 0 {
		view.AddDay = now.Format(dayLayout)
	}
	view.TaskGroups, view.Names = ui.taskChoices(now)

	intervals, err := ui.Time.IntervalsBetween(monday, monday.AddDate(0, 0, 7))
	switch {
	case problem.KindOf(err) == problem.Unavailable:
		return view
	case err != nil:
		view.Available, view.Problem = true, err.Error()
		return view
	}
	view.Available = true
	byDay := map[string]*timeLogDay{}
	for _, interval := range intervals {
		entry := timeEntry{Interval: interval, Begin: interval.StartTime(), Length: interval.Duration(now)}
		entry.End, entry.Running = interval.EndTime()
		key := entry.Begin.Format(dayLayout)
		if byDay[key] == nil {
			byDay[key] = &timeLogDay{Day: startOfDay(entry.Begin)}
		}
		byDay[key].Entries = append(byDay[key].Entries, entry)
		byDay[key].Tracked += entry.Length
		view.Tracked += entry.Length
	}
	for _, day := range byDay {
		sort.Slice(day.Entries, func(i, j int) bool { return day.Entries[i].Begin.Before(day.Entries[j].Begin) })
		view.Days = append(view.Days, *day)
	}
	sort.Slice(view.Days, func(i, j int) bool { return view.Days[i].Day.After(view.Days[j].Day) })
	return view
}

// taskChoices are pending tasks, repeating ones, and tasks done in the last two weeks, plus
// task names to suggest.
func (ui *UI) taskChoices(now time.Time) ([]taskChoiceGroup, []string) {
	all, err := ui.Tasks.All()
	if err != nil {
		return nil, nil
	}
	recent := now.AddDate(0, 0, -14)
	groups := []taskChoiceGroup{{Label: "Pending"}, {Label: "Repeating"}, {Label: "Done recently"}}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, task := range all {
		group := -1
		switch task.Status() {
		case "pending":
			group = 0
		case "recurring":
			group = 1
		case "completed":
			if end, ok := task.EndTime(); ok && end.After(recent) {
				group = 2
			}
		}
		if task.Status() == "pending" || task.Status() == "recurring" {
			names[task.Description()] = true
		}
		tags := strings.Join(task.TimewarriorTags(), ", ")
		if group < 0 || seen[tags] {
			continue
		}
		seen[tags] = true
		groups[group].Choices = append(groups[group].Choices, taskChoice{Tags: tags, Label: choiceLabel(task)})
	}
	for i := range groups {
		sort.Slice(groups[i].Choices, func(a, b int) bool { return groups[i].Choices[a].Label < groups[i].Choices[b].Label })
	}
	suggestions := make([]string, 0, len(names))
	for name := range names {
		suggestions = append(suggestions, name)
	}
	sort.Strings(suggestions)
	return groups, suggestions
}

func choiceLabel(task taskwarrior.Task) string {
	if task.Project() != "" {
		return task.Description() + " · " + task.Project()
	}
	return task.Description()
}

func weekFrom(r *http.Request) int {
	week, _ := strconv.Atoi(r.FormValue("week"))
	return min(0, week)
}

func (ui *UI) timeLogCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "card-timelog", ui.timeLogView(weekFrom(r)))
}

// trackTime logs time from the add form. An end before the start is past midnight.
func (ui *UI) trackTime(w http.ResponseWriter, r *http.Request) {
	day, err := time.ParseInLocation(dayLayout, formText(r, "day"), time.Local)
	if err != nil {
		fail(w, problem.Invalidf("Pick a day"))
		return
	}
	startClock, endClock := formText(r, "start"), formText(r, "end")
	endDay := day
	if endClock <= startClock {
		endDay = day.AddDate(0, 0, 1)
	}
	start := day.Format(dayLayout) + "T" + startClock
	end := endDay.Format(dayLayout) + "T" + endClock
	if err := ui.Time.Track(start, end, splitTimeTags(r.FormValue("tags"))); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Time logged"), timeChanged)
	nothing(w)
}

// splitTimeTags reads "Read a book, reading" as two tags (a tag can have spaces).
func splitTimeTags(typed string) []string {
	var tags []string
	for _, tag := range strings.Split(typed, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// timeEntryEditor turns an entry of the log into a form.
func (ui *UI) timeEntryEditor(w http.ResponseWriter, r *http.Request) {
	view := ui.timeLogView(weekFrom(r))
	view.Editing = r.PathValue("id") + "|" + r.FormValue("start")
	ui.render(w, "card-timelog", view)
}

// saveTimeEntry changes what changed: start, end, tags.
func (ui *UI) saveTimeEntry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	startWas := r.FormValue("start_was")
	old := timewarrior.Interval{Start: startWas, End: r.FormValue("end_was"), Tags: r.Form["tags_was"]}
	was := timeEntry{Interval: old, Begin: old.StartTime()}
	was.End, was.Running = old.EndTime()

	var edit timewarrior.IntervalEdit
	if start := formText(r, "start"); start != was.StartInput() {
		edit.Start = start
	}
	if end := formText(r, "end"); !was.Running && end != was.EndInput() {
		edit.End = end
	}
	if tags := splitTimeTags(r.FormValue("tags")); strings.Join(tags, "|") != strings.Join(old.Tags, "|") {
		edit.Tags = tags
	}
	newStart, newEnd := firstNonEmpty(edit.Start, was.StartInput()), firstNonEmpty(edit.End, was.EndInput())
	if !was.Running && newEnd <= newStart {
		fail(w, problem.Invalidf("End must be after start"))
		return
	}
	if err := ui.Time.EditInterval(id, startWas, edit); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Entry updated"), timeChanged)
	nothing(w)
}

func (ui *UI) deleteTimeEntry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := ui.Time.DeleteInterval(id, r.FormValue("start_was")); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Entry deleted"), timeChanged)
	nothing(w)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

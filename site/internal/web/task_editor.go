package web

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
)

// ---------- the editor ----------

type taskEditorView struct {
	Task   taskwarrior.Task
	Dates  editorDates
	Notes  []taskNote
	Series series
	Repeat *repeatView // nil for finished tasks
}

type taskNote struct {
	Text  string
	Added *time.Time
}

// editorDates are the task's dates as the edit boxes show them.
type editorDates struct {
	Due, Scheduled, Wait, End string
}

func (v taskEditorView) IsCopy() bool     { return v.Task.Parent() != "" }
func (v taskEditorView) IsTemplate() bool { return v.Task.IsRecurringTemplate() }
func (v taskEditorView) IsFinished() bool { return v.Task.IsFinished() }

func (v taskEditorView) EndLabel() string {
	if v.Task.Status() == "completed" {
		return "Completed on"
	}
	return "Deleted on"
}

func (v taskEditorView) Added() *time.Time { return timeOrNil(v.Task.EntryTime()) }

func (v taskEditorView) ShortUUID() string { return v.Task.UUID()[:8] }

// dateInput is what an edit box shows for a date: "2026-10-21", or "2026-10-21T14:30" when it
// has a real time.
func dateInput(t time.Time, ok bool) string {
	if !ok {
		return ""
	}
	if clock := t.Format("15:04"); clock != "00:00" && clock != "23:59" {
		return t.Format("2006-01-02T15:04")
	}
	return t.Format(dayLayout)
}

func (d taskData) editorView(task taskwarrior.Task) taskEditorView {
	view := taskEditorView{
		Task:   task,
		Series: d.seriesOf(task),
		Dates: editorDates{
			Due:       dateInput(task.DueTime()),
			Scheduled: dateInput(task.ScheduledTime()),
			Wait:      dateInput(task.WaitTime()),
			End:       dateInput(task.EndTime()),
		},
	}
	for _, note := range task.Annotations() {
		view.Notes = append(view.Notes, taskNote{Text: note.Description, Added: timeOrNil(note.When())})
	}
	if !task.IsFinished() {
		repeat := d.repeatSummary(task, view.Series)
		view.Repeat = &repeat
	}
	return view
}

// loadEditor finds the task in {uuid} along with everything its editor shows.
func (ui *UI) loadEditor(r *http.Request) (taskData, taskwarrior.Task, error) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		return data, nil, err
	}
	task, found := data.find(r.PathValue("uuid"))
	if !found {
		return data, nil, problem.NotFoundf("task not found")
	}
	return data, task, nil
}

func (ui *UI) taskEditor(w http.ResponseWriter, r *http.Request) {
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "task-editor", data.editorView(task))
}

// saveTask saves what changed in the editor. Name, project, tags and priority go to every repeat
// of the task when "scope" is "all".
func (ui *UI) saveTask(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	view := data.editorView(task)
	edit := tasks.Edit{
		UUID:        task.UUID(),
		Description: changedValue(r, "description", task.Description()),
		Changes: taskwarrior.Changes{
			Project:   changedValue(r, "project", task.Project()),
			Priority:  changedValue(r, "priority", task.Priority()),
			Due:       changedValue(r, "due", view.Dates.Due),
			Scheduled: changedValue(r, "scheduled", view.Dates.Scheduled),
			Wait:      changedValue(r, "wait", view.Dates.Wait),
		},
		End:         changedValue(r, "end", view.Dates.End),
		WholeSeries: r.FormValue("scope") == "all",
		Templates:   view.Series.UUIDs(),
	}
	typedTags := splitTags(r.FormValue("tags"))
	for _, tag := range typedTags {
		if !slices.Contains(task.Tags(), tag) {
			edit.Changes.AddTags = append(edit.Changes.AddTags, tag)
		}
	}
	for _, tag := range task.Tags() {
		if !slices.Contains(typedTags, tag) {
			edit.Changes.RemoveTags = append(edit.Changes.RemoveTags, tag)
		}
	}
	if edit.ChangesNothing() {
		nothing(w) // nothing changed: just close
		return
	}
	if err := ui.ws(r).Tasks.Modify(edit); err != nil {
		fail(w, err)
		return
	}
	announce(w, sayUndoable("Saved"), tasksChanged)
	nothing(w)
}

// changedValue is the form's value for a field it has and that differs from what's saved.
func changedValue(r *http.Request, field, saved string) *string {
	if _, sent := r.PostForm[field]; !sent {
		return nil
	}
	if value := formText(r, field); value != saved {
		return &value
	}
	return nil
}

func (ui *UI) addTaskNote(w http.ResponseWriter, r *http.Request) {
	ui.changeNotes(w, r, ui.ws(r).Tasks.AddNote, formText(r, "note"), "Note added")
}

func (ui *UI) removeTaskNote(w http.ResponseWriter, r *http.Request) {
	ui.changeNotes(w, r, ui.ws(r).Tasks.RemoveNote, r.FormValue("remove"), "Note removed")
}

// changeNotes adds or removes a note and answers with the editor, still open.
func (ui *UI) changeNotes(w http.ResponseWriter, r *http.Request, change func(uuid, note string) error, note, done string) {
	if err := change(r.PathValue("uuid"), note); err != nil {
		fail(w, err)
		return
	}
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, sayUndoable(done))
	ui.render(w, "task-editor", data.editorView(task))
}

// ---------- repeat schedules ----------

// repeatView is the editor's Repeat section: the schedule in words, or the form to change it.
type repeatView struct {
	UUID    string
	Summary string
	Repeats bool
	Next    time.Time
	HasNext bool

	Editing    bool
	Type       string // "days", "weekly" or "monthly"
	EveryDays  int
	EveryWeeks int
	Weekdays   []weekdayChoice
	Start      string
	Preview    []time.Time
	Problem    string
}

type weekdayChoice struct {
	Number  int
	Name    string
	Checked bool
}

// weekChoice is an option of the "every N weeks" box.
type weekChoice struct {
	Every int
	Label string
}

func (repeatView) WeekChoices() []weekChoice {
	return []weekChoice{{1, "week"}, {2, "2 weeks (every other)"}, {3, "3 weeks"}, {4, "4 weeks"}}
}

func (d taskData) repeatSummary(task taskwarrior.Task, s series) repeatView {
	view := repeatView{UUID: task.UUID(), Summary: "Doesn't repeat", Repeats: s.Repeats()}
	if s.Repeats() {
		view.Summary = s.Summary()
		view.Next, view.HasNext = d.nextDue(s)
		if !view.HasNext {
			view.Next, view.HasNext = task.DueTime()
		}
	}
	return view
}

// repeatForm opens the schedule form with the current schedule, or redraws it (with a fresh
// preview) as it's changed.
func (ui *UI) repeatForm(w http.ResponseWriter, r *http.Request) {
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	s := data.seriesOf(task)
	view := data.repeatSummary(task, s)
	if r.FormValue("close") != "" {
		ui.render(w, "task-repeat", view)
		return
	}
	form := repeatFormFrom(r)
	if form.Type == "" {
		form = data.currentSchedule(task, s)
	}
	view.Editing = true
	view.Type, view.Start = form.Type, form.Start
	view.EveryDays, view.EveryWeeks = 1, 1
	every, _ := strconv.Atoi(form.Every)
	switch form.Type {
	case "days":
		view.EveryDays = max(every, 1)
	case "weekly":
		view.EveryWeeks = max(every, 1)
	}
	for day := range 7 {
		view.Weekdays = append(view.Weekdays, weekdayChoice{
			Number: day, Name: taskwarrior.ShortWeekday(time.Weekday(day)), Checked: slices.Contains(form.Days, strconv.Itoa(day)),
		})
	}
	if rule, err := form.Rule(data.Now); err != nil {
		view.Problem = err.Error()
	} else if view.Preview = rule.Upcoming(data.Now, 5); view.Preview == nil {
		view.Problem = "Pick at least one day"
	}
	ui.render(w, "task-repeat", view)
}

// repeatFormFrom reads the schedule form. Its interval box depends on the type.
func repeatFormFrom(r *http.Request) taskwarrior.RepeatForm {
	form := taskwarrior.RepeatForm{Type: r.FormValue("repeat_type"), Days: r.Form["repeat_day"], Start: r.FormValue("repeat_start")}
	switch form.Type {
	case "days":
		form.Every = r.FormValue("every_days")
	case "weekly":
		form.Every = r.FormValue("every_weeks")
	}
	return form
}

// currentSchedule is the form filled in from the task's schedule (or weekly, on the task's day).
func (d taskData) currentSchedule(task taskwarrior.Task, s series) taskwarrior.RepeatForm {
	next, found := d.nextDue(s)
	if !found {
		next, found = task.DueTime()
	}
	if !found {
		next = d.Now
	}
	form := taskwarrior.RepeatForm{Type: "weekly", Every: "1", Start: next.Format(dayLayout)}
	if !s.Repeats() {
		form.Days = []string{strconv.Itoa(int(next.Weekday()))}
		return form
	}
	period := taskwarrior.ParsePeriod(s.Templates[0].Recur())
	form.Every = strconv.Itoa(max(period.Every, 1))
	switch period.Unit {
	case "d":
		form.Type = "days"
	case "m":
		form.Type = "monthly"
	default:
		for _, template := range s.Templates {
			if due, ok := template.DueTime(); ok {
				form.Days = append(form.Days, strconv.Itoa(int(due.Weekday())))
			}
		}
	}
	return form
}

func (ui *UI) saveSchedule(w http.ResponseWriter, r *http.Request) {
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	rule, err := repeatFormFrom(r).Rule(data.Now)
	if err != nil {
		fail(w, problem.Invalidf(err.Error()))
		return
	}
	made, err := ui.ws(r).Tasks.ReplaceSchedule(task.UUID(), data.seriesOf(task).UUIDs(), rule)
	if err != nil {
		fail(w, err)
		return
	}
	message := "Schedule saved"
	if made > 1 {
		message += " (" + strconv.Itoa(made) + " weekly series)"
	}
	announce(w, sayUndoable(message), tasksChanged)
	nothing(w)
}

func (ui *UI) stopSchedule(w http.ResponseWriter, r *http.Request) {
	data, task, err := ui.loadEditor(r)
	if err != nil {
		fail(w, err)
		return
	}
	if err := ui.ws(r).Tasks.StopRepeating(data.seriesOf(task).UUIDs()); err != nil {
		fail(w, err)
		return
	}
	announce(w, sayUndoable("No longer repeats"), tasksChanged)
	nothing(w)
}

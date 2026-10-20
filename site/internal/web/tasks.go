package web

import (
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
)

// ---------- the Tasks tab ----------

type tasksPageView struct {
	Projects []string
	List     taskListView
}

// taskListView is one of the Tasks tab's lists, as the filter form asked for it.
type taskListView struct {
	View      string // "pending", "completed", "deleted" or "repeating"
	Query     string
	Sections  []taskSection
	Closed    closedListView
	Repeating []repeatingRow
}

const closedListStep = 50

func (d taskData) listView(r *http.Request) taskListView {
	query := strings.TrimSpace(r.FormValue("q"))
	view := taskListView{View: r.FormValue("view"), Query: query}
	switch view.View {
	case "completed", "deleted":
		limit, err := strconv.Atoi(r.FormValue("limit"))
		if err != nil || limit < closedListStep {
			limit = closedListStep
		}
		view.Closed = d.closedList(view.View, query, r.FormValue("auto") == "1", limit)
	case "repeating":
		view.Repeating = d.repeatingRows(query)
	default:
		view.View = "pending"
		var shown []taskRow
		for _, row := range d.pendingRows() {
			if matches(row.Task, query) {
				shown = append(shown, row)
			}
		}
		view.Sections = sectionsOf(shown)
	}
	return view
}

func (ui *UI) tasksPage(w http.ResponseWriter, r *http.Request) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.renderPage(r, w, "tasks", "Tasks", "tasks", tasksPageView{Projects: data.projects(), List: data.listView(r)})
}

func (ui *UI) taskList(w http.ResponseWriter, r *http.Request) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "task-list", data.listView(r))
}

// addTask adds a task from the add form, and answers with an empty form.
func (ui *UI) addTask(w http.ResponseWriter, r *http.Request) {
	err := ui.ws(r).Tasks.Add(tasks.NewTask{
		Description: formText(r, "description"),
		Project:     formText(r, "project"),
		Priority:    formText(r, "priority"),
		Due:         formText(r, "due"),
		Recur:       formText(r, "recur"),
		Tags:        splitTags(r.FormValue("tags")),
	})
	if err != nil {
		fail(w, err)
		return
	}
	data, _ := ui.loadTaskData(r)
	announce(w, sayUndoable("Task added"), tasksChanged)
	ui.render(w, "task-add-form", tasksPageView{Projects: data.projects()})
}

// splitTags reads "+gym, morning" as [gym morning].
func splitTags(typed string) []string {
	var tags []string
	for _, tag := range strings.FieldsFunc(typed, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
		if tag = strings.TrimPrefix(tag, "+"); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (ui *UI) undoTaskChange(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Tasks.Undo(); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Undone"), tasksChanged, timeChanged)
	nothing(w)
}

// ---------- the Today card ----------

type dueTasksView struct {
	Rows []taskRow
}

func (ui *UI) renderDueTasksCard(r *http.Request) (template.HTML, error) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		return "", err
	}
	var due []taskRow
	for _, row := range data.pendingRows() {
		if row.Bucket == "overdue" || row.Bucket == "today" {
			due = append(due, row)
		}
	}
	return ui.renderToHTML("card-tasks", dueTasksView{Rows: due})
}

func (ui *UI) dueTasksCard(w http.ResponseWriter, r *http.Request) {
	html, err := ui.renderDueTasksCard(r)
	if err != nil {
		fail(w, err)
		return
	}
	writeHTML(w, html)
}

// ---------- simple actions ----------

func (ui *UI) startTask(w http.ResponseWriter, r *http.Request) {
	ui.runTaskAction(w, r, "start", "Started")
}

func (ui *UI) stopTask(w http.ResponseWriter, r *http.Request) {
	ui.runTaskAction(w, r, "stop", "Stopped")
}

func (ui *UI) runTaskAction(w http.ResponseWriter, r *http.Request, action, done string) {
	if err := ui.ws(r).Tasks.Do(r.PathValue("uuid"), action); err != nil {
		fail(w, err)
		return
	}
	announce(w, sayUndoable(done), tasksChanged, timeChanged)
	nothing(w)
}

func (ui *UI) reopenTask(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Tasks.Reopen(r.PathValue("uuid")); err != nil {
		fail(w, err)
		return
	}
	announce(w, sayUndoable("Reopened"), tasksChanged)
	nothing(w)
}

// ---------- done ----------

// donePanel asks how long the task took. A started task doesn't ask: its timer logged the time.
func (ui *UI) donePanel(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "task-done", map[string]string{"UUID": r.PathValue("uuid")})
}

// completeTask completes a task now, logging the time it took (when the form says).
func (ui *UI) completeTask(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	minutes, err := parseTook(r.FormValue("took"))
	if err != nil {
		fail(w, err)
		return
	}
	warning, err := ui.ws(r).Tasks.CompleteAt(uuid, time.Now().Truncate(time.Minute), minutes)
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, completedNotice("Done ✓", minutes, warning), tasksChanged, timeChanged)
	nothing(w)
}

type doneOnView struct {
	UUID      string
	Day, Time string // what the inputs start with
	Today     string
}

// doneOnPanel asks which day (and time) a task was done. A missed task starts on its due day.
func (ui *UI) doneOnPanel(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	view := doneOnView{UUID: r.PathValue("uuid"), Day: now.Format(dayLayout), Time: now.Format("15:04"), Today: now.Format(dayLayout)}
	if task, err := ui.ws(r).Tasks.Find(view.UUID); err == nil {
		if due, ok := task.DueTime(); ok && due.Before(startOfDay(now)) {
			view.Day, view.Time = due.Format(dayLayout), "20:00"
		}
	}
	ui.render(w, "task-done-on", view)
}

func (ui *UI) completeTaskOn(w http.ResponseWriter, r *http.Request) {
	at := formText(r, "time")
	if at == "" {
		at = "20:00"
	}
	end, valid := tasks.ParseLocalMinute(formText(r, "day") + "T" + at)
	if !valid {
		fail(w, problem.Invalidf("Pick a date"))
		return
	}
	if end.After(time.Now()) {
		fail(w, problem.Invalidf("That's in the future"))
		return
	}
	minutes, err := parseTook(r.FormValue("took"))
	if err != nil {
		fail(w, err)
		return
	}
	warning, err := ui.ws(r).Tasks.CompleteAt(r.PathValue("uuid"), end, minutes)
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, completedNotice("Marked done on "+end.Format("Mon, Jan 2"), minutes, warning), tasksChanged, timeChanged)
	nothing(w)
}

// completedNotice is "Done ✓ · 45 min logged", or the warning when the time wasn't logged.
func completedNotice(message string, minutes int, warning string) *notice {
	if warning != "" {
		return &notice{Message: warning, Bad: true}
	}
	if minutes > 0 {
		message += " · " + minutesText(minutes) + " logged"
	}
	return sayUndoable(message)
}

var (
	tookClock   = regexp.MustCompile(`^(\d+):(\d{1,2})$`)
	tookWritten = regexp.MustCompile(`^(?:(\d+(?:\.\d+)?)\s*h(?:ours?|rs?)?)?\s*(?:(\d+)\s*(?:m(?:ins?|inutes?)?)?)?$`)
	errBadTook  = problem.Invalidf("Time taken: use minutes or hours, like 30, 45m, 1h 30m (up to 24h)")
)

// parseTook reads how long something took: "30", "45m", "1h", "1.5h", "1h 30m", "1:30". Blank
// is 0 (don't log time).
func parseTook(typed string) (int, error) {
	typed = strings.ToLower(strings.TrimSpace(typed))
	if typed == "" {
		return 0, nil
	}
	var hours float64
	var minutes int
	if m := tookClock.FindStringSubmatch(typed); m != nil {
		h, _ := strconv.Atoi(m[1])
		hours = float64(h)
		minutes, _ = strconv.Atoi(m[2])
	} else if m := tookWritten.FindStringSubmatch(typed); m != nil && (m[1] != "" || m[2] != "") {
		hours, _ = strconv.ParseFloat(m[1], 64)
		minutes, _ = strconv.Atoi(m[2])
	} else {
		return 0, errBadTook
	}
	total := int(hours*60+0.5) + minutes
	if total < 1 || total > 24*60 {
		return 0, errBadTook
	}
	return total, nil
}

// minutesText is "30 min" or "1h 30m".
func minutesText(minutes int) string {
	if minutes < 60 {
		return strconv.Itoa(minutes) + " min"
	}
	return formatDuration(time.Duration(minutes) * time.Minute)
}

const dayLayout = "2006-01-02"

// ---------- skipping ----------

type skipPanelView struct {
	UUID    string // the row the panel opens under
	UUIDs   string // the copies to skip, comma-separated
	Dismiss bool
	Count   int
}

func (v skipPanelView) Button() string {
	switch {
	case v.Dismiss:
		return "Dismiss"
	case v.Count > 1:
		return "Skip " + strconv.Itoa(v.Count)
	}
	return "Skip"
}

func (ui *UI) skipPanel(w http.ResponseWriter, r *http.Request) {
	uuids := commaList(r.FormValue("uuids"))
	ui.render(w, "task-skip", skipPanelView{
		UUID: r.PathValue("uuid"), UUIDs: strings.Join(uuids, ","), Dismiss: r.FormValue("kind") == "dismiss", Count: len(uuids),
	})
}

func (ui *UI) skipTasks(w http.ResponseWriter, r *http.Request) {
	uuids := commaList(r.FormValue("uuids"))
	reason := formText(r, "reason")
	dismiss := r.FormValue("kind") == "dismiss"
	if err := ui.ws(r).Tasks.Skip(uuids, reason, dismiss); err != nil {
		fail(w, err)
		return
	}
	message := "Dismissed"
	if !dismiss {
		message = "Skipped"
		if len(uuids) > 1 {
			message += " " + strconv.Itoa(len(uuids))
		}
		if reason != "" {
			message += " · reason saved"
		}
	}
	announce(w, sayUndoable(message), tasksChanged)
	nothing(w)
}

func commaList(joined string) []string {
	var values []string
	for _, value := range strings.Split(joined, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// ---------- catching up on a repeating task ----------

// catchUpView goes through each missed copy of a repeating task: done that day, or skipped.
// Nothing else on the page reloads until Close, so the panel stays open.
type catchUpView struct {
	UUID   string // the row the panel opens under
	Parent string
	Rows   []catchUpRow
}

type catchUpRow struct {
	UUID    string
	Due     time.Time
	Done    bool
	Minutes int
	Warning string
	Skipped *tasks.SkipNote
}

func (ui *UI) catchUpPanel(w http.ResponseWriter, r *http.Request) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		fail(w, err)
		return
	}
	task, found := data.find(r.PathValue("uuid"))
	if !found || task.Parent() == "" {
		fail(w, problem.NotFoundf("task not found"))
		return
	}
	view := catchUpView{UUID: task.UUID(), Parent: task.Parent()}
	for _, missed := range data.missedCopies(task.Parent()) {
		due, _ := missed.DueTime()
		view.Rows = append(view.Rows, catchUpRow{UUID: missed.UUID(), Due: due})
	}
	ui.render(w, "catch-up", view)
}

// missedCopies are a series' pending copies that were due before today, oldest first.
func (d taskData) missedCopies(parent string) []taskwarrior.Task {
	var missed []taskwarrior.Task
	for _, task := range d.Pending {
		if due, ok := task.DueTime(); ok && task.Parent() == parent && due.Before(startOfDay(d.Now)) {
			missed = append(missed, task)
		}
	}
	slices.SortFunc(missed, func(a, b taskwarrior.Task) int {
		dueA, _ := a.DueTime()
		dueB, _ := b.DueTime()
		return dueA.Compare(dueB)
	})
	return missed
}

// catchUpDone completes one missed copy at 20:00 on its due day.
func (ui *UI) catchUpDone(w http.ResponseWriter, r *http.Request) {
	task, err := ui.ws(r).Tasks.Find(r.PathValue("uuid"))
	if err != nil {
		fail(w, err)
		return
	}
	minutes, err := parseTook(r.FormValue("took"))
	if err != nil {
		fail(w, err)
		return
	}
	due, _ := task.DueTime()
	evening := time.Date(due.Year(), due.Month(), due.Day(), 20, 0, 0, 0, time.Local)
	warning, err := ui.ws(r).Tasks.CompleteAt(task.UUID(), evening, minutes)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "catch-up-row", catchUpRow{UUID: task.UUID(), Due: due, Done: true, Minutes: minutes, Warning: warning})
}

func (ui *UI) catchUpSkip(w http.ResponseWriter, r *http.Request) {
	task, err := ui.ws(r).Tasks.Find(r.PathValue("uuid"))
	if err != nil {
		fail(w, err)
		return
	}
	reason := formText(r, "reason")
	if err := ui.ws(r).Tasks.Skip([]string{task.UUID()}, reason, false); err != nil {
		fail(w, err)
		return
	}
	due, _ := task.DueTime()
	ui.render(w, "catch-up-row", catchUpRow{UUID: task.UUID(), Due: due, Skipped: &tasks.SkipNote{Label: "Skipped", Reason: reason}})
}

// catchUpSkipRest skips every copy still missed, and closes the panel.
func (ui *UI) catchUpSkipRest(w http.ResponseWriter, r *http.Request) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		fail(w, err)
		return
	}
	var uuids []string
	for _, missed := range data.missedCopies(r.FormValue("parent")) {
		uuids = append(uuids, missed.UUID())
	}
	if len(uuids) > 0 {
		if err := ui.ws(r).Tasks.Skip(uuids, formText(r, "reason"), false); err != nil {
			fail(w, err)
			return
		}
	}
	announce(w, sayUndoable("Skipped "+strconv.Itoa(len(uuids))), tasksChanged, timeChanged)
	nothing(w)
}

func (ui *UI) catchUpClose(w http.ResponseWriter, r *http.Request) {
	announce(w, nil, tasksChanged, timeChanged)
	nothing(w)
}

// ---------- skip reasons ----------

// reasonView is a skip reason you can click to change: of skipped tasks, or of a checklist
// item skipped on a day.
type reasonView struct {
	UUIDs     string // comma-separated
	Day, Item string
	Reason    string
}

func (v *reasonView) addUUID(uuid string) {
	if v.UUIDs != "" {
		v.UUIDs += ","
	}
	v.UUIDs += uuid
}

func reasonFrom(r *http.Request) reasonView {
	return reasonView{
		UUIDs: strings.Join(commaList(r.FormValue("uuids")), ","),
		Day:   r.FormValue("day"), Item: r.FormValue("item"),
		Reason: formText(r, "reason"),
	}
}

func (ui *UI) reasonForm(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "reason-form", reasonFrom(r))
}
func (ui *UI) reasonButton(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "reason-button", reasonFrom(r))
}

func (ui *UI) saveReason(w http.ResponseWriter, r *http.Request) {
	view := reasonFrom(r)
	var err error
	if view.Day != "" {
		err = ui.ws(r).Store.SetSkipReason(view.Day, view.Item, view.Reason)
	} else {
		err = ui.ws(r).Tasks.ChangeSkipReason(commaList(view.UUIDs), view.Reason)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if view.Day != "" {
		announce(w, say("Reason saved"), checklistChanged)
	} else {
		announce(w, say("Reason saved"))
	}
	ui.render(w, "reason-button", view)
}

func writeHTML(w http.ResponseWriter, html template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(html))
}

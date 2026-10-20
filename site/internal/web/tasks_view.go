package web

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
)

// taskData is what the task views are built from: the pending tasks, and every task (for
// repeating series' templates, and the Completed and Deleted lists).
type taskData struct {
	Pending []taskwarrior.Task
	All     []taskwarrior.Task
	Now     time.Time
}

func (ui *UI) loadTaskData() (taskData, error) {
	pending, err := ui.Tasks.Pending()
	if err != nil {
		return taskData{}, err
	}
	all, err := ui.Tasks.All()
	if err != nil {
		return taskData{}, err
	}
	return taskData{Pending: pending, All: all, Now: time.Now()}, nil
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ---------- pending rows ----------

// listedTask is a task in a list, with what its badges show.
type listedTask struct {
	taskwarrior.Task
}

func (t listedTask) Active() bool { return t.IsActive() }

func (t listedTask) PriorityLabel() string {
	return map[string]string{"H": "high", "M": "medium", "L": "low"}[t.Priority()]
}

// NotesTooltip is every note's text, one per line.
func (t listedTask) NotesTooltip() string {
	var lines []string
	for _, note := range t.Annotations() {
		lines = append(lines, note.Description)
	}
	return strings.Join(lines, "\n")
}

// taskRow is a pending task as the lists show it. A repeating task leaves one pending copy per
// missed day; those are one row, shown as the oldest copy.
type taskRow struct {
	listedTask
	Due          time.Time
	HasDue       bool
	Bucket       string // "overdue", "today", "week", "later", "none"
	Copies       int
	OverdueUUIDs []string
}

// DueLabel is "was due Mon, Oct 20", "due Fri, Oct 24" or "no due date".
func (r taskRow) DueLabel() string {
	switch {
	case !r.HasDue:
		return "no due date"
	case r.Bucket == "overdue":
		return "was due " + r.Due.Format("Mon, Jan 2")
	}
	return "due " + r.Due.Format("Mon, Jan 2")
}

func (r taskRow) MissedCount() int { return len(r.OverdueUUIDs) }

// CanCatchUp: a repeating task with missed copies can mark each one done or skipped.
func (r taskRow) CanCatchUp() bool { return r.Recur() != "" && len(r.OverdueUUIDs) > 0 }

// skipButton is the row's Skip/Dismiss button, if it has one.
type skipButton struct {
	Label string
	UUIDs string // comma-separated
	Kind  string // "skip" or "dismiss"
}

// Skip: a repeating task skips its missed copies (or today's copy); a one-off task is dismissed.
func (r taskRow) Skip() *skipButton {
	switch {
	case r.Recur() == "":
		return &skipButton{Label: "Dismiss", UUIDs: r.UUID(), Kind: "dismiss"}
	case len(r.OverdueUUIDs) > 0:
		return &skipButton{Label: "Skip " + strconv.Itoa(len(r.OverdueUUIDs)) + " missed", UUIDs: strings.Join(r.OverdueUUIDs, ","), Kind: "skip"}
	case r.Bucket == "today":
		return &skipButton{Label: "Skip today", UUIDs: r.UUID(), Kind: "skip"}
	}
	return nil
}

// pendingRows groups pending tasks into rows, soonest due first.
func (d taskData) pendingRows() []taskRow {
	today := startOfDay(d.Now)
	byKey := map[string]*taskRow{}
	var order []string
	for _, task := range d.Pending {
		key := task.Parent()
		if key == "" {
			key = task.UUID()
		}
		due, hasDue := task.DueTime()
		row, seen := byKey[key]
		if !seen {
			row = &taskRow{listedTask: listedTask{task}, Due: due, HasDue: hasDue}
			byKey[key] = row
			order = append(order, key)
		} else if hasDue && (!row.HasDue || due.Before(row.Due)) {
			row.Task, row.Due, row.HasDue = task, due, true
		}
		row.Copies++
		if hasDue && due.Before(today) {
			row.OverdueUUIDs = append(row.OverdueUUIDs, task.UUID())
		}
	}
	rows := make([]taskRow, 0, len(order))
	for _, key := range order {
		row := byKey[key]
		row.Bucket = bucketOf(row.Due, row.HasDue, d.Now)
		rows = append(rows, *row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.HasDue != b.HasDue {
			return a.HasDue
		}
		if a.HasDue && !a.Due.Equal(b.Due) {
			return a.Due.Before(b.Due)
		}
		return a.Urgency() > b.Urgency()
	})
	return rows
}

func bucketOf(due time.Time, hasDue bool, now time.Time) string {
	today := startOfDay(now)
	switch {
	case !hasDue:
		return "none"
	case due.Before(today):
		return "overdue"
	case due.Before(today.AddDate(0, 0, 1)):
		return "today"
	case due.Before(today.AddDate(0, 0, 7)):
		return "week"
	}
	return "later"
}

// taskSection is a group of rows under a heading ("Overdue · 2").
type taskSection struct {
	Title string
	Warn  bool
	Rows  []taskRow
}

var bucketTitles = []struct{ key, title string }{
	{"overdue", "Overdue"}, {"today", "Due today"}, {"week", "Next 7 days"}, {"later", "Later"}, {"none", "No due date"},
}

func sectionsOf(rows []taskRow) []taskSection {
	var sections []taskSection
	for _, bucket := range bucketTitles {
		var inBucket []taskRow
		for _, row := range rows {
			if row.Bucket == bucket.key {
				inBucket = append(inBucket, row)
			}
		}
		if len(inBucket) > 0 {
			sections = append(sections, taskSection{Title: bucket.title, Warn: bucket.key == "overdue", Rows: inBucket})
		}
	}
	return sections
}

// matches is the filter box: name, project, tags and notes, ignoring case.
func matches(task taskwarrior.Task, query string) bool {
	if query == "" {
		return true
	}
	haystack := []string{task.Description(), task.Project()}
	haystack = append(haystack, task.Tags()...)
	for _, note := range task.Annotations() {
		haystack = append(haystack, note.Description)
	}
	return strings.Contains(strings.ToLower(strings.Join(haystack, " ")), strings.ToLower(query))
}

// ---------- completed and deleted ----------

// closedRow is a completed or deleted task.
type closedRow struct {
	listedTask
	Ended    time.Time
	HasEnded bool
	Reason   *tasks.SkipNote // why it was skipped or dismissed
}

func (r closedRow) Verb() string {
	if r.Status() == "completed" {
		return "completed"
	}
	return "deleted"
}

func (r closedRow) ReopenLabel() string {
	if r.Status() == "completed" {
		return "Reopen"
	}
	return "Restore"
}

// WasDue is when the task was due, or nil.
func (r closedRow) WasDue() *time.Time { return timeOrNil(r.DueTime()) }

func timeOrNil(t time.Time, ok bool) *time.Time {
	if !ok {
		return nil
	}
	return &t
}

func closedRowOf(task taskwarrior.Task) closedRow {
	ended, hasEnded := task.EndTime()
	row := closedRow{listedTask: listedTask{task}, Ended: ended, HasEnded: hasEnded}
	for _, note := range task.Annotations() {
		if reason, isSkip := tasks.ParseSkipNote(note.Description); isSkip {
			row.Reason = &reason
			break
		}
	}
	return row
}

// closedGroup is one task, or every copy of a repeating task, in the Completed/Deleted lists.
type closedGroup struct {
	Key    string
	Copies []closedRow // newest first
}

func (g closedGroup) Latest() closedRow { return g.Copies[0] }
func (g closedGroup) Oldest() closedRow { return g.Copies[len(g.Copies)-1] }
func (g closedGroup) IsSeries() bool    { return len(g.Copies) > 1 }

type closedListView struct {
	Status        string // "completed" or "deleted"
	Groups        []closedGroup
	Total         int
	HiddenCopies  int // deleted repeat copies without a reason (shown on request)
	ShowAutomatic bool
	More          int // groups beyond the limit
	Limit         int
}

// closedList builds the Completed or Deleted list. In Deleted, copies a reschedule removed
// without a reason are hidden unless showAutomatic.
func (d taskData) closedList(status, query string, showAutomatic bool, limit int) closedListView {
	view := closedListView{Status: status, ShowAutomatic: showAutomatic, Limit: limit}
	byKey := map[string]*closedGroup{}
	var order []string
	for _, task := range d.All {
		if task.Status() != status || !matches(task, query) {
			continue
		}
		row := closedRowOf(task)
		isCopy := task.Parent() != "" || task.Recur() != ""
		if status == "deleted" && !showAutomatic && isCopy && row.Reason == nil {
			view.HiddenCopies++
			continue
		}
		view.Total++
		key := task.UUID()
		if isCopy {
			key = "series|" + task.Description() + "|" + task.Project()
		}
		group, seen := byKey[key]
		if !seen {
			group = &closedGroup{Key: key}
			byKey[key] = group
			order = append(order, key)
		}
		group.Copies = append(group.Copies, row)
	}
	for _, key := range order {
		group := byKey[key]
		sort.Slice(group.Copies, func(i, j int) bool { return group.Copies[i].Ended.After(group.Copies[j].Ended) })
		view.Groups = append(view.Groups, *group)
	}
	sort.SliceStable(view.Groups, func(i, j int) bool { return view.Groups[i].Latest().Ended.After(view.Groups[j].Latest().Ended) })
	if len(view.Groups) > limit {
		view.More = min(50, len(view.Groups)-limit)
		view.Groups = view.Groups[:limit]
	}
	return view
}

// ---------- repeating tasks ----------

// series is every template of one repeating task ("Mon, Wed, Fri" is three series).
type series struct {
	Templates []taskwarrior.Task
}

// seriesOf finds the templates of the task's repeating series: same name and project.
func (d taskData) seriesOf(task taskwarrior.Task) series {
	template := task
	if !task.IsRecurringTemplate() {
		template = nil
		for _, candidate := range d.All {
			if task.Parent() != "" && candidate.UUID() == task.Parent() {
				template = candidate
			}
		}
	}
	if template == nil {
		return series{}
	}
	var all series
	for _, candidate := range d.All {
		if candidate.IsRecurringTemplate() && candidate.Description() == template.Description() && candidate.Project() == template.Project() {
			all.Templates = append(all.Templates, candidate)
		}
	}
	return all
}

func (s series) UUIDs() []string {
	uuids := make([]string, len(s.Templates))
	for i, template := range s.Templates {
		uuids[i] = template.UUID()
	}
	return uuids
}

func (s series) Repeats() bool   { return len(s.Templates) > 0 }
func (s series) Summary() string { return taskwarrior.DescribeSchedule(s.Templates) }

// pendingCopies are the series' pending copies, soonest due first.
func (d taskData) pendingCopies(s series) []taskwarrior.Task {
	templates := map[string]bool{}
	for _, uuid := range s.UUIDs() {
		templates[uuid] = true
	}
	var copies []taskwarrior.Task
	for _, task := range d.Pending {
		if templates[task.Parent()] {
			copies = append(copies, task)
		}
	}
	sort.Slice(copies, func(i, j int) bool {
		a, _ := copies[i].DueTime()
		b, _ := copies[j].DueTime()
		return a.Before(b)
	})
	return copies
}

// nextDue is the soonest pending copy's due date.
func (d taskData) nextDue(s series) (time.Time, bool) {
	for _, task := range d.pendingCopies(s) {
		if due, ok := task.DueTime(); ok {
			return due, true
		}
	}
	return time.Time{}, false
}

// repeatingRow is one repeating task in the Repeating list.
type repeatingRow struct {
	Representative taskwarrior.Task // the copy (or template) the editor opens
	Series         series
	Next           time.Time
	HasNext        bool
	Missed         int
}

func (d taskData) repeatingRows(query string) []repeatingRow {
	seen := map[string]bool{}
	var rows []repeatingRow
	for _, template := range d.All {
		key := template.Description() + "|" + template.Project()
		if !template.IsRecurringTemplate() || seen[key] || !matches(template, query) {
			continue
		}
		seen[key] = true
		s := d.seriesOf(template)
		row := repeatingRow{Representative: template, Series: s}
		copies := d.pendingCopies(s)
		if len(copies) > 0 {
			row.Representative = copies[0]
		}
		row.Next, row.HasNext = d.nextDue(s)
		for _, copy := range copies {
			if due, ok := copy.DueTime(); ok && due.Before(startOfDay(d.Now)) {
				row.Missed++
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].HasNext != rows[j].HasNext {
			return rows[i].HasNext
		}
		return rows[i].Next.Before(rows[j].Next)
	})
	return rows
}

func (d taskData) find(uuid string) (taskwarrior.Task, bool) {
	for _, task := range d.All {
		if task.UUID() == uuid {
			return task, true
		}
	}
	return nil, false
}

// projects is every project name used, sorted (for the add form's suggestions).
func (d taskData) projects() []string {
	seen := map[string]bool{}
	var names []string
	for _, task := range d.All {
		if project := task.Project(); project != "" && !seen[project] {
			seen[project] = true
			names = append(names, project)
		}
	}
	sort.Strings(names)
	return names
}

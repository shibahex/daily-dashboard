// Package tasks is what the dashboard does with tasks: add, edit, complete (logging the time),
// skip with a reason, and repeat on a schedule. It works through Taskwarrior and Timewarrior.
package tasks

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/text"
	"dashboard/internal/timewarrior"
)

// Service runs task changes. Times are local.
type Service struct {
	tasks *taskwarrior.Client
	time  timewarrior.Client
}

func NewService(tasks *taskwarrior.Client, time timewarrior.Client) *Service {
	return &Service{tasks: tasks, time: time}
}

// ---------- reading ----------

// Pending is every pending task (repeating copies included).
func (s *Service) Pending() ([]taskwarrior.Task, error) { return s.list("status:pending") }

// All is every task ever: pending, completed, deleted, waiting and repeating templates.
func (s *Service) All() ([]taskwarrior.Task, error) { return s.list() }

// Deleted is every deleted task (skipped and dismissed ones keep their reason as a note).
func (s *Service) Deleted() ([]taskwarrior.Task, error) { return s.list("status:deleted") }

func (s *Service) Find(uuid string) (taskwarrior.Task, error) {
	if !taskwarrior.IsUUID(uuid) {
		return nil, problem.Invalidf("bad uuid")
	}
	task, found := s.tasks.Find(uuid)
	if !found {
		return nil, problem.NotFoundf("task not found")
	}
	return task, nil
}

func (s *Service) list(filter ...string) ([]taskwarrior.Task, error) {
	tasks, result := s.tasks.Tasks(filter...)
	if !result.OK() {
		return nil, commandError(result, "task export failed")
	}
	return tasks, nil
}

// ---------- adding and changing ----------

// NewTask is a task to add. Empty fields are left out.
type NewTask struct {
	Description, Project, Priority, Due, Recur string
	Tags                                       []string
}

func (s *Service) Add(task NewTask) error {
	description := strings.TrimSpace(task.Description)
	if description == "" || len([]rune(description)) > 300 {
		return problem.Invalidf("description required")
	}
	changes := taskwarrior.Changes{Recur: strings.TrimSpace(task.Recur), AddTags: task.Tags}
	changes.Project = nonEmpty(task.Project)
	changes.Priority = nonEmpty(task.Priority)
	changes.Due = nonEmpty(task.Due)
	if changes.Recur != "" && changes.Due == nil {
		return problem.Invalidf("a repeating task needs a due date")
	}
	seriesArgs, dateArgs, err := changes.Args()
	if err != nil {
		return problem.Invalidf(err.Error())
	}
	args := append(append(append([]string{"add"}, seriesArgs...), dateArgs...), "--", description)
	return s.run(args...)
}

// Edit is a change to an existing task. Nil fields stay as they are.
type Edit struct {
	UUID        string
	Description *string
	Changes     taskwarrior.Changes // project, priority, dates, tags
	End         *string             // when a finished task was finished
	// WholeSeries sends name, project, tags and priority to every template of a repeating task
	// (Templates; the page sends them all, since "Mon, Wed, Fri" is three series). Dates
	// always change on this copy only: on the series they'd give every copy the same date.
	WholeSeries bool
	Templates   []string
}

// ChangesNothing reports whether saving the edit would leave the task as it is.
func (e Edit) ChangesNothing() bool {
	c := e.Changes
	return e.Description == nil && e.End == nil && c.Project == nil && c.Priority == nil && c.Due == nil &&
		c.Scheduled == nil && c.Wait == nil && c.Recur == "" && len(c.AddTags) == 0 && len(c.RemoveTags) == 0
}

func (s *Service) Modify(edit Edit) error {
	if !taskwarrior.IsUUID(edit.UUID) {
		return problem.Invalidf("bad request")
	}
	seriesArgs, dateArgs, err := edit.Changes.Args()
	if err != nil {
		return problem.Invalidf(err.Error())
	}
	var descriptionArgs []string
	if edit.Description != nil {
		description := text.Truncate(text.Collapse(*edit.Description), 300)
		if description == "" {
			return problem.Invalidf("description can't be empty")
		}
		descriptionArgs = []string{"--", description}
	}
	task, found := s.tasks.Find(edit.UUID)
	if !found {
		return problem.NotFoundf("task not found")
	}
	if edit.End != nil {
		end := strings.TrimSpace(*edit.End)
		if !task.IsFinished() || end == "" || !taskwarrior.IsDateValue(end) {
			return problem.Invalidf("can only set the completion date of a finished task")
		}
		dateArgs = append(dateArgs, "end:"+end)
	}

	templates := taskwarrior.FilterUUIDs(edit.Templates)
	if task.IsRecurringTemplate() && !slices.Contains(templates, edit.UUID) {
		templates = append(templates, edit.UUID)
	}
	if len(templates) == 0 && task.Parent() != "" {
		templates = []string{task.Parent()}
	}
	seriesChange := append(append([]string{}, seriesArgs...), descriptionArgs...)

	var steps [][]string
	switch {
	case edit.WholeSeries && len(templates) > 0:
		if len(seriesChange) > 0 {
			for _, template := range templates {
				steps = append(steps, append([]string{"rc.recurrence.confirmation=yes", template, "modify"}, seriesChange...))
			}
		}
		if len(dateArgs) > 0 && !task.IsRecurringTemplate() {
			steps = append(steps, append([]string{edit.UUID, "modify"}, dateArgs...))
		}
	case len(seriesChange) > 0 || len(dateArgs) > 0:
		step := append(append([]string{edit.UUID, "modify"}, seriesArgs...), dateArgs...)
		steps = append(steps, append(step, descriptionArgs...))
	}
	for _, step := range steps {
		if err := s.run(step...); err != nil {
			return err
		}
	}
	return nil
}

// Do runs `task <uuid> done|start|stop`.
func (s *Service) Do(uuid, action string) error {
	if !taskwarrior.IsUUID(uuid) {
		return problem.Invalidf("bad uuid")
	}
	return s.run(uuid, action)
}

func (s *Service) Reopen(uuid string) error {
	if !taskwarrior.IsUUID(uuid) {
		return problem.Invalidf("bad uuid")
	}
	return s.run(uuid, "modify", "status:pending")
}

// Undo reverts the last Taskwarrior change.
func (s *Service) Undo() error { return s.run("undo") }

// AddNote adds a note to a task; RemoveNote removes one with this exact text.
func (s *Service) AddNote(uuid, note string) error    { return s.annotate(uuid, "annotate", note) }
func (s *Service) RemoveNote(uuid, note string) error { return s.annotate(uuid, "denotate", note) }

func (s *Service) annotate(uuid, action, note string) error {
	note = text.Truncate(text.Collapse(note), 500)
	if !taskwarrior.IsUUID(uuid) || note == "" {
		return problem.Invalidf("bad request")
	}
	return s.run(uuid, action, "--", note)
}

// ---------- completing ----------

// CompleteAt completes a task as of `end` and, with minutes > 0, logs that much time ending then
// in Timewarrior (with the tags the Start button would give it). The returned warning says the
// task was completed but the time couldn't be logged.
func (s *Service) CompleteAt(uuid string, end time.Time, minutes int) (warning string, err error) {
	if minutes < 0 || minutes > 24*60 {
		return "", problem.Invalidf("minutes must be between 1 and 1440")
	}
	task, err := s.Find(uuid)
	if err != nil {
		return "", err
	}
	if err := s.run(uuid, "done", "end:"+end.Format(localMinute)); err != nil {
		return "", err
	}
	if minutes == 0 {
		return "", nil
	}
	start := end.Add(-time.Duration(minutes) * time.Minute)
	track := append([]string{"track", start.Format(localMinute), "-", end.Format(localMinute)}, task.TimewarriorTags()...)
	if result := s.time.Run(track...); !result.OK() {
		return "Completed, but the time wasn't logged: " + result.FirstErrorLine("timew error"), nil
	}
	return "", nil
}

// localMinute is how Taskwarrior and Timewarrior take a local time to the minute.
const localMinute = "2006-01-02T15:04"

// ParseLocalMinute reads "2026-10-20T14:30".
func ParseLocalMinute(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(localMinute, s, time.Local)
	return t, err == nil
}

// ---------- skipping ----------

// Skip notes the reason ("Skipped: tired"; "Dismissed" for one-off tasks) and deletes the tasks.
// Deleted tasks keep their notes, so History can show why.
func (s *Service) Skip(uuids []string, reason string, dismiss bool) error {
	if len(uuids) > 500 || !taskwarrior.AllUUIDs(uuids) {
		return problem.Invalidf("bad uuids")
	}
	label := "Skipped"
	if dismiss {
		label = "Dismissed"
	}
	note := SkipNote{Label: label, Reason: text.Truncate(text.Collapse(reason), 300)}.String()
	bulk := append([]string{"rc.bulk=0"}, uuids...)
	if err := s.run(append(append([]string{}, bulk...), "annotate", "--", note)...); err != nil {
		return err
	}
	return s.run(append(bulk, "delete")...)
}

// ChangeSkipReason rewrites the reason on skipped tasks. It goes through export and import so
// the note keeps its date (History groups by it).
func (s *Service) ChangeSkipReason(uuids []string, reason string) error {
	if len(uuids) > 500 || !taskwarrior.AllUUIDs(uuids) {
		return problem.Invalidf("bad uuids")
	}
	reason = text.Truncate(text.Collapse(reason), 300)
	tasks, err := s.list(uuids...)
	if err != nil {
		return err
	}
	var changed []taskwarrior.Task
	for _, task := range tasks {
		for i, annotation := range task.Annotations() {
			if note, isSkip := ParseSkipNote(annotation.Description); isSkip {
				task.SetAnnotationText(i, SkipNote{Label: note.Label, Reason: reason}.String())
				changed = append(changed, task)
				break
			}
		}
	}
	if len(changed) == 0 {
		return problem.Invalidf("no skip reason found on those tasks")
	}
	imported, _ := json.Marshal(changed)
	return commandErr(s.tasks.RunWithInput(string(imported), "import", "-"))
}

// ---------- repeat schedules ----------

var newUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// ReplaceSchedule gives a task a new repeat schedule. It creates the new series first and then
// deletes the old ones (templates, and their pending copies), so a failure leaves the old
// schedule in place. It returns how many series were made.
func (s *Service) ReplaceSchedule(source string, templates []string, rule taskwarrior.RepeatRule) (int, error) {
	if !taskwarrior.IsUUID(source) {
		return 0, problem.Invalidf("bad source")
	}
	templates = taskwarrior.FilterUUIDs(templates)
	task, found := s.tasks.Find(source)
	if !found {
		return 0, problem.NotFoundf("task not found")
	}
	series, err := rule.Series(time.Now())
	if err != nil {
		return 0, problem.Invalidf(err.Error())
	}

	attributes := []string{}
	if task.Project() != "" {
		attributes = append(attributes, "project:"+task.Project())
	}
	if task.Priority() != "" {
		attributes = append(attributes, "priority:"+task.Priority())
	}
	for _, tag := range task.Tags() {
		if taskwarrior.IsTag(tag) {
			attributes = append(attributes, "+"+tag)
		}
	}
	var created []string
	for _, one := range series {
		args := append(append([]string{"rc.verbose=new-uuid", "add"}, attributes...),
			"due:"+one.FirstDue+"T23:59:59", "recur:"+one.Recur, "--", task.Description())
		result := s.tasks.Run(args...)
		uuid := newUUID.FindString(result.Stdout)
		if !result.OK() || uuid == "" {
			if len(created) > 0 { // roll back the half-made schedule
				s.deleteSeries(created)
			}
			return 0, commandError(result, "couldn't create the new schedule")
		}
		created = append(created, uuid)
	}
	old := templates
	if task.Parent() == "" && !task.IsRecurringTemplate() {
		old = append(old, source) // a one-off task that now repeats
	}
	if len(old) > 0 {
		s.deleteSeries(old)
	}
	return len(created), nil
}

// StopRepeating deletes the series and their pending copies.
func (s *Service) StopRepeating(templates []string) error {
	templates = taskwarrior.FilterUUIDs(templates)
	if len(templates) == 0 {
		return problem.Invalidf("no series given")
	}
	return s.deleteSeries(templates)
}

func (s *Service) deleteSeries(templates []string) error {
	return s.run(append(append([]string{"rc.bulk=0", "rc.recurrence.confirmation=yes"}, templates...), "delete")...)
}

// ---------- helpers ----------

func (s *Service) run(args ...string) error { return commandErr(s.tasks.Run(args...)) }

func commandErr(result taskwarrior.Result) error {
	if result.OK() {
		return nil
	}
	return problem.Failedf(result.Error())
}

func commandError(result taskwarrior.Result, fallback string) error {
	message := result.Error()
	if message == "" {
		message = fallback
	}
	return problem.Failedf(message)
}

func nonEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

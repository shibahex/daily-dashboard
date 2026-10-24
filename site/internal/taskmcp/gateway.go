package taskmcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/text"
)

// Gateway runs the tools for one user, on that user's own tasks. Each run's sandbox gets a
// socket that leads to the gateway of the user the run is for, so it needs no key: whoever can
// reach the socket is that user's agent.
type Gateway struct {
	tasks *tasks.Service
}

func NewGateway(service *tasks.Service) *Gateway { return &Gateway{tasks: service} }

// Serve answers on listener until it's closed.
func (g *Gateway) Serve(listener net.Listener) {
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	server.Serve(listener)
}

const maxRequest = 1 << 20

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/call" {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequest+1))
	if err != nil || len(data) > maxRequest {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(data, &request) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reply, _ := json.Marshal(g.Call(request.Tool, request.Arguments))
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}

// Call runs one tool. Whatever goes wrong comes back as an error result for the AI to read,
// worded the way the Tasks tab words it.
func (g *Gateway) Call(tool string, arguments json.RawMessage) Result {
	if _, known := toolNamed(tool); !known {
		return failure("unknown tool " + tool)
	}
	out, err := g.run(tool, arguments)
	if err != nil {
		return failure(problem.Public(err))
	}
	return Result{Text: out}
}

func failure(message string) Result { return Result{Text: message, IsError: true} }

func (g *Gateway) run(tool string, arguments json.RawMessage) (string, error) {
	// Unknown fields are mistakes (a misspelled "due" would otherwise be dropped without a word).
	decode := func(into any) error {
		if len(bytes.TrimSpace(arguments)) == 0 || string(bytes.TrimSpace(arguments)) == "null" {
			arguments = json.RawMessage("{}")
		}
		decoder := json.NewDecoder(bytes.NewReader(arguments))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(into); err != nil {
			return problem.Invalidf("bad arguments: " + err.Error())
		}
		return nil
	}
	switch tool {
	case "tasks_list":
		var in listInput
		if err := decode(&in); err != nil {
			return "", err
		}
		return g.list(in)
	case "tasks_add":
		var in addInput
		if err := decode(&in); err != nil {
			return "", err
		}
		return g.add(in)
	case "tasks_modify":
		var in modifyInput
		if err := decode(&in); err != nil {
			return "", err
		}
		return g.modify(in)
	case "tasks_annotate":
		var in struct {
			UUID string `json:"uuid"`
			Note string `json:"note"`
		}
		if err := decode(&in); err != nil {
			return "", err
		}
		if _, err := g.tasks.Find(in.UUID); err != nil {
			return "", err
		}
		if err := g.tasks.AddNote(in.UUID, in.Note); err != nil {
			return "", err
		}
		return g.show("Added the note.", in.UUID)
	case "tasks_delete":
		var in struct {
			UUID   string `json:"uuid"`
			Reason string `json:"reason"`
		}
		if err := decode(&in); err != nil {
			return "", err
		}
		return g.delete(in.UUID, in.Reason)
	case "tasks_done", "tasks_reopen", "tasks_start", "tasks_stop":
		var in struct {
			UUID string `json:"uuid"`
		}
		if err := decode(&in); err != nil {
			return "", err
		}
		return g.change(tool, in.UUID)
	}
	return "", problem.Invalidf("unknown tool " + tool)
}

// ---------- reading ----------

type listInput struct {
	Status  string `json:"status"`
	Project string `json:"project"`
	Tag     string `json:"tag"`
	Search  string `json:"search"`
	DueBy   string `json:"due_by"`
	UUID    string `json:"uuid"`
	Limit   int    `json:"limit"`
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

func (g *Gateway) list(in listInput) (string, error) {
	if in.UUID != "" {
		task, err := g.tasks.Find(in.UUID)
		if err != nil {
			return "", err
		}
		return render(map[string]any{"tasks": []map[string]any{summarize(task)}}), nil
	}
	status := in.Status
	if status == "" {
		status = "pending"
	}
	if !slices.Contains([]string{"pending", "waiting", "completed", "deleted", "all"}, status) {
		return "", problem.Invalidf("status must be pending, waiting, completed, deleted or all")
	}
	if in.Tag != "" && !taskwarrior.IsTag(in.Tag) {
		return "", problem.Invalidf("bad tag")
	}
	var bound time.Time // tasks due before this are in
	if in.DueBy != "" {
		var ok bool
		if bound, ok = dueBound(in.DueBy, time.Now()); !ok {
			return "", problem.Invalidf("due_by must be a date like 2026-10-25, 2026-10-25T09:30, today or tomorrow")
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)

	var all []taskwarrior.Task
	var err error
	if status == "pending" {
		all, err = g.tasks.Pending()
	} else {
		all, err = g.tasks.All()
	}
	if err != nil {
		return "", err
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	var matches []taskwarrior.Task
	for _, task := range all {
		switch {
		case task.IsRecurringTemplate():
			continue // a repeating schedule is changed on the Tasks tab
		case !hasStatus(task, status):
			continue
		case in.Project != "" && !inProject(task.Project(), in.Project):
			continue
		case in.Tag != "" && !slices.Contains(task.Tags(), in.Tag):
			continue
		case search != "" && !mentions(task, search):
			continue
		case !bound.IsZero():
			if due, ok := task.DueTime(); !ok || !due.Before(bound) {
				continue
			}
		}
		matches = append(matches, task)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, aok := matches[i].DueTime()
		b, bok := matches[j].DueTime()
		switch {
		case aok && bok:
			return a.Before(b)
		default:
			return aok && !bok // tasks with a due date first
		}
	})
	shown := matches[:min(len(matches), limit)]
	summaries := make([]map[string]any, 0, len(shown))
	for _, task := range shown {
		summaries = append(summaries, summarize(task))
	}
	return render(map[string]any{"total": len(matches), "shown": len(shown), "tasks": summaries}), nil
}

// inProject reports whether project is name or one of its sub-projects (home.garden is in home).
func inProject(project, name string) bool {
	return strings.EqualFold(project, name) || strings.HasPrefix(strings.ToLower(project), strings.ToLower(name)+".")
}

func mentions(task taskwarrior.Task, lower string) bool {
	if strings.Contains(strings.ToLower(task.Description()), lower) {
		return true
	}
	for _, note := range task.Annotations() {
		if strings.Contains(strings.ToLower(note.Description), lower) {
			return true
		}
	}
	return false
}

// dueBound reads the due_by filter into the time tasks must be due before: the end of the day for
// today, tomorrow and a date (so overdue tasks are in too), and just after the minute for a time.
func dueBound(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch strings.ToLower(s) {
	case "today":
		return startOfDay.AddDate(0, 0, 1), true
	case "tomorrow":
		return startOfDay.AddDate(0, 0, 2), true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.AddDate(0, 0, 1), true
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", strings.ToUpper(s), time.Local); err == nil {
		return t.Add(time.Minute), true
	}
	return time.Time{}, false
}

// isWaiting reports whether the task is hidden until its wait date (Taskwarrior 2 gives those the
// status "waiting"; 3 keeps them pending).
func isWaiting(task taskwarrior.Task) bool {
	if task.Status() == "waiting" {
		return true
	}
	wait, ok := task.WaitTime()
	return task.Status() == "pending" && ok && wait.After(time.Now())
}

func hasStatus(task taskwarrior.Task, status string) bool {
	switch status {
	case "all":
		return true
	case "pending":
		return task.Status() == "pending" && !isWaiting(task)
	case "waiting":
		return isWaiting(task)
	}
	return task.Status() == status
}

// summarize is a task as the AI reads it: the fields people set, dates in local time.
func summarize(task taskwarrior.Task) map[string]any {
	out := map[string]any{"uuid": task.UUID(), "description": task.Description(), "status": task.Status()}
	put := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	put("project", task.Project())
	put("priority", task.Priority())
	put("recur", task.Recur())
	put("parent", task.Parent())
	if tags := task.Tags(); len(tags) > 0 {
		out["tags"] = tags
	}
	for key, get := range map[string]func() (time.Time, bool){
		"due": task.DueTime, "scheduled": task.ScheduledTime, "wait": task.WaitTime,
		"start": task.StartTime, "end": task.EndTime, "entered": task.EntryTime,
	} {
		if t, ok := get(); ok {
			out[key] = t.Format("2006-01-02T15:04")
		}
	}
	if notes := task.Annotations(); len(notes) > 0 {
		texts := make([]string, 0, len(notes))
		for _, note := range notes {
			texts = append(texts, note.Description)
		}
		out["notes"] = texts
	}
	if urgency := task.Urgency(); urgency != 0 {
		out["urgency"] = float64(int(urgency*100)) / 100
	}
	return out
}

func render(value any) string {
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		return "couldn't write the answer"
	}
	if len(data) > maxResponse-1024 {
		return string(data[:maxResponse-1024]) + "\n[cut off: ask for fewer tasks (limit, project, search)]"
	}
	return string(data)
}

// show is the task as it is now, after a change.
func (g *Gateway) show(message, uuid string) (string, error) {
	task, err := g.tasks.Find(uuid)
	if err != nil {
		return message, nil // done, but not found again (a deleted task, say)
	}
	return message + "\n" + render(summarize(task)), nil
}

// ---------- changing ----------

type addInput struct {
	Description string   `json:"description"`
	Project     string   `json:"project"`
	Priority    string   `json:"priority"`
	Due         string   `json:"due"`
	Scheduled   string   `json:"scheduled"`
	Wait        string   `json:"wait"`
	Tags        []string `json:"tags"`
	Recur       string   `json:"recur"`
}

func (g *Gateway) add(in addInput) (string, error) {
	description := text.Collapse(in.Description)
	if description == "" {
		return "", problem.Invalidf("description is required")
	}
	if len([]rune(description)) > 300 {
		return "", problem.Invalidf("description can be at most 300 characters: put the rest in a note (tasks_annotate)")
	}
	uuid, err := g.tasks.AddTask(tasks.NewTask{
		Description: description, Project: in.Project, Priority: in.Priority, Due: in.Due,
		Scheduled: in.Scheduled, Wait: in.Wait, Recur: in.Recur, Tags: in.Tags,
	})
	if err != nil {
		return "", err
	}
	return g.show("Added the task.", uuid)
}

type modifyInput struct {
	UUID        string   `json:"uuid"`
	Description *string  `json:"description"`
	Project     *string  `json:"project"`
	Priority    *string  `json:"priority"`
	Due         *string  `json:"due"`
	Scheduled   *string  `json:"scheduled"`
	Wait        *string  `json:"wait"`
	AddTags     []string `json:"add_tags"`
	RemoveTags  []string `json:"remove_tags"`
}

func (g *Gateway) modify(in modifyInput) (string, error) {
	task, err := g.editable(in.UUID)
	if err != nil {
		return "", err
	}
	edit := tasks.Edit{UUID: in.UUID, Description: in.Description, Changes: taskwarrior.Changes{
		Project: in.Project, Priority: in.Priority, Due: in.Due, Scheduled: in.Scheduled, Wait: in.Wait,
		AddTags: in.AddTags, RemoveTags: in.RemoveTags,
	}}
	if edit.ChangesNothing() {
		return "", problem.Invalidf("nothing to change: pass at least one field besides uuid")
	}
	if task.IsFinished() && (in.Due != nil || in.Scheduled != nil || in.Wait != nil) {
		return "", problem.Invalidf("that task is finished: reopen it (tasks_reopen) before changing its dates")
	}
	if err := g.tasks.Modify(edit); err != nil {
		return "", err
	}
	return g.show("Changed the task.", in.UUID)
}

// change is done, reopen, start and stop.
func (g *Gateway) change(tool, uuid string) (string, error) {
	task, err := g.tasks.Find(uuid)
	if err != nil {
		return "", err
	}
	if task.IsRecurringTemplate() {
		return "", problem.Invalidf("that's a repeating schedule, not a task: change it on the Tasks tab")
	}
	switch tool {
	case "tasks_done":
		if task.IsFinished() {
			return "", problem.Invalidf("that task is already " + task.Status())
		}
		err = g.tasks.Do(uuid, "done")
	case "tasks_start":
		if task.IsFinished() {
			return "", problem.Invalidf("that task is " + task.Status() + ": reopen it first")
		}
		err = g.tasks.Do(uuid, "start")
	case "tasks_stop":
		if !task.IsActive() {
			return "", problem.Invalidf("that task isn't started")
		}
		err = g.tasks.Do(uuid, "stop")
	case "tasks_reopen":
		if !task.IsFinished() {
			return "", problem.Invalidf("that task isn't finished")
		}
		err = g.tasks.Reopen(uuid)
	}
	if err != nil {
		return "", err
	}
	message := map[string]string{"tasks_done": "Marked the task done.", "tasks_start": "Started the task.",
		"tasks_stop": "Stopped the task.", "tasks_reopen": "Reopened the task."}[tool]
	return g.show(message, uuid)
}

func (g *Gateway) delete(uuid, reason string) (string, error) {
	task, err := g.editable(uuid)
	if err != nil {
		return "", err
	}
	if task.IsFinished() {
		return "", problem.Invalidf("that task is already " + task.Status())
	}
	// A copy of a repeating task is skipped; any other task is dismissed (as on the Tasks tab).
	dismiss := task.Parent() == ""
	if err := g.tasks.Skip([]string{uuid}, reason, dismiss); err != nil {
		return "", err
	}
	if dismiss {
		return "Deleted the task (noted as dismissed).", nil
	}
	return "Deleted this copy of the repeating task (noted as skipped); the series goes on.", nil
}

// editable is the task, if it's one the AI may change: not a repeating schedule.
func (g *Gateway) editable(uuid string) (taskwarrior.Task, error) {
	task, err := g.tasks.Find(uuid)
	if err != nil {
		return nil, err
	}
	if task.IsRecurringTemplate() {
		return nil, problem.Invalidf("that's a repeating schedule, not a task: change it on the Tasks tab")
	}
	return task, nil
}

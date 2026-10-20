package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"dashboard/internal/taskwarrior"
	"dashboard/internal/text"
)

// "2026-10-20T14:30", as the page's date and time inputs send it.
var localDateTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)

// newUUID finds the uuid `task add` prints with rc.verbose=new-uuid.
var newUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func (s *Server) routeTasks(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks", s.exportTasks("status:pending"))
	mux.HandleFunc("GET /api/tasks/all", s.exportTasks()) // every task ever, for History
	mux.HandleFunc("GET /api/history", s.skipHistory)

	mux.HandleFunc("POST /api/tasks/add", handlePost(s.addTask))
	mux.HandleFunc("POST /api/tasks/modify", handlePost(s.modifyTask))
	mux.HandleFunc("POST /api/tasks/done_at", handlePost(s.completeTaskAt))
	for _, action := range []string{"done", "start", "stop"} {
		mux.HandleFunc("POST /api/tasks/"+action, handlePost(s.runOnTask(action)))
	}
	mux.HandleFunc("POST /api/tasks/reopen", handlePost(s.reopenTask))
	mux.HandleFunc("POST /api/tasks/undo", handlePost(s.undo))
	mux.HandleFunc("POST /api/tasks/delete", handlePost(s.deleteTasks))
	mux.HandleFunc("POST /api/tasks/skip", handlePost(s.skipTasks))
	mux.HandleFunc("POST /api/tasks/skip_note", handlePost(s.changeSkipReason))
	mux.HandleFunc("POST /api/tasks/annotate", handlePost(s.annotate("annotate")))
	mux.HandleFunc("POST /api/tasks/denotate", handlePost(s.annotate("denotate")))
	mux.HandleFunc("POST /api/series/schedule", handlePost(s.replaceSchedule))
	mux.HandleFunc("POST /api/series/stop", handlePost(s.stopRepeating))
}

func (s *Server) exportTasks(filter ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := s.Tasks.Export(filter...)
		if !result.OK() {
			writeError(w, http.StatusInternalServerError, orDefault(result.Error(), "task export failed"))
			return
		}
		writeBytes(w, http.StatusOK, []byte(result.Stdout), "application/json")
	}
}

// skippedTask is a task skipped or dismissed from the dashboard (deleted, with the reason as
// an annotation).
type skippedTask struct {
	Description any    `json:"description"`
	Project     any    `json:"project"`
	Due         any    `json:"due"`
	Kind        string `json:"kind"` // "skip" or "dismiss"
	Note        string `json:"note"`
	At          string `json:"at"`
}

func (s *Server) skipHistory(w http.ResponseWriter, r *http.Request) {
	tasks, result := s.Tasks.Tasks("status:deleted")
	if !result.OK() {
		writeError(w, http.StatusInternalServerError, orDefault(result.Error(), "task export failed"))
		return
	}
	skips := []skippedTask{}
	for _, task := range tasks {
		for _, note := range task.Annotations() {
			label, reason, isSkip := parseSkipNote(note.Description)
			if !isSkip {
				continue
			}
			kind := "skip"
			if label == "Dismissed" {
				kind = "dismiss"
			}
			skips = append(skips, skippedTask{Description: task["description"], Project: task["project"],
				Due: task["due"], Kind: kind, Note: reason, At: note.Entry})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"skips": skips})
}

// parseSkipNote reads "Skipped: tired" / "Dismissed" annotations.
func parseSkipNote(annotation string) (label, reason string, isSkip bool) {
	switch {
	case strings.HasPrefix(annotation, "Skipped"):
		label = "Skipped"
	case strings.HasPrefix(annotation, "Dismissed"):
		label = "Dismissed"
	default:
		return "", "", false
	}
	if _, after, found := strings.Cut(annotation, ":"); found {
		reason = strings.TrimSpace(after)
	}
	return label, reason, true
}

func skipNote(label, reason string) string {
	if reason == "" {
		return label
	}
	return label + ": " + reason
}

// ---------- adding and changing tasks ----------

// changesFrom reads the attributes a request sets. Each one only counts if it was sent.
func changesFrom(fields Body, onlyNonEmpty bool) taskwarrior.Changes {
	value := func(key string) *string {
		if !fields.Has(key) || (onlyNonEmpty && fields.TrimmedString(key) == "") {
			return nil
		}
		v := fields.String(key)
		return &v
	}
	return taskwarrior.Changes{
		Project: value("project"), Priority: value("priority"),
		Due: value("due"), Scheduled: value("scheduled"), Wait: value("wait"),
	}
}

// addTask: {"description", "project", "priority", "due", "scheduled", "wait", "recur", "tags"}.
func (s *Server) addTask(w http.ResponseWriter, r *http.Request, body Body) {
	description := body.TrimmedString("description")
	if description == "" || len([]rune(description)) > 300 {
		writeError(w, http.StatusBadRequest, "description required")
		return
	}
	changes := changesFrom(body, true)
	changes.Recur = body.TrimmedString("recur")
	changes.AddTags = body.Strings("tags")
	if changes.Recur != "" && changes.Due == nil {
		writeError(w, http.StatusBadRequest, "a repeating task needs a due date")
		return
	}
	seriesArgs, dateArgs, err := changes.Args()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	args := append(append(append([]string{"add"}, seriesArgs...), dateArgs...), "--", description)
	result := s.Tasks.Run(args...)
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// modifyTask: {"uuid", "fields": {...}, "scope": "all"?, "templates": [...]}.
// With scope "all" on a repeating task, the name, project, tags and priority change for the
// whole series (all its templates); dates always change on this copy only, since changing
// them on the series would give every pending copy the same date.
func (s *Server) modifyTask(w http.ResponseWriter, r *http.Request, body Body) {
	uuid := body.String("uuid")
	fields, isObject := body.Object("fields")
	if !taskwarrior.IsUUID(uuid) || !isObject {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	changes := changesFrom(fields, false)
	changes.AddTags, changes.RemoveTags = fields.Strings("tags_add"), fields.Strings("tags_remove")
	seriesArgs, dateArgs, err := changes.Args()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	description := ""
	if fields.Has("description") {
		description = text.Truncate(text.Collapse(fields.String("description")), 300)
		if description == "" {
			writeError(w, http.StatusBadRequest, "description can't be empty")
			return
		}
	}
	task, found := s.Tasks.Find(uuid)
	if !found {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if fields.IsSet("end") { // when a finished task was finished
		end := fields.TrimmedString("end")
		if !task.IsFinished() || end == "" || !taskwarrior.IsDateValue(end) {
			writeError(w, http.StatusBadRequest, "can only set the completion date of a finished task")
			return
		}
		dateArgs = append(dateArgs, "end:"+end)
	}

	// A repeating task can be several series (Mon + Wed + Fri); the page sends all their templates.
	templates := taskwarrior.FilterUUIDs(body.Strings("templates"))
	if task.IsRecurringTemplate() && !slices.Contains(templates, uuid) {
		templates = append(templates, uuid)
	}
	if len(templates) == 0 && task.Parent() != "" {
		templates = []string{task.Parent()}
	}
	descriptionArgs := []string{}
	if description != "" {
		descriptionArgs = []string{"--", description}
	}
	wholeSeries := append(append([]string{}, seriesArgs...), descriptionArgs...)

	var steps [][]string
	switch {
	case body.String("scope") == "all" && len(templates) > 0:
		if len(wholeSeries) > 0 {
			for _, template := range templates {
				steps = append(steps, append([]string{"rc.recurrence.confirmation=yes", template, "modify"}, wholeSeries...))
			}
		}
		if len(dateArgs) > 0 && !task.IsRecurringTemplate() {
			steps = append(steps, append([]string{uuid, "modify"}, dateArgs...))
		}
	case len(wholeSeries) > 0 || len(dateArgs) > 0:
		step := append([]string{uuid, "modify"}, seriesArgs...)
		step = append(append(step, dateArgs...), descriptionArgs...)
		steps = append(steps, step)
	}
	for _, step := range steps {
		if result := s.Tasks.Run(step...); !result.OK() {
			writeOutcome(w, false, result.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeOK(w)
}

// completeTaskAt: {"uuid", "end": "2026-10-20T14:30", "minutes": 30?} completes a task as of
// that time and, with minutes, logs how long it took in Timewarrior.
func (s *Server) completeTaskAt(w http.ResponseWriter, r *http.Request, body Body) {
	uuid, end := body.String("uuid"), body.String("end")
	if !taskwarrior.IsUUID(uuid) || !localDateTime.MatchString(end) {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	minutes, ok := minutesTaken(body)
	if !ok {
		writeError(w, http.StatusBadRequest, "minutes must be between 1 and 1440")
		return
	}
	task, found := s.Tasks.Find(uuid)
	if !found {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if result := s.Tasks.Run(uuid, "done", "end:"+end); !result.OK() {
		writeOutcome(w, false, result.Error(), http.StatusInternalServerError)
		return
	}
	warning := ""
	if minutes > 0 {
		stop, _ := time.ParseInLocation("2006-01-02T15:04", end, time.Local)
		start := stop.Add(-time.Duration(minutes) * time.Minute)
		args := append([]string{"track", start.Format("2006-01-02T15:04"), "-", stop.Format("2006-01-02T15:04")}, task.TimewarriorTags()...)
		if result := s.Time.Run(args...); !result.OK() {
			warning = "Completed, but the time wasn't logged: " + result.FirstErrorLine("timew error")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warning})
}

// minutesTaken reads "minutes": nothing, null, "" or 0 means none; otherwise a whole number 1-1440.
func minutesTaken(body Body) (minutes int, ok bool) {
	if !body.IsSet("minutes") || body.String("minutes") == "" {
		return 0, true
	}
	if n, isNumber := body.Number("minutes"); isNumber && n == 0 {
		return 0, true
	}
	minutes, isInt := body.Int("minutes")
	return minutes, isInt && minutes > 0 && minutes <= 24*60
}

// runOnTask: {"uuid"} runs `task <uuid> done|start|stop`.
func (s *Server) runOnTask(action string) func(http.ResponseWriter, *http.Request, Body) {
	return func(w http.ResponseWriter, r *http.Request, body Body) {
		uuid := body.String("uuid")
		if !taskwarrior.IsUUID(uuid) {
			writeError(w, http.StatusBadRequest, "bad uuid")
			return
		}
		result := s.Tasks.Run(uuid, action)
		writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) reopenTask(w http.ResponseWriter, r *http.Request, body Body) {
	uuid := body.String("uuid")
	if !taskwarrior.IsUUID(uuid) {
		writeError(w, http.StatusBadRequest, "bad uuid")
		return
	}
	result := s.Tasks.Run(uuid, "modify", "status:pending")
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request, body Body) {
	result := s.Tasks.Run("undo")
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// annotate: {"uuid", "text"} adds or removes a note on a task.
func (s *Server) annotate(action string) func(http.ResponseWriter, *http.Request, Body) {
	return func(w http.ResponseWriter, r *http.Request, body Body) {
		uuid, note := body.String("uuid"), text.Truncate(text.Collapse(body.String("text")), 500)
		if !taskwarrior.IsUUID(uuid) || note == "" {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		result := s.Tasks.Run(uuid, action, "--", note)
		writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
	}
}

// taskUUIDs reads "uuids": 1-500 task uuids, all valid.
func taskUUIDs(body Body) ([]string, bool) {
	uuids := body.Strings("uuids")
	return uuids, len(uuids) <= 500 && taskwarrior.AllUUIDs(uuids)
}

func (s *Server) deleteTasks(w http.ResponseWriter, r *http.Request, body Body) {
	uuids, ok := taskUUIDs(body)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad uuids")
		return
	}
	result := s.Tasks.Run(append(append([]string{"rc.bulk=0"}, uuids...), "delete")...)
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// skipTasks: {"uuids", "note", "kind": "skip"|"dismiss"} notes the reason, then deletes the
// tasks. Deleted tasks keep their notes, so History can show why.
func (s *Server) skipTasks(w http.ResponseWriter, r *http.Request, body Body) {
	uuids, ok := taskUUIDs(body)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad uuids")
		return
	}
	label := "Skipped"
	if body.String("kind") == "dismiss" {
		label = "Dismissed"
	}
	reason := text.Truncate(text.Collapse(body.String("note")), 300)
	bulk := append([]string{"rc.bulk=0"}, uuids...)
	result := s.Tasks.Run(append(append([]string{}, bulk...), "annotate", "--", skipNote(label, reason))...)
	if result.OK() {
		result = s.Tasks.Run(append(bulk, "delete")...)
	}
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// changeSkipReason: {"uuids", "note"} rewrites the reason on skipped tasks. It goes through
// export and import, so the note keeps its date (History groups by it).
func (s *Server) changeSkipReason(w http.ResponseWriter, r *http.Request, body Body) {
	uuids, ok := taskUUIDs(body)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad uuids")
		return
	}
	reason := text.Truncate(text.Collapse(body.String("note")), 300)
	tasks, result := s.Tasks.Tasks(uuids...)
	if !result.OK() {
		writeError(w, http.StatusInternalServerError, orDefault(result.Error(), "task export failed"))
		return
	}
	var changed []taskwarrior.Task
	for _, task := range tasks {
		for i, note := range task.Annotations() {
			if label, _, isSkip := parseSkipNote(note.Description); isSkip {
				task.SetAnnotationText(i, skipNote(label, reason))
				changed = append(changed, task)
				break
			}
		}
	}
	if len(changed) == 0 {
		writeError(w, http.StatusBadRequest, "no skip reason found on those tasks")
		return
	}
	imported, _ := json.Marshal(changed)
	result = s.Tasks.RunWithInput(string(imported), "import", "-")
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

// ---------- repeat schedules ----------

// replaceSchedule: {"source": uuid, "templates": [...], "rule": {...}} replaces a task's repeat
// schedule. The new series are created first; the old ones (and their pending copies) are
// deleted after, so a failure leaves the old schedule in place.
func (s *Server) replaceSchedule(w http.ResponseWriter, r *http.Request, body Body) {
	source := body.String("source")
	if !taskwarrior.IsUUID(source) {
		writeError(w, http.StatusBadRequest, "bad source")
		return
	}
	templates := taskwarrior.FilterUUIDs(body.Strings("templates"))
	task, found := s.Tasks.Find(source)
	if !found {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	rule, err := taskwarrior.ParseRepeatRule(body.Raw("rule"), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	series, err := rule.Series(time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
		result := s.Tasks.Run(args...)
		uuid := newUUID.FindString(result.Stdout)
		if !result.OK() || uuid == "" {
			if len(created) > 0 { // roll back the half-made schedule
				s.Tasks.Run(append(append([]string{"rc.bulk=0", "rc.recurrence.confirmation=yes"}, created...), "delete")...)
			}
			writeOutcome(w, false, orDefault(result.Error(), "couldn't create the new schedule"), http.StatusInternalServerError)
			return
		}
		created = append(created, uuid)
	}
	old := templates
	if task.Parent() == "" && !task.IsRecurringTemplate() {
		old = append(old, source) // a one-off task that became repeating
	}
	if len(old) > 0 {
		s.Tasks.Run(append(append([]string{"rc.bulk=0", "rc.recurrence.confirmation=yes"}, old...), "delete")...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "series": len(created)})
}

// stopRepeating: {"templates"} deletes the series and their pending copies.
func (s *Server) stopRepeating(w http.ResponseWriter, r *http.Request, body Body) {
	templates := taskwarrior.FilterUUIDs(body.Strings("templates"))
	if len(templates) == 0 {
		writeError(w, http.StatusBadRequest, "no series given")
		return
	}
	result := s.Tasks.Run(append(append([]string{"rc.bulk=0", "rc.recurrence.confirmation=yes"}, templates...), "delete")...)
	writeOutcome(w, result.OK(), result.Error(), http.StatusInternalServerError)
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

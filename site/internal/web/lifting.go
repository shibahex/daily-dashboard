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

	"dashboard/internal/lifting"
	"dashboard/internal/problem"
	"dashboard/internal/store"
	"dashboard/internal/text"
	"dashboard/internal/timewarrior"
)

// The Lifting tab: routines (days of planned exercises), the workout in progress with its rest
// timer, and what was lifted before. The data is lifting.go's; the clocks tick in app.js.

const liftingChanged = "lifting-changed"

type liftingPageView struct {
	Unit     string
	Workout  workoutView
	Routines routinesView
	Progress progressView
	History  liftingHistoryView
}

func (ui *UI) liftingPage(w http.ResponseWriter, r *http.Request) {
	ui.seedLifting(r)
	data := ui.ws(r).Lifting.Load()
	view := liftingPageView{Unit: data.Unit, Workout: workoutViewOf(data, time.Now()), Routines: routinesViewOf(data),
		Progress: progressViewOf(data, r.URL.Query().Get("exercise")), History: liftingHistoryOf(data, 0)}
	ui.renderPage(r, w, "lifting", "Lifting", "lifting", view)
}

// seedLifting copies the plan's training sessions in as a first routine, once. A session whose
// title is also a pending task's gets linked to it, so finishing that workout ticks the task.
func (ui *UI) seedLifting(r *http.Request) {
	ws := ui.ws(r)
	if ws.Lifting.Load().Seeded {
		return
	}
	plan, err := ws.Store.LoadPlan()
	if err != nil {
		return
	}
	tasks := map[string]string{}
	for _, description := range ui.pendingDescriptions(r) {
		tasks[strings.ToLower(description)] = description
	}
	ws.Lifting.Update(func(d *lifting.Data) error {
		if d.Seeded {
			return nil
		}
		d.Seeded = true
		if len(d.Routines) > 0 {
			return nil
		}
		routine := lifting.Routine{Name: "My plan"}
		for _, key := range planSessionOrder(plan) {
			session := plan.Sessions[key]
			day := lifting.Day{Name: session.Title, Task: tasks[strings.ToLower(strings.TrimSpace(session.Title))]}
			for _, exercise := range session.Exercises {
				day.Exercises = append(day.Exercises, lifting.ParsePlanned(exercise[0], exercise[1]))
			}
			routine.Days = append(routine.Days, day)
		}
		if len(routine.Days) > 0 {
			d.SaveRoutine(routine)
		}
		return nil
	})
}

// planSessionOrder is the plan's sessions that have exercises: in week order, then the rest.
func planSessionOrder(plan store.Plan) []string {
	var keys []string
	seen := map[string]bool{}
	add := func(key string) {
		if session, ok := plan.Sessions[key]; ok && !seen[key] && len(session.Exercises) > 0 {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for _, day := range store.Weekdays {
		add(plan.Schedule[day])
	}
	var rest []string
	for key := range plan.Sessions {
		rest = append(rest, key)
	}
	sort.Strings(rest)
	for _, key := range rest {
		add(key)
	}
	return keys
}

// pendingDescriptions is every pending task's description, each once, sorted.
func (ui *UI) pendingDescriptions(r *http.Request) []string {
	pending, err := ui.ws(r).Tasks.Pending()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var descriptions []string
	for _, task := range pending {
		if d := task.Description(); d != "" && !seen[d] {
			seen[d] = true
			descriptions = append(descriptions, d)
		}
	}
	sort.Strings(descriptions)
	return descriptions
}

// ---------- the workout ----------

type workoutView struct {
	Going     bool
	ID        string
	Title     string
	Task      string
	Started   time.Time
	Elapsed   string // the clock as first shown; app.js keeps it going
	RestUntil time.Time
	RestFor   int
	RestLeft  string
	Unit      string
	Exercises []exerciseView
	Names     []string // for the add-exercise box
	Sets      int
	Volume    string
	Plate     string // the rounding step of estimates, for app.js
	Note      string
}

type exerciseView struct {
	Index       int
	Name        string
	Plan        string // "3 × 8-12 · rest 2:00"
	Last        string // last time's sets
	Top, Bottom bool   // first and last, for the move buttons
	Sets        []setView
	Est         string // the best estimated one-rep max of its sets as typed (app.js keeps it current)
	Best        string // the best before this workout
	Record      bool   // a set done today beat Best
	Skipped     bool
	Why         string
}

type setView struct {
	Index   int
	Weight  string
	Reps    string
	Done    bool
	Record  bool
	Note    string
	Skipped bool
	Why     string
}

// setViewOf is a set as its row shows it.
func setViewOf(s int, set lifting.Set, unit string) setView {
	shown := setView{Index: s, Done: set.Done, Note: set.Note, Skipped: set.Skipped, Why: set.Why}
	if set.LB > 0 {
		shown.Weight = lifting.FormatShown(set.LB, unit)
	}
	if set.Reps > 0 {
		shown.Reps = strconv.Itoa(set.Reps)
	}
	return shown
}

func workoutViewOf(data lifting.Data, now time.Time) workoutView {
	w := data.Active
	if w == nil {
		return workoutView{Unit: data.Unit}
	}
	view := workoutView{Going: true, ID: w.ID, Title: w.Title, Task: w.Task, Started: w.Started,
		Elapsed: clockText(now.Sub(w.Started)), Unit: data.Unit, Names: data.ExerciseNames(),
		Sets: w.DoneSets(), Volume: formatNumber(lifting.Shown(w.Volume(), data.Unit)),
		Plate: strconv.FormatFloat(lifting.PlateStep(data.Unit), 'f', -1, 64), Note: w.Note}
	if w.RestUntil.After(now) {
		view.RestUntil, view.RestFor, view.RestLeft = w.RestUntil, w.RestFor, clockText(w.RestUntil.Sub(now))
	}
	records := data.Records(*w)
	for e, ex := range w.Exercises {
		exercise := exerciseView{Index: e, Name: ex.Name, Plan: planText(len(ex.Sets), ex.Target, ex.Rest),
			Last: setsText(data.LastSets(ex.Name), data.Unit), Top: e == 0, Bottom: e == len(w.Exercises)-1,
			Skipped: ex.Skipped, Why: ex.Why}
		if best := data.Best(ex.Name, time.Time{}); best > 0 {
			exercise.Best = lifting.FormatPlate(best, data.Unit)
		}
		estimate := 0.0
		for s, set := range ex.Sets {
			shown := setViewOf(s, set, data.Unit)
			shown.Record = records[[2]int{e, s}]
			exercise.Record = exercise.Record || shown.Record
			if !set.Skipped {
				estimate = math.Max(estimate, lifting.E1RM(set))
			}
			exercise.Sets = append(exercise.Sets, shown)
		}
		if estimate > 0 {
			exercise.Est = lifting.FormatPlate(estimate, data.Unit)
		}
		view.Exercises = append(view.Exercises, exercise)
	}
	return view
}

// clockText is a duration as a clock: 4:05, 1:02:09.
func clockText(d time.Duration) string {
	s := max(0, int(math.Round(d.Seconds())))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func planText(sets int, target string, rest int) string {
	text := strconv.Itoa(sets) + " × " + target
	if target == "" {
		text = strconv.Itoa(sets) + " " + plural(sets, "set", "sets")
	}
	if rest > 0 {
		text += " · rest " + clockText(time.Duration(rest)*time.Second)
	}
	return text
}

// setsText is sets as "135 × 8, 135 × 8, 145 × 6" (BW for bodyweight).
func setsText(sets []lifting.Set, unit string) string {
	parts := make([]string, len(sets))
	for i, set := range sets {
		weight := "BW"
		if set.LB > 0 {
			weight = lifting.FormatShown(set.LB, unit)
		}
		parts[i] = fmt.Sprintf("%s × %d", weight, set.Reps)
	}
	return strings.Join(parts, ", ")
}

// setsWithNotes is setsText with each set's note: "135 × 8 (felt easy), 135 × 8".
func setsWithNotes(sets []lifting.Set, unit string) string {
	parts := make([]string, len(sets))
	for i, set := range sets {
		if set.Skipped {
			parts[i] = "skipped"
			if set.Why != "" {
				parts[i] += " (" + set.Why + ")"
			}
			continue
		}
		parts[i] = setsText([]lifting.Set{set}, unit)
		if set.Note != "" {
			parts[i] += " (" + set.Note + ")"
		}
	}
	return strings.Join(parts, ", ")
}

func (ui *UI) workoutCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "lift-workout", workoutViewOf(ui.ws(r).Lifting.Load(), time.Now()))
}

// startWorkout begins a routine's day (or an empty workout) and opens the Lifting tab.
func (ui *UI) startWorkout(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error {
		if r.FormValue("routine") == "" {
			return d.StartEmpty(now)
		}
		return d.Start(r.FormValue("routine"), r.FormValue("day"), now)
	})
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("HX-Location", "/lifting")
	nothing(w)
}

var setField = regexp.MustCompile(`^([wrn])(\d+)\.(\d+)$`)

// typedSets reads the sets' inputs (w0.1 is exercise 0, set 1's weight; r0.1 its reps; n0.1 its note).
func typedSets(r *http.Request, unit string) map[[2]int]lifting.Typed {
	r.ParseForm()
	typed := map[[2]int]lifting.Typed{}
	for name, values := range r.PostForm {
		m := setField.FindStringSubmatch(name)
		if m == nil || len(values) == 0 {
			continue
		}
		e, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		key, value, entry := [2]int{e, s}, strings.TrimSpace(values[0]), typed[[2]int{e, s}]
		if m[1] == "n" {
			note := values[0]
			entry.Note = &note
		} else if m[1] == "w" {
			if value == "" {
				zero := 0.0
				entry.LB = &zero
			} else if n, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64); err == nil {
				lb := lifting.FromShown(n, unit)
				entry.LB = &lb
			}
		} else if n, err := strconv.Atoi(value); err == nil || value == "" {
			entry.Reps = &n
		}
		typed[key] = entry
	}
	return typed
}

// changeFrom reads the button that was pressed: "done:0:1", "add-set:0", "rest:30"...
func changeFrom(r *http.Request) (lifting.Change, bool) {
	parts := strings.Split(r.FormValue("op"), ":")
	change := lifting.Change{Op: parts[0], Name: strings.TrimSpace(r.FormValue("new_exercise"))}
	if len(parts) > 1 {
		change.Why = r.FormValue("why" + parts[1]) // an exercise's skip reason: why0, why1...
	}
	if change.Op == "" && change.Name != "" { // Enter in the add-exercise box
		change.Op = "add-exercise"
	}
	if change.Op == "" {
		return change, false
	}
	numbers := make([]int, 0, 2)
	for _, part := range parts[1:] {
		n, err := strconv.Atoi(part)
		if err != nil {
			return change, false
		}
		numbers = append(numbers, n)
	}
	if change.Op == "rest" && len(numbers) == 1 {
		change.Seconds = clampInt(numbers[0], -600, 600)
	}
	if len(numbers) > 0 {
		change.Exercise = numbers[0]
	}
	if len(numbers) > 1 {
		change.Set = numbers[1]
	}
	return change, true
}

func clampInt(n, low, high int) int { return max(low, min(n, high)) }

// changeWorkout keeps what was typed, makes the change of the button pressed (if any) and
// answers with the workout card.
func (ui *UI) changeWorkout(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	now := time.Now()
	err := ws.Lifting.Update(func(d *lifting.Data) error {
		if d.Active == nil || d.Active.ID != r.FormValue("workout") {
			return problem.Conflictf("that workout has ended: reload the page")
		}
		d.SetTyped(typedSets(r, d.Unit))
		if r.PostForm.Has("workout_note") {
			d.Active.Note = text.Truncate(text.Collapse(r.PostForm.Get("workout_note")), 500)
		}
		if change, ok := changeFrom(r); ok {
			return d.Apply(change, now)
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	if r.URL.Query().Get("quiet") == "1" { // just keeping what was typed
		nothing(w)
		return
	}
	ui.render(w, "lift-workout", workoutViewOf(ws.Lifting.Load(), now))
}

type finishView struct {
	Note     string // written during the workout
	Took     string
	Task     string
	TaskDue  bool
	Sets     int
	NoneDone bool
}

func (ui *UI) finishPanel(w http.ResponseWriter, r *http.Request) {
	data := ui.ws(r).Lifting.Load()
	if data.Active == nil {
		fail(w, problem.Conflictf("no workout is going"))
		return
	}
	minutes := max(1, int(math.Round(time.Since(data.Active.Started).Minutes())))
	view := finishView{Took: minutesText(min(minutes, 24*60)), Note: data.Active.Note, Task: data.Active.Task, Sets: data.Active.DoneSets()}
	view.NoneDone = view.Sets == 0
	if view.Task != "" {
		_, view.TaskDue = ui.dueTaskNamed(r, view.Task)
	}
	ui.render(w, "lift-finish", view)
}

// dueTaskNamed is the due (or overdue) task with this description: the copy that Done ticks.
func (ui *UI) dueTaskNamed(r *http.Request, description string) (string, bool) {
	data, err := ui.loadTaskData(r)
	if err != nil {
		return "", false
	}
	for _, row := range data.pendingRows() {
		if (row.Bucket == "overdue" || row.Bucket == "today") && strings.EqualFold(row.Description(), description) {
			return row.UUID(), true
		}
	}
	return "", false
}

// finishWorkout saves the workout, then ticks its task (which logs the time with the task's
// tags) or, without one due, logs the time as "Lifting" plus the workout's name.
func (ui *UI) finishWorkout(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	minutes, err := parseTook(r.FormValue("took"))
	if err != nil {
		fail(w, err)
		return
	}
	end := time.Now().Truncate(time.Minute)
	var finished lifting.Workout
	err = ws.Lifting.Update(func(d *lifting.Data) error {
		var err error
		finished, err = d.Finish(end, minutes, r.FormValue("note"))
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	message := fmt.Sprintf("Workout saved ✓ · %d %s", finished.DoneSets(), plural(finished.DoneSets(), "set", "sets"))
	warning := ""
	uuid, due := "", false
	if finished.Task != "" {
		uuid, due = ui.dueTaskNamed(r, finished.Task)
	}
	if due {
		if warning, err = ws.Tasks.CompleteAt(uuid, end, minutes); err != nil {
			warning = "Saved, but the task wasn't ticked: " + err.Error()
		} else {
			message += " · task ticked"
		}
	} else if minutes > 0 {
		start := end.Add(-time.Duration(minutes) * time.Minute)
		if err := ws.Time.Track(start.Format(localMinuteLayout), end.Format(localMinuteLayout),
			timewarrior.TaskTags([]string{"Lifting", finished.Title})); err != nil {
			warning = "Saved, but the time wasn't logged: " + err.Error()
		}
	}
	if minutes > 0 && warning == "" {
		message += " · " + minutesText(minutes) + " logged"
	}
	if warning != "" {
		announce(w, &notice{Message: warning, Bad: true}, liftingChanged, tasksChanged, timeChanged)
	} else {
		announce(w, say(message), liftingChanged, tasksChanged, timeChanged)
	}
	nothing(w)
}

func (ui *UI) discardWorkout(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error { d.Discard(); return nil }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Workout discarded"), liftingChanged)
	nothing(w)
}

// ---------- routines ----------

type routinesView struct {
	Routines []lifting.Routine
	Going    bool   // a workout is in progress: the Start buttons wait
	GoingDay string // the day it came from
}

func routinesViewOf(data lifting.Data) routinesView {
	view := routinesView{Routines: data.Routines, Going: data.Active != nil}
	if data.Active != nil {
		view.GoingDay = data.Active.Day
	}
	return view
}

func (ui *UI) routinesCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "lift-routines", routinesViewOf(ui.ws(r).Lifting.Load()))
}

type routineEditorView struct {
	Routine lifting.Routine
	New     bool
	Tasks   []string // pending tasks, to link a day to
	Names   []string // exercises, to pick from
}

func (ui *UI) routineEditor(w http.ResponseWriter, r *http.Request) {
	data := ui.ws(r).Lifting.Load()
	view := routineEditorView{Tasks: ui.pendingDescriptions(r), Names: data.ExerciseNames()}
	if id := r.PathValue("id"); id == "new" {
		view.New = true
		view.Routine = lifting.Routine{Name: "", Days: []lifting.Day{{Exercises: []lifting.Planned{{Sets: 3, Reps: "10", Rest: lifting.DefaultRest}}}}}
	} else if routine, ok := data.Routine(id); ok {
		view.Routine = *routine
	} else {
		fail(w, problem.NotFoundf("that routine is gone"))
		return
	}
	ui.render(w, "lift-routine-editor", view)
}

// routineFromForm reads the editor: name, then d0.name, d0.task, d0.id and each exercise's
// d0.e1.name, .sets, .reps and .rest.
func routineFromForm(r *http.Request) lifting.Routine {
	r.ParseForm()
	routine := lifting.Routine{Name: r.PostForm.Get("name")}
	for d := 0; r.PostForm.Has(fmt.Sprintf("d%d.name", d)); d++ {
		prefix := fmt.Sprintf("d%d.", d)
		day := lifting.Day{ID: r.PostForm.Get(prefix + "id"), Name: r.PostForm.Get(prefix + "name"), Task: r.PostForm.Get(prefix + "task")}
		for e := 0; r.PostForm.Has(fmt.Sprintf("%se%d.name", prefix, e)); e++ {
			field := func(name string) string {
				return strings.TrimSpace(r.PostForm.Get(fmt.Sprintf("%se%d.%s", prefix, e, name)))
			}
			sets, _ := strconv.Atoi(field("sets"))
			rest, _ := strconv.Atoi(field("rest"))
			day.Exercises = append(day.Exercises, lifting.Planned{Name: field("name"), Sets: sets, Reps: field("reps"), Rest: rest})
		}
		routine.Days = append(routine.Days, day)
	}
	return routine
}

// editRoutine applies one of the editor's buttons to the routine as typed: add-day,
// remove-day:1, up-day:1, down-day:1, add-ex:1, remove-ex:1:2, up-ex:1:2, down-ex:1:2.
func editRoutine(routine *lifting.Routine, op string) {
	parts := strings.Split(op, ":")
	n := make([]int, len(parts)-1)
	for i, part := range parts[1:] {
		n[i], _ = strconv.Atoi(part)
	}
	days := routine.Days
	inDays := len(n) > 0 && n[0] >= 0 && n[0] < len(days)
	switch {
	case parts[0] == "add-day":
		routine.Days = append(days, lifting.Day{Exercises: []lifting.Planned{{Sets: 3, Reps: "10", Rest: lifting.DefaultRest}}})
	case parts[0] == "remove-day" && inDays:
		routine.Days = append(days[:n[0]], days[n[0]+1:]...)
	case (parts[0] == "up-day" || parts[0] == "down-day") && inDays:
		swapAt(days, n[0], parts[0] == "up-day")
	case parts[0] == "add-ex" && inDays:
		days[n[0]].Exercises = append(days[n[0]].Exercises, lifting.Planned{Sets: 3, Reps: "10", Rest: lifting.DefaultRest})
	case len(n) == 2 && inDays && n[1] >= 0 && n[1] < len(days[n[0]].Exercises):
		exercises := days[n[0]].Exercises
		switch parts[0] {
		case "remove-ex":
			days[n[0]].Exercises = append(exercises[:n[1]], exercises[n[1]+1:]...)
		case "up-ex", "down-ex":
			swapAt(exercises, n[1], parts[0] == "up-ex")
		}
	}
}

func swapAt[T any](items []T, i int, up bool) {
	j := i + 1
	if up {
		j = i - 1
	}
	if j >= 0 && j < len(items) {
		items[i], items[j] = items[j], items[i]
	}
}

// saveRoutine applies an editor button (answering with the editor) or, for Save, saves.
func (ui *UI) saveRoutine(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	routine := routineFromForm(r)
	id := r.PathValue("id")
	if id != "new" {
		routine.ID = id
	}
	if op := r.FormValue("op"); op != "" {
		editRoutine(&routine, op)
		data := ws.Lifting.Load()
		ui.render(w, "lift-routine-editor", routineEditorView{Routine: routine, New: id == "new",
			Tasks: ui.pendingDescriptions(r), Names: data.ExerciseNames()})
		return
	}
	err := ws.Lifting.Update(func(d *lifting.Data) error {
		_, err := d.SaveRoutine(routine)
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Routine saved"), liftingChanged, tasksChanged)
	ui.render(w, "lift-routines", routinesViewOf(ws.Lifting.Load()))
}

func (ui *UI) deleteRoutine(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	if err := ws.Lifting.Update(func(d *lifting.Data) error { d.DeleteRoutine(r.PathValue("id")); return nil }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Routine deleted"), tasksChanged)
	ui.render(w, "lift-routines", routinesViewOf(ws.Lifting.Load()))
}

// ---------- progress: one exercise over time ----------

type progressView struct {
	Names    []string
	Name     string
	Unit     string
	Count    int
	Best     string // best estimated one-rep max
	Heaviest string
	Trend    *sparkline // each session's best estimated one-rep max (in LB: the shown unit)
	Sessions []progressSession
}

type progressSession struct {
	Day    time.Time
	Title  string
	Groups []setGroup
	Best   string
	Record bool
}

// setGroup is a run of sets at one weight: Reps is "4 × 5" when every set had the same reps, else "5/5/5/4".
type setGroup struct{ Weight, Reps string }

// groupSets folds back-to-back sets at the same weight into one group.
func groupSets(sets []lifting.Set, unit string) []setGroup {
	var groups []setGroup
	for i := 0; i < len(sets); {
		j := i + 1
		for j < len(sets) && sets[j].LB == sets[i].LB {
			j++
		}
		weight, reps, same := "BW", make([]string, 0, j-i), true
		if sets[i].LB > 0 {
			weight = lifting.FormatShown(sets[i].LB, unit)
		}
		for _, set := range sets[i:j] {
			reps = append(reps, strconv.Itoa(set.Reps))
			same = same && set.Reps == sets[i].Reps
		}
		group := setGroup{Weight: weight, Reps: strings.Join(reps, "/")}
		if same && len(reps) > 1 {
			group.Reps = fmt.Sprintf("%d × %d", len(reps), sets[i].Reps)
		}
		groups = append(groups, group)
		i = j
	}
	return groups
}

func progressViewOf(data lifting.Data, name string) progressView {
	view := progressView{Names: data.LoggedNames(), Name: name, Unit: data.Unit}
	if view.Name == "" && len(data.Workouts) > 0 {
		last := data.Workouts[len(data.Workouts)-1]
		view.Name = last.Exercises[0].Name
	}
	history := data.History(view.Name)
	view.Count = len(history)
	best, heaviest := 0.0, 0.0
	var trend []trendValue
	for _, session := range history {
		best, heaviest = math.Max(best, session.Best), math.Max(heaviest, session.Heavy)
		if session.Best > 0 {
			trend = append(trend, trendValue{session.Day, lifting.Shown(session.Best, data.Unit)})
		}
	}
	if best > 0 {
		view.Best, view.Heaviest = lifting.FormatPlate(best, data.Unit), lifting.FormatShown(heaviest, data.Unit)
	}
	view.Trend = trendLine(trend)
	for i := len(history) - 1; i >= 0 && len(view.Sessions) < 12; i-- {
		session := history[i]
		shown := progressSession{Day: session.Day, Title: session.Title, Groups: groupSets(session.Sets, data.Unit), Record: session.Record}
		if session.Best > 0 {
			shown.Best = lifting.FormatPlate(session.Best, data.Unit)
		}
		view.Sessions = append(view.Sessions, shown)
	}
	return view
}

type trendValue struct {
	Day   time.Time
	Value float64
}

// trendLine draws values as an SVG path (300×60), or nil with fewer than two.
func trendLine(values []trendValue) *sparkline {
	if len(values) < 2 {
		return nil
	}
	low, high := values[0].Value, values[0].Value
	for _, v := range values {
		low, high = math.Min(low, v.Value), math.Max(high, v.Value)
	}
	pad := math.Max((high-low)*0.1, 1)
	low, high = low-pad, high+pad
	const width, height = 300.0, 60.0
	var path strings.Builder
	trend := &sparkline{}
	for i, v := range values {
		x := float64(i) / float64(len(values)-1) * width
		y := height - (v.Value-low)/(high-low)*height
		command := "L"
		if i == 0 {
			command = "M"
		}
		fmt.Fprintf(&path, "%s%.1f,%.1f ", command, x, y)
		trend.Points = append(trend.Points, sparkPoint{Left: x / width * 100, Top: y / height * 100, Day: v.Day, LB: math.Round(v.Value)})
	}
	trend.Path = strings.TrimSpace(path.String())
	trend.From, trend.To = values[0].Day, values[len(values)-1].Day
	return trend
}

func (ui *UI) progressCard(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "lift-progress", progressViewOf(ui.ws(r).Lifting.Load(), r.URL.Query().Get("exercise")))
}

// ---------- history ----------

type liftingHistoryView struct {
	Unit     string
	Workouts []liftingHistoryRow
	More     int
	Limit    int
}

type liftingHistoryRow struct {
	ID        string
	Day       time.Time
	Title     string
	Took      string
	Sets      int
	Volume    string
	Records   int
	Note      string
	Exercises []liftingHistoryExercise
}

type liftingHistoryExercise struct{ Name, Sets string }

func liftingHistoryOf(data lifting.Data, limit int) liftingHistoryView {
	if limit <= 0 {
		limit = 15
	}
	view := liftingHistoryView{Unit: data.Unit, Limit: limit}
	for i := len(data.Workouts) - 1; i >= 0 && len(view.Workouts) < limit; i-- {
		workout := data.Workouts[i]
		row := liftingHistoryRow{ID: workout.ID, Day: workout.Started, Title: workout.Title, Note: workout.Note,
			Took: formatDuration(workout.Duration(workout.Ended)), Sets: workout.DoneSets(),
			Volume: formatNumber(lifting.Shown(workout.Volume(), data.Unit)), Records: len(data.Records(workout))}
		for _, ex := range workout.Exercises {
			sets := setsWithNotes(ex.Sets, data.Unit)
			if ex.Skipped {
				sets = "skipped"
				if ex.Why != "" {
					sets += " (" + ex.Why + ")"
				}
			}
			row.Exercises = append(row.Exercises, liftingHistoryExercise{ex.Name, sets})
		}
		view.Workouts = append(view.Workouts, row)
	}
	view.More = len(data.Workouts) - len(view.Workouts)
	return view
}

func (ui *UI) liftingHistoryCard(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ui.render(w, "lift-history", liftingHistoryOf(ui.ws(r).Lifting.Load(), min(limit, 1000)))
}

func (ui *UI) deleteWorkout(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error { d.DeleteWorkout(r.PathValue("id")); return nil }); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Workout deleted"), liftingChanged)
	nothing(w)
}

// setLiftingUnit switches between lb and kg, and redraws the tab in the new unit.
func (ui *UI) setLiftingUnit(w http.ResponseWriter, r *http.Request) {
	unit := r.FormValue("unit")
	if unit != "lb" && unit != "kg" {
		fail(w, problem.Invalidf("pick lb or kg"))
		return
	}
	if err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error { d.Unit = unit; return nil }); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	nothing(w)
}

// ---------- on the task lists ----------

// workoutLink is a due task's way into its routine day ("Start workout").
type workoutLink struct {
	Routine, Day string
	Going        bool // that day's workout is the one in progress
}

// linkWorkouts gives each task row linked to a routine day its Start workout button.
func (ui *UI) linkWorkouts(r *http.Request, rows []taskRow) {
	data := ui.ws(r).Lifting.Load()
	for i := range rows {
		if routine, day, ok := data.DayForTask(rows[i].Description()); ok {
			rows[i].Workout = &workoutLink{Routine: routine.ID, Day: day.ID, Going: data.Active != nil && data.Active.Day == day.ID}
		}
	}
}

// ---------- strength: the Today card's chart and the 1RM calculator ----------

// strengthView is the Strength card: the logged weights of the lifts picked, as one line each
// (in a fixed colour slot, so a lift keeps its colour), what they are now, and the picker.
type strengthView struct {
	Unit     string
	Measure  string // "heaviest" or "e1rm"
	Range    string
	Ranges   []choice
	Series   []strengthSeries
	Choices  []liftChoice
	Full     bool // every colour slot is taken
	Chart    *strengthChart
	Body     string // the latest weigh-in, in Unit ("" without one)
	HasLifts bool
	PickOpen bool // answering the picker: keep it open
	Calc     calcView
}

type strengthSeries struct {
	Slot   int // 1 to lifting.ChartSlots: the colour
	Name   string
	Latest string // the last session's number
	Ratio  string // "1.45" × bodyweight, for the best number in the range
	Change string // "▲ 10" over the range
	Path   string
	Points []strengthPoint
}

type strengthPoint struct {
	Left, Top float64
	Day       time.Time
	Value     string // what's plotted
	Set       string // the session's best set: "145 × 10"
	E1RM      string
}

type liftChoice struct {
	Name     string
	Slot     int // 0: not shown
	Sessions int
}

// strengthChart is what the lines share: one axis (same measure, same unit) and the dates.
type strengthChart struct {
	Ticks    []strengthTick
	From, To time.Time
}

type strengthTick struct {
	Top   float64
	Label string
}

var strengthRanges = []choice{{"3m", "3M"}, {"6m", "6M"}, {"1y", "1Y"}, {"all", "All"}}

// strengthMeasure is what the chart plots: the estimated one-rep max, unless "heaviest" was picked.
func strengthMeasure(saved string) string {
	if saved == "heaviest" {
		return saved
	}
	return "e1rm"
}

func rangeStart(id string, now time.Time) time.Time {
	switch id {
	case "3m":
		return now.AddDate(0, -3, 0)
	case "1y":
		return now.AddDate(-1, 0, 0)
	case "all":
		return time.Time{}
	}
	return now.AddDate(0, -6, 0)
}

func (ui *UI) strengthViewOf(r *http.Request) strengthView {
	data := ui.ws(r).Lifting.Load()
	now := time.Now()
	view := strengthView{Unit: data.Unit, Measure: strengthMeasure(data.Strength.Measure), Range: data.Strength.Range,
		Ranges: strengthRanges, Calc: calcView{Unit: data.Unit}}
	if !slices.ContainsFunc(strengthRanges, func(c choice) bool { return c.ID == view.Range }) {
		view.Range = "6m"
	}
	body := 0.0
	if weights := ui.ws(r).Store.Weights(); len(weights) > 0 {
		body = weights[len(weights)-1].LB
		view.Body = lifting.FormatShown(body, data.Unit)
	}
	slots := data.ShownLifts(now)
	slotOf := func(name string) int {
		for i, shown := range slots {
			if shown != "" && strings.EqualFold(shown, name) {
				return i + 1
			}
		}
		return 0
	}
	lifts := data.Lifts(now)
	view.HasLifts = len(lifts) > 0
	taken := 0
	for _, shown := range slots {
		if shown != "" {
			taken++
		}
	}
	view.Full = taken == lifting.ChartSlots
	for _, lift := range lifts {
		view.Choices = append(view.Choices, liftChoice{Name: lift.Name, Slot: slotOf(lift.Name), Sessions: lift.Sessions})
	}

	since := rangeStart(view.Range, now)
	value := func(p lifting.Point) float64 { // estimates to the plate, logged weights as logged
		if view.Measure == "e1rm" {
			return lifting.RoundToPlate(lifting.Shown(p.Best, data.Unit), data.Unit)
		}
		return lifting.Shown(p.Heavy, data.Unit)
	}
	valueText := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	type line struct {
		series strengthSeries
		points []lifting.Point
	}
	var lines []line
	low, high, first := math.Inf(1), math.Inf(-1), now
	for i, name := range slots {
		if name == "" {
			continue
		}
		points := data.Points(name, since)
		series := strengthSeries{Slot: i + 1, Name: name}
		if len(points) > 0 {
			last := points[len(points)-1]
			series.Latest = valueText(value(last))
			top := 0.0
			for _, p := range points {
				low, high, top = math.Min(low, value(p)), math.Max(high, value(p)), math.Max(top, value(p))
				if p.Day.Before(first) {
					first = p.Day
				}
			}
			if gain := math.Round(value(last) - value(points[0])); gain > 0 && len(points) > 1 {
				series.Change = fmt.Sprintf("▲ %.0f", gain)
			}
			if body > 0 {
				series.Ratio = strconv.FormatFloat(lifting.FromShown(top, data.Unit)/body, 'f', 2, 64)
			}
		}
		lines = append(lines, line{series, points})
	}
	if math.IsInf(low, 1) { // nothing logged in the range
		for _, l := range lines {
			view.Series = append(view.Series, l.series)
		}
		return view
	}
	from := since
	if from.IsZero() || first.After(from) {
		from = first
	}
	from, to := startOfDay(from), now
	if to.Sub(from) < 7*24*time.Hour {
		from = to.AddDate(0, 0, -7)
	}
	low, high = niceBounds(low, high)
	const width, height = 300.0, 120.0
	x := func(t time.Time) float64 { return t.Sub(from).Seconds() / to.Sub(from).Seconds() * width }
	y := func(v float64) float64 { return height - (v-low)/(high-low)*height }
	view.Chart = &strengthChart{From: from, To: to}
	for _, tick := range []float64{low, (low + high) / 2, high} {
		view.Chart.Ticks = append(view.Chart.Ticks, strengthTick{Top: y(tick) / height * 100, Label: formatNumber(tick)})
	}
	for _, l := range lines {
		series := l.series
		var path strings.Builder
		for i, p := range l.points {
			px, py := x(p.Day), y(value(p))
			command := "L"
			if i == 0 {
				command = "M"
			}
			fmt.Fprintf(&path, "%s%.1f,%.1f ", command, px, py)
			point := strengthPoint{Left: px / width * 100, Top: py / height * 100, Day: p.Day, Value: valueText(value(p)),
				E1RM: lifting.FormatPlate(p.Best, data.Unit)}
			if p.BestSet.Reps > 0 {
				point.Set = setsText([]lifting.Set{p.BestSet}, data.Unit)
			}
			series.Points = append(series.Points, point)
		}
		series.Path = strings.TrimSpace(path.String())
		view.Series = append(view.Series, series)
	}
	return view
}

// niceBounds widens a range to round numbers a step apart (5, 10, 25...), with room above and below.
func niceBounds(low, high float64) (float64, float64) {
	span := math.Max(high-low, 10)
	step := 5.0
	for _, s := range []float64{5, 10, 25, 50, 100, 250} {
		if step = s; span/s <= 6 {
			break
		}
	}
	low = math.Max(0, math.Floor((low-span*0.1)/step)*step)
	high = math.Ceil((high+span*0.1)/step) * step
	if high <= low {
		high = low + step
	}
	return low, high
}

func (ui *UI) renderStrengthCard(r *http.Request) (template.HTML, error) {
	return ui.renderToHTML("card-strength", ui.strengthViewOf(r))
}

func (ui *UI) strengthCard(w http.ResponseWriter, r *http.Request) {
	html, err := ui.renderStrengthCard(r)
	if err != nil {
		fail(w, err)
		return
	}
	writeHTML(w, html)
}

// saveStrength saves the Strength card's choices: the lifts (by name), the measure and range.
func (ui *UI) saveStrength(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error {
		if r.PostForm.Has("pick") {
			d.PickLifts(r.PostForm["lift"], time.Now())
		}
		if measure := r.PostForm.Get("measure"); measure == "heaviest" || measure == "e1rm" {
			d.Strength.Measure = measure
		}
		for _, choice := range strengthRanges {
			if r.PostForm.Get("range") == choice.ID {
				d.Strength.Range = choice.ID
			}
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	view := ui.strengthViewOf(r)
	view.PickOpen = r.PostForm.Has("pick")
	ui.render(w, "card-strength", view)
}

// calcView is the 1RM calculator: a set typed in (weight × reps), what it says your one-rep max
// is, and what that suggests for other rep counts. It works in the unit shown, so no conversion.
type calcView struct {
	Unit    string
	Weight  string
	Reps    string
	Problem string
	Epley   string // the estimate the Lifting tab uses (PRs, progress)
	Brzycki string
	Table   []calcRow
}

type calcRow struct {
	Reps    int
	Weight  string
	Percent int
}

func calcViewOf(unit, typedWeight, typedReps string) calcView {
	view := calcView{Unit: unit, Weight: strings.TrimSpace(typedWeight), Reps: strings.TrimSpace(typedReps)}
	if view.Weight == "" || view.Reps == "" {
		return view
	}
	weight, err := strconv.ParseFloat(strings.ReplaceAll(view.Weight, ",", "."), 64)
	reps, repsErr := strconv.Atoi(view.Reps)
	switch {
	case err != nil || weight <= 0 || weight > 5000:
		view.Problem = "Type the weight you lifted."
		return view
	case repsErr != nil || reps < 1 || reps > 30:
		view.Problem = "Reps: 1 to 30 (one set's reps, not the number of sets)."
		return view
	}
	oneRepMax := lifting.E1RM(lifting.Set{LB: weight, Reps: reps})
	plate := func(v float64) string { return strconv.FormatFloat(lifting.RoundToPlate(v, unit), 'f', -1, 64) }
	view.Epley, view.Brzycki = plate(oneRepMax), plate(lifting.Brzycki(weight, reps))
	for _, n := range []int{1, 2, 3, 4, 5, 6, 8, 10, 12, 15} {
		at := lifting.WeightFor(oneRepMax, n)
		view.Table = append(view.Table, calcRow{Reps: n, Weight: plate(at), Percent: int(math.Round(at / oneRepMax * 100))})
	}
	return view
}

func (ui *UI) oneRepMax(w http.ResponseWriter, r *http.Request) {
	unit := ui.ws(r).Lifting.Load().Unit
	ui.render(w, "lift-calc-result", calcViewOf(unit, r.FormValue("weight"), r.FormValue("reps")))
}

// ---------- editing a past workout ----------

type workoutEditorView struct {
	ID, Title, Note string
	Day, Time       string // the start, as the date and time inputs take them
	Took            string
	Unit            string
	Exercises       []editedExercise
	Names           []string
}

type editedExercise struct {
	Index       int
	Name        string
	Top, Bottom bool
	Sets        []setView // the ones done: skipped sets stay as they were
	Skipped     bool
	Why         string
}

func workoutEditorOf(w lifting.Workout, data lifting.Data) workoutEditorView {
	view := workoutEditorView{ID: w.ID, Title: w.Title, Note: w.Note, Day: w.Started.Format(dayLayout),
		Time: w.Started.Format("15:04"), Unit: data.Unit, Names: data.ExerciseNames()}
	if minutes := int(math.Round(w.Duration(w.Ended).Minutes())); minutes > 0 {
		view.Took = minutesText(min(minutes, 24*60))
	}
	for e, ex := range w.Exercises {
		edited := editedExercise{Index: e, Name: ex.Name, Top: e == 0, Bottom: e == len(w.Exercises)-1, Skipped: ex.Skipped, Why: ex.Why}
		for _, set := range ex.Sets {
			if !set.Skipped {
				edited.Sets = append(edited.Sets, setViewOf(len(edited.Sets), set, data.Unit))
			}
		}
		view.Exercises = append(view.Exercises, edited)
	}
	return view
}

func (ui *UI) workoutEditor(w http.ResponseWriter, r *http.Request) {
	data := ui.ws(r).Lifting.Load()
	workout, ok := data.Workout(r.PathValue("id"))
	if !ok {
		fail(w, problem.NotFoundf("that workout is gone"))
		return
	}
	ui.render(w, "lift-workout-editor", workoutEditorOf(workout, data))
}

// workoutFromForm reads the editor: title, day, time, took, note, and each exercise's e0.name
// with its sets' weights and reps (e0.s1.w, e0.s1.r), weights in the unit shown.
func workoutFromForm(r *http.Request, base lifting.Workout, unit string) (lifting.Workout, error) {
	r.ParseForm()
	edited := base
	edited.Title, edited.Note = r.PostForm.Get("title"), r.PostForm.Get("note")
	var problemFound error // reported after reading everything, so an editor button keeps it all
	if started, ok := parseLocalTime(formText(r, "day"), formText(r, "time")); ok {
		edited.Started = started
	} else {
		problemFound = problem.Invalidf("pick the day and time it started")
	}
	if minutes, err := parseTook(r.PostForm.Get("took")); err == nil {
		edited.Minutes = minutes
	} else if problemFound == nil {
		problemFound = err
	}
	edited.Exercises = nil
	for e := 0; r.PostForm.Has(fmt.Sprintf("e%d.name", e)); e++ {
		ex := lifting.Exercise{Name: r.PostForm.Get(fmt.Sprintf("e%d.name", e)),
			Skipped: r.PostForm.Get(fmt.Sprintf("e%d.skipped", e)) == "1", Why: r.PostForm.Get(fmt.Sprintf("e%d.why", e))}
		var skippedSets []lifting.Set
		for _, old := range base.Exercises { // keep the planned reps, rest and skipped sets of an exercise it had
			if strings.EqualFold(old.Name, strings.TrimSpace(ex.Name)) {
				ex.Target, ex.Rest = old.Target, old.Rest
				for _, set := range old.Sets {
					if set.Skipped {
						skippedSets = append(skippedSets, set)
					}
				}
			}
		}
		for s := 0; r.PostForm.Has(fmt.Sprintf("e%d.s%d.r", e, s)); s++ {
			set := lifting.Set{}
			if typed := strings.ReplaceAll(formText(r, fmt.Sprintf("e%d.s%d.w", e, s)), ",", "."); typed != "" {
				if weight, err := strconv.ParseFloat(typed, 64); err == nil && weight >= 0 {
					set.LB = lifting.FromShown(weight, unit)
				} else if problemFound == nil {
					problemFound = problem.Invalidf("a weight isn't a number: " + typed)
				}
			}
			set.Reps, _ = strconv.Atoi(formText(r, fmt.Sprintf("e%d.s%d.r", e, s)))
			set.Note = r.PostForm.Get(fmt.Sprintf("e%d.s%d.n", e, s))
			ex.Sets = append(ex.Sets, set)
		}
		ex.Sets = append(ex.Sets, skippedSets...)
		edited.Exercises = append(edited.Exercises, ex)
	}
	return edited, problemFound
}

func parseLocalTime(day, clock string) (time.Time, bool) {
	if clock == "" {
		clock = "12:00"
	}
	t, err := time.ParseInLocation(dayLayout+" 15:04", day+" "+clock, time.Local)
	return t, err == nil
}

// editWorkout applies one of the editor's buttons (add-ex, remove-ex:0, up:0, down:0,
// add-set:0, remove-set:0:1) to the workout as typed.
func editWorkout(w *lifting.Workout, op string) {
	parts := strings.Split(op, ":")
	n := make([]int, len(parts)-1)
	for i, part := range parts[1:] {
		n[i], _ = strconv.Atoi(part)
	}
	exercises := w.Exercises
	inRange := len(n) > 0 && n[0] >= 0 && n[0] < len(exercises)
	switch {
	case parts[0] == "add-ex":
		w.Exercises = append(exercises, lifting.Exercise{Sets: []lifting.Set{{}}})
	case parts[0] == "remove-ex" && inRange:
		w.Exercises = append(exercises[:n[0]], exercises[n[0]+1:]...)
	case (parts[0] == "up" || parts[0] == "down") && inRange:
		swapAt(exercises, n[0], parts[0] == "up")
	case parts[0] == "add-set" && inRange:
		sets := exercises[n[0]].Sets
		next := lifting.Set{}
		if len(sets) > 0 {
			next = sets[len(sets)-1]
		}
		exercises[n[0]].Sets = append(sets, next)
	case parts[0] == "remove-set" && inRange && len(n) == 2 && n[1] >= 0 && n[1] < len(exercises[n[0]].Sets):
		exercises[n[0]].Sets = append(exercises[n[0]].Sets[:n[1]], exercises[n[0]].Sets[n[1]+1:]...)
	}
}

// saveWorkout applies an editor button (answering with the editor) or, for Save, saves the
// workout and answers with the Workouts card. Its task and time log stay as they were.
func (ui *UI) saveWorkout(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	data := ws.Lifting.Load()
	base, ok := data.Workout(r.PathValue("id"))
	if !ok {
		fail(w, problem.NotFoundf("that workout is gone"))
		return
	}
	edited, err := workoutFromForm(r, base, data.Unit)
	if op := r.FormValue("op"); op != "" {
		editWorkout(&edited, op)
		view := workoutEditorOf(edited, data)
		view.Day, view.Time, view.Took = formText(r, "day"), formText(r, "time"), formText(r, "took")
		ui.render(w, "lift-workout-editor", view)
		return
	}
	if err == nil {
		err = ws.Lifting.Update(func(d *lifting.Data) error { return d.ReplaceWorkout(edited) })
	}
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Workout saved"), liftingChanged)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ui.render(w, "lift-history", liftingHistoryOf(ws.Lifting.Load(), min(limit, 1000)))
}

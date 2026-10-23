// Package lifting is the Lifting tab's data: routines (days of planned exercises), the workout in
// progress and the finished ones, kept in lifting.json in the user's folder.
//
// Weights are kept in pounds; Unit only says how they're shown and typed. A weight of 0 is a
// bodyweight set (push-ups, pull-ups).
package lifting

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"io"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
	"dashboard/internal/text"
)

// Data is lifting.json.
type Data struct {
	Unit     string    `json:"unit,omitempty"` // "lb" (the default) or "kg"
	Routines []Routine `json:"routines"`
	Active   *Workout  `json:"active,omitempty"`  // the workout in progress
	Workouts []Workout `json:"workouts"`          // finished, oldest first
	Seeded   bool      `json:"seeded,omitempty"`  // the plan's sessions were copied in (once)
	Strength Strength  `json:"strength,omitzero"` // what the Strength chart shows
}

// Strength is what the Strength chart (on Today) shows: which lifts, in fixed colour slots,
// which number and how far back.
type Strength struct {
	Picked  bool               `json:"picked,omitempty"`  // false: the most-done lifts
	Lifts   [ChartSlots]string `json:"lifts"`             // by colour slot; "" is a free slot
	Measure string             `json:"measure,omitempty"` // "e1rm" (default) or "heaviest"
	Range   string             `json:"range,omitempty"`   // "3m", "6m" (default), "1y", "all"
}

// ChartSlots is how many lifts the Strength chart shows at once (one colour each).
const ChartSlots = 4

// Routine is a training plan made of days ("Push", "Legs"...).
type Routine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Days []Day  `json:"days"`
}

// Day is one workout of a routine.
type Day struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Task      string    `json:"task,omitempty"` // a task's description: finishing the workout ticks it
	Exercises []Planned `json:"exercises"`
}

// Planned is an exercise as the routine plans it.
type Planned struct {
	Name string `json:"name"`
	Sets int    `json:"sets"`
	Reps string `json:"reps,omitempty"` // "10", "8-12", "max", "45 s"
	Rest int    `json:"rest"`           // seconds between sets
}

// Workout is a training session: in progress (Data.Active) or finished.
type Workout struct {
	ID        string     `json:"id"`
	Routine   string     `json:"routine,omitempty"` // the routine and day it came from; empty for a free one
	Day       string     `json:"day,omitempty"`
	Title     string     `json:"title"`
	Task      string     `json:"task,omitempty"`
	Started   time.Time  `json:"started"`
	Ended     time.Time  `json:"ended,omitzero"`
	Minutes   int        `json:"minutes,omitempty"` // how long it took, as confirmed when finishing
	RestUntil time.Time  `json:"restUntil,omitzero"`
	RestFor   int        `json:"restFor,omitempty"` // seconds the current rest started with
	Exercises []Exercise `json:"exercises"`
	Note      string     `json:"note,omitempty"`
}

// Exercise is an exercise in a workout and its sets.
type Exercise struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"` // the planned reps
	Rest     int    `json:"rest"`
	Sets     []Set  `json:"sets"`
	FromLast bool   `json:"fromLast,omitempty"` // its sets were filled in from the last session
	Skipped  bool   `json:"skipped,omitempty"`  // left out on purpose, maybe with a reason
	Why      string `json:"why,omitempty"`
}

// Set is one set: done, or still a plan (pre-filled from last time, or as typed so far).
type Set struct {
	LB     float64 `json:"lb"`
	Reps   int     `json:"reps"`
	Done   bool    `json:"done,omitempty"`
	Edited bool    `json:"edited,omitempty"` // typed in during the workout: logging a set above leaves it
	Note   string  `json:"note,omitempty"`
	// Skipped on purpose; Why says why (it was the set's note when it was skipped).
	Skipped bool   `json:"skipped,omitempty"`
	Why     string `json:"why,omitempty"`
}

const maxSetNote = 200

const (
	DefaultRest = 90
	maxSets     = 30
	maxItems    = 40 // exercises in a workout or day, days in a routine
	maxWorkouts = 5000
)

// ---------- the file ----------

// Store guards lifting.json, so two requests at once can't lose each other's changes.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: path} }

func (s *Store) Load() Data {
	var data Data
	jsonfile.Read(s.path, &data)
	if data.Unit != "kg" {
		data.Unit = "lb"
	}
	return data
}

// Update loads the data, applies change and saves it, as one step. Nothing is saved when change
// fails.
func (s *Store) Update(change func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.Load()
	if err := change(&data); err != nil {
		return err
	}
	return jsonfile.Write(s.path, data)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- units ----------

const kgPerLB = 0.45359237

// Shown is a weight in the unit it's shown in, to a tenth.
func Shown(lb float64, unit string) float64 {
	if unit == "kg" {
		lb *= kgPerLB
	}
	return math.Round(lb*10) / 10
}

// FromShown is a weight typed in unit, in pounds.
func FromShown(value float64, unit string) float64 {
	if unit == "kg" {
		return value / kgPerLB
	}
	return value
}

// PlateStep is the smallest jump in weight most gyms have: 2.5 lb, or 1.25 kg.
func PlateStep(unit string) float64 {
	if unit == "kg" {
		return 1.25
	}
	return 2.5
}

// RoundToPlate rounds a weight already in unit to the nearest PlateStep.
func RoundToPlate(value float64, unit string) float64 {
	step := PlateStep(unit)
	return math.Round(value/step) * step
}

// FormatPlate writes a weight (pounds) in unit, rounded to the nearest PlateStep: 262.5, 245.
// Estimates (one-rep maxes, weights for a number of reps) are shown this way: weights you can load.
func FormatPlate(lb float64, unit string) string {
	value := lb
	if unit == "kg" {
		value *= kgPerLB
	}
	return strconv.FormatFloat(RoundToPlate(value, unit), 'f', -1, 64)
}

// FormatShown writes a weight in unit the way it's shown: 135, 61.2 (no trailing .0).
func FormatShown(lb float64, unit string) string {
	return strconv.FormatFloat(Shown(lb, unit), 'f', -1, 64)
}

// ---------- routines ----------

func (d *Data) Routine(id string) (*Routine, bool) {
	for i := range d.Routines {
		if d.Routines[i].ID == id {
			return &d.Routines[i], true
		}
	}
	return nil, false
}

func (r *Routine) Day(id string) (*Day, bool) {
	for i := range r.Days {
		if r.Days[i].ID == id {
			return &r.Days[i], true
		}
	}
	return nil, false
}

// SaveRoutine adds or replaces a routine (by ID), cleaned up: days and exercises without a name
// are dropped, and new ones get IDs.
func (d *Data) SaveRoutine(routine Routine) (Routine, error) {
	routine = CleanRoutine(routine)
	if routine.ID == "" {
		routine.ID = newID()
		d.Routines = append(d.Routines, routine)
		return routine, nil
	}
	existing, ok := d.Routine(routine.ID)
	if !ok {
		return Routine{}, problem.NotFoundf("that routine is gone")
	}
	*existing = routine
	return routine, nil
}

func (d *Data) DeleteRoutine(id string) {
	d.Routines = slices.DeleteFunc(d.Routines, func(r Routine) bool { return r.ID == id })
}

// CleanRoutine trims what was typed and fills in what's missing.
func CleanRoutine(routine Routine) Routine {
	routine.Name = text.Truncate(text.Collapse(routine.Name), 80)
	if routine.Name == "" {
		routine.Name = "Routine"
	}
	var days []Day
	for _, day := range routine.Days {
		day.Name = text.Truncate(text.Collapse(day.Name), 80)
		day.Task = text.Truncate(text.Collapse(day.Task), 300)
		var exercises []Planned
		for _, ex := range day.Exercises {
			if ex.Name = cleanName(ex.Name); ex.Name == "" {
				continue
			}
			ex.Sets = clamp(ex.Sets, 1, maxSets)
			ex.Reps = text.Truncate(text.Collapse(ex.Reps), 20)
			ex.Rest = clamp(ex.Rest, 0, 3600)
			exercises = append(exercises, ex)
		}
		if day.Name == "" && len(exercises) == 0 {
			continue
		}
		if day.Name == "" {
			day.Name = "Day " + strconv.Itoa(len(days)+1)
		}
		if day.ID == "" {
			day.ID = newID()
		}
		day.Exercises = exercises[:min(len(exercises), maxItems)]
		days = append(days, day)
	}
	routine.Days = days[:min(len(days), maxItems)]
	return routine
}

func cleanName(name string) string { return text.Truncate(text.Collapse(name), 80) }

func clamp(n, low, high int) int { return max(low, min(n, high)) }

// DayForTask is the routine day linked to a task, by its description.
func (d Data) DayForTask(description string) (Routine, Day, bool) {
	for _, routine := range d.Routines {
		for _, day := range routine.Days {
			if day.Task != "" && strings.EqualFold(day.Task, strings.TrimSpace(description)) {
				return routine, day, true
			}
		}
	}
	return Routine{}, Day{}, false
}

// ---------- the workout in progress ----------

// Start begins a routine's day, each exercise's sets filled in from the last time it was done.
func (d *Data) Start(routineID, dayID string, now time.Time) error {
	if d.Active != nil {
		return problem.Conflictf("a workout is already going: finish or discard it first")
	}
	routine, ok := d.Routine(routineID)
	if !ok {
		return problem.NotFoundf("that routine is gone")
	}
	day, ok := routine.Day(dayID)
	if !ok {
		return problem.NotFoundf("that day is gone from the routine")
	}
	workout := &Workout{ID: newID(), Routine: routine.ID, Day: day.ID, Title: day.Name, Task: day.Task, Started: now}
	for _, planned := range day.Exercises {
		workout.Exercises = append(workout.Exercises, d.prefill(planned))
	}
	d.Active = workout
	return nil
}

// StartEmpty begins a workout with no exercises yet.
func (d *Data) StartEmpty(now time.Time) error {
	if d.Active != nil {
		return problem.Conflictf("a workout is already going: finish or discard it first")
	}
	d.Active = &Workout{ID: newID(), Title: "Workout", Started: now}
	return nil
}

// prefill makes a planned exercise's sets: last time's weights and reps, set by set, or the
// planned reps.
func (d Data) prefill(planned Planned) Exercise {
	last := d.LastSets(planned.Name)
	exercise := Exercise{Name: planned.Name, Target: planned.Reps, Rest: planned.Rest, FromLast: len(last) > 0}
	count := planned.Sets
	if count == 0 {
		count = max(len(last), 3)
	}
	for i := range count {
		set := Set{Reps: leadingNumber(planned.Reps)}
		if len(last) > 0 {
			previous := last[min(i, len(last)-1)]
			set.LB, set.Reps = previous.LB, previous.Reps
		}
		exercise.Sets = append(exercise.Sets, set)
	}
	return exercise
}

// leadingNumber is the number a target starts with ("8-12" → 8), or 0.
func leadingNumber(target string) int {
	end := strings.IndexFunc(target, func(r rune) bool { return r < '0' || r > '9' })
	if end == -1 {
		end = len(target)
	}
	n, _ := strconv.Atoi(target[:end])
	return n
}

// LastSets are the done sets of the last finished workout that had this exercise.
func (d Data) LastSets(name string) []Set {
	for i := len(d.Workouts) - 1; i >= 0; i-- {
		for _, ex := range d.Workouts[i].Exercises {
			if sameExercise(ex.Name, name) {
				return doneSets(ex.Sets)
			}
		}
	}
	return nil
}

func sameExercise(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func doneSets(sets []Set) []Set {
	var done []Set
	for _, set := range sets {
		if set.Done {
			done = append(done, set)
		}
	}
	return done
}

// Change is one thing done to the workout in progress.
type Change struct {
	Op       string // done, undo, add-set, remove-set, add-exercise, remove-exercise, up, down, rest, skip-rest, skip-set, unskip-set, skip-exercise, unskip-exercise
	Exercise int
	Set      int
	Name     string // for add-exercise
	Seconds  int    // for rest
	Why      string // for skip-set and skip-exercise
}

// Apply makes a change to the workout in progress.
func (d *Data) Apply(change Change, now time.Time) error {
	w := d.Active
	if w == nil {
		return problem.Conflictf("no workout is going")
	}
	if change.Op == "add-exercise" {
		name := cleanName(change.Name)
		if name == "" {
			return problem.Invalidf("type the exercise's name")
		}
		if len(w.Exercises) >= maxItems {
			return problem.Invalidf("that's a lot of exercises: finish this workout first")
		}
		for _, known := range d.ExerciseNames() { // "squats" is the Squats done before
			if sameExercise(known, name) {
				name = known
				break
			}
		}
		w.Exercises = append(w.Exercises, d.prefill(Planned{Name: name, Rest: DefaultRest}))
		return nil
	}
	switch change.Op {
	case "rest":
		if w.RestUntil.Before(now) {
			w.RestUntil, w.RestFor = now, 0
		}
		w.RestUntil = w.RestUntil.Add(time.Duration(change.Seconds) * time.Second)
		w.RestFor += change.Seconds
		return nil
	case "skip-rest":
		w.RestUntil, w.RestFor = time.Time{}, 0
		return nil
	}
	if change.Exercise < 0 || change.Exercise >= len(w.Exercises) {
		return problem.Invalidf("that exercise isn't in this workout")
	}
	ex := &w.Exercises[change.Exercise]
	switch change.Op {
	case "add-set":
		if len(ex.Sets) >= maxSets {
			return problem.Invalidf("that's a lot of sets")
		}
		set := Set{Reps: leadingNumber(ex.Target)}
		if len(ex.Sets) > 0 {
			last := ex.Sets[len(ex.Sets)-1]
			set = Set{LB: last.LB, Reps: last.Reps}
		}
		ex.Sets = append(ex.Sets, set)
		return nil
	case "remove-exercise":
		w.Exercises = slices.Delete(w.Exercises, change.Exercise, change.Exercise+1)
		return nil
	case "skip-exercise":
		ex.Skipped, ex.Why = true, text.Truncate(text.Collapse(change.Why), maxSetNote)
		return nil
	case "unskip-exercise":
		ex.Skipped, ex.Why = false, ""
		return nil
	case "up", "down":
		other := change.Exercise - 1
		if change.Op == "down" {
			other = change.Exercise + 1
		}
		if other >= 0 && other < len(w.Exercises) {
			w.Exercises[other], w.Exercises[change.Exercise] = w.Exercises[change.Exercise], w.Exercises[other]
		}
		return nil
	}
	if change.Set < 0 || change.Set >= len(ex.Sets) {
		return problem.Invalidf("that set isn't in this workout")
	}
	set := &ex.Sets[change.Set]
	switch change.Op {
	case "skip-set":
		set.Skipped, set.Done = true, false
		set.Why, set.Note = text.Truncate(text.Collapse(set.Note), maxSetNote), ""
		return nil
	case "unskip-set":
		set.Skipped, set.Note, set.Why = false, set.Why, ""
		return nil
	case "done":
		if set.Reps <= 0 {
			return problem.Invalidf("type how many reps you did")
		}
		set.Done, set.Skipped = true, false
		// With no last session to go by, the sets after it start from this one (unless typed in).
		for i := change.Set + 1; i < len(ex.Sets) && !ex.FromLast; i++ {
			if next := &ex.Sets[i]; !next.Done && !next.Edited {
				next.LB, next.Reps = set.LB, set.Reps
			}
		}
		if ex.Rest > 0 {
			w.RestUntil, w.RestFor = now.Add(time.Duration(ex.Rest)*time.Second), ex.Rest
		}
	case "undo":
		set.Done = false
	case "remove-set":
		ex.Sets = slices.Delete(ex.Sets, change.Set, change.Set+1)
	default:
		return problem.Invalidf("unknown change: " + change.Op)
	}
	return nil
}

// Typed is what's in a set's inputs: nil leaves that value as it is.
type Typed struct {
	LB   *float64
	Reps *int
	Note *string
}

// SetTyped keeps what was typed in the sets' inputs, by exercise and set, so it survives a
// reload or another change.
func (d *Data) SetTyped(typed map[[2]int]Typed) {
	if d.Active == nil {
		return
	}
	for key, value := range typed {
		e, s := key[0], key[1]
		if e < 0 || e >= len(d.Active.Exercises) || s < 0 || s >= len(d.Active.Exercises[e].Sets) {
			continue
		}
		set := &d.Active.Exercises[e].Sets[s]
		// The page sends every input each time, so only a value that changed counts as typed
		// (weights within a quarter pound: kg shown to a tenth comes back a little off).
		if value.LB != nil && *value.LB >= 0 && *value.LB < 5000 && math.Abs(*value.LB-set.LB) > 0.25 {
			set.LB, set.Edited = math.Round(*value.LB*1000)/1000, true
		}
		if value.Reps != nil && *value.Reps >= 0 && *value.Reps < 1000 && *value.Reps != set.Reps {
			set.Reps, set.Edited = *value.Reps, true
		}
		if value.Note != nil {
			set.Note = text.Truncate(text.Collapse(*value.Note), maxSetNote)
		}
	}
}

// Finish saves the workout in progress with its done sets (exercises with none are dropped).
func (d *Data) Finish(now time.Time, minutes int, note string) (Workout, error) {
	w := d.Active
	if w == nil {
		return Workout{}, problem.Conflictf("no workout is going")
	}
	finished := *w
	finished.Exercises = nil
	done := 0
	for _, ex := range w.Exercises {
		// The sets done and the ones skipped (with why) are kept; a skipped exercise keeps
		// only its reason. Sets neither done nor skipped weren't done.
		var kept []Set
		for _, set := range ex.Sets {
			if set.Done || (set.Skipped && !ex.Skipped) {
				set.Edited = false
				kept = append(kept, set)
			}
			if set.Done {
				done++
			}
		}
		ex.FromLast = false
		if ex.Skipped && len(doneSets(kept)) == 0 {
			ex.Sets = nil
			finished.Exercises = append(finished.Exercises, ex)
		} else if len(kept) > 0 {
			ex.Sets, ex.Skipped, ex.Why = kept, false, ""
			finished.Exercises = append(finished.Exercises, ex)
		}
	}
	if done == 0 {
		return Workout{}, problem.Invalidf("tick at least one set first (or discard the workout)")
	}
	finished.Ended, finished.Minutes = now, minutes
	finished.RestUntil, finished.RestFor = time.Time{}, 0
	finished.Note = text.Truncate(text.Collapse(note), 500)
	d.Workouts = append(d.Workouts, finished)
	if len(d.Workouts) > maxWorkouts {
		d.Workouts = d.Workouts[len(d.Workouts)-maxWorkouts:]
	}
	d.Active = nil
	return finished, nil
}

func (d *Data) Discard() { d.Active = nil }

func (d *Data) DeleteWorkout(id string) {
	d.Workouts = slices.DeleteFunc(d.Workouts, func(w Workout) bool { return w.ID == id })
}

// ---------- numbers ----------

// E1RM is a set's estimated one-rep max (Epley), in pounds; 0 for bodyweight sets.
func E1RM(set Set) float64 {
	if set.LB <= 0 || set.Reps <= 0 {
		return 0
	}
	if set.Reps == 1 {
		return set.LB
	}
	return set.LB * (1 + float64(set.Reps)/30)
}

// Volume is weight × reps over a workout's done sets, in pounds.
func (w Workout) Volume() float64 {
	total := 0.0
	for _, ex := range w.Exercises {
		for _, set := range ex.Sets {
			if set.Done {
				total += set.LB * float64(set.Reps)
			}
		}
	}
	return total
}

// DoneSets counts the workout's done sets.
func (w Workout) DoneSets() int {
	n := 0
	for _, ex := range w.Exercises {
		n += len(doneSets(ex.Sets))
	}
	return n
}

// Duration is how long the workout took: as confirmed when finishing, or so far.
func (w Workout) Duration(now time.Time) time.Duration {
	if w.Minutes > 0 {
		return time.Duration(w.Minutes) * time.Minute
	}
	if !w.Ended.IsZero() {
		now = w.Ended
	}
	return now.Sub(w.Started)
}

// Best is an exercise's best estimated one-rep max (pounds) over workouts that ended before a
// time, or ever with a zero time.
func (d Data) Best(name string, before time.Time) float64 {
	best := 0.0
	for _, w := range d.Workouts {
		if !before.IsZero() && !w.Ended.Before(before) {
			continue
		}
		for _, ex := range w.Exercises {
			if sameExercise(ex.Name, name) {
				for _, set := range doneSets(ex.Sets) {
					best = math.Max(best, E1RM(set))
				}
			}
		}
	}
	return best
}

// Records marks each done set of a workout that beat the exercise's earlier best (by
// estimated one-rep max), by exercise and set. A first time doing an exercise isn't a record.
func (d Data) Records(w Workout) map[[2]int]bool {
	records := map[[2]int]bool{}
	before := w.Ended
	if before.IsZero() {
		before = time.Now().Add(time.Minute) // in progress: everything finished counts
	}
	for e, ex := range w.Exercises {
		best := d.Best(ex.Name, before)
		if best == 0 {
			continue
		}
		for s, set := range ex.Sets {
			if set.Done && E1RM(set) > best+0.01 {
				records[[2]int{e, s}] = true
				best = E1RM(set)
			}
		}
	}
	return records
}

// Session is one workout's sets of an exercise.
type Session struct {
	Workout string
	Day     time.Time
	Title   string
	Sets    []Set
	Best    float64 // the best set's estimated one-rep max, pounds
	Heavy   float64 // the heaviest weight, pounds
	Record  bool
}

// History is an exercise's sessions, oldest first.
func (d Data) History(name string) []Session {
	var sessions []Session
	best := 0.0
	for _, w := range d.Workouts {
		for _, ex := range w.Exercises {
			if !sameExercise(ex.Name, name) {
				continue
			}
			session := Session{Workout: w.ID, Day: w.Started, Title: w.Title, Sets: doneSets(ex.Sets)}
			if len(session.Sets) == 0 { // skipped that day
				continue
			}
			for _, set := range session.Sets {
				session.Best = math.Max(session.Best, E1RM(set))
				session.Heavy = math.Max(session.Heavy, set.LB)
			}
			session.Record = best > 0 && session.Best > best+0.01
			best = math.Max(best, session.Best)
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// ExerciseNames is every exercise in the routines and workouts, sorted, each once.
func (d Data) ExerciseNames() []string {
	seen := map[string]string{}
	add := func(name string) {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" && seen[key] == "" {
			seen[key] = strings.TrimSpace(name)
		}
	}
	for _, routine := range d.Routines {
		for _, day := range routine.Days {
			for _, ex := range day.Exercises {
				add(ex.Name)
			}
		}
	}
	for _, w := range d.Workouts {
		for _, ex := range w.Exercises {
			add(ex.Name)
		}
	}
	if d.Active != nil {
		for _, ex := range d.Active.Exercises {
			add(ex.Name)
		}
	}
	names := make([]string, 0, len(seen))
	for _, name := range seen {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// LoggedNames is every exercise of the finished workouts, sorted.
func (d Data) LoggedNames() []string { return Data{Workouts: d.Workouts}.ExerciseNames() }

// ---------- from the plan ----------

var setsTimes = regexp.MustCompile(`^\s*(\d+)\s*[×xX*]\s*(.*?)\s*$`)

// ParsePlanned reads an exercise as the plan writes it ("3 × 10", "3x8-12", "3 × 45 s").
func ParsePlanned(name, target string) Planned {
	planned := Planned{Name: name, Sets: 3, Reps: strings.TrimSpace(target), Rest: DefaultRest}
	if m := setsTimes.FindStringSubmatch(target); m != nil {
		planned.Sets, _ = strconv.Atoi(m[1])
		planned.Reps = m[2]
	}
	return planned
}

// ---------- strength ----------

// Brzycki is another estimate of a set's one-rep max: weight × 36 / (37 − reps). It reads a
// little lower than Epley's (E1RM) and is meant for up to about 10 reps.
func Brzycki(lb float64, reps int) float64 {
	if lb <= 0 || reps <= 0 || reps >= 37 {
		return 0
	}
	if reps == 1 {
		return lb
	}
	return lb * 36 / float64(37-reps)
}

// WeightFor is the weight a one-rep max suggests for a number of reps (Epley, turned around).
func WeightFor(oneRepMax float64, reps int) float64 {
	if reps <= 1 {
		return oneRepMax
	}
	return oneRepMax / (1 + float64(reps)/30)
}

// Lift is how strong you are at one exercise.
type Lift struct {
	Name     string
	Best     float64   // the best estimated one-rep max ever, pounds
	Before   float64   // the best before the last 30 days (0: none)
	Sessions int       // workouts with weighted sets of it
	Last     time.Time // when it was last done
}

// Change is how much the best estimated one-rep max went up in the last 30 days.
func (l Lift) Change() float64 {
	if l.Before == 0 {
		return 0
	}
	return l.Best - l.Before
}

// Lifts are the exercises done with weights, the most done first.
func (d Data) Lifts(now time.Time) []Lift {
	since := now.AddDate(0, 0, -30)
	byName := map[string]*Lift{}
	var order []string
	for _, w := range d.Workouts {
		for _, ex := range w.Exercises {
			best := 0.0
			for _, set := range doneSets(ex.Sets) {
				best = math.Max(best, E1RM(set))
			}
			if best == 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(ex.Name))
			lift, ok := byName[key]
			if !ok {
				lift = &Lift{Name: strings.TrimSpace(ex.Name)}
				byName[key] = lift
				order = append(order, key)
			}
			lift.Sessions++
			lift.Best = math.Max(lift.Best, best)
			if w.Started.Before(since) {
				lift.Before = math.Max(lift.Before, best)
			}
			if w.Started.After(lift.Last) {
				lift.Last = w.Started
			}
		}
	}
	lifts := make([]Lift, 0, len(order))
	for _, key := range order {
		lifts = append(lifts, *byName[key])
	}
	sort.SliceStable(lifts, func(i, j int) bool {
		if lifts[i].Sessions != lifts[j].Sessions {
			return lifts[i].Sessions > lifts[j].Sessions
		}
		return lifts[i].Last.After(lifts[j].Last)
	})
	return lifts
}

// ---------- changing a finished workout ----------

// Workout is a finished workout, by ID.
func (d Data) Workout(id string) (Workout, bool) {
	for _, w := range d.Workouts {
		if w.ID == id {
			return w, true
		}
	}
	return Workout{}, false
}

// ReplaceWorkout saves an edited finished workout (by ID), cleaned up, and keeps the list in
// date order (PRs and "last time" follow it). Its routine, day and task stay as they were.
func (d *Data) ReplaceWorkout(edited Workout) error {
	for i := range d.Workouts {
		if d.Workouts[i].ID != edited.ID {
			continue
		}
		cleaned, err := CleanWorkout(edited)
		if err != nil {
			return err
		}
		old := d.Workouts[i]
		cleaned.Routine, cleaned.Day, cleaned.Task = old.Routine, old.Day, old.Task
		d.Workouts[i] = cleaned
		sort.SliceStable(d.Workouts, func(a, b int) bool { return d.Workouts[a].Started.Before(d.Workouts[b].Started) })
		return nil
	}
	return problem.NotFoundf("that workout is gone")
}

// CleanWorkout trims an edited workout: sets without reps and exercises without a name or sets
// are dropped, and what's left counts as done. It must keep at least one set.
func CleanWorkout(w Workout) (Workout, error) {
	if w.Title = text.Truncate(text.Collapse(w.Title), 80); w.Title == "" {
		w.Title = "Workout"
	}
	w.Note = text.Truncate(text.Collapse(w.Note), 500)
	w.Minutes = clamp(w.Minutes, 0, 24*60)
	w.Ended = w.Started.Add(time.Duration(w.Minutes) * time.Minute)
	w.RestUntil, w.RestFor = time.Time{}, 0
	var exercises []Exercise
	for _, ex := range w.Exercises {
		if ex.Name = cleanName(ex.Name); ex.Name == "" {
			continue
		}
		var sets []Set
		for _, set := range ex.Sets {
			if !set.Skipped && set.Reps > 0 && set.Reps < 1000 && set.LB >= 0 && set.LB < 5000 && len(sets) < maxSets {
				sets = append(sets, Set{LB: math.Round(set.LB*1000) / 1000, Reps: set.Reps, Done: true,
					Note: text.Truncate(text.Collapse(set.Note), maxSetNote)})
			}
		}
		for _, set := range ex.Sets { // skipped sets ride along as they were
			if set.Skipped && !set.Done && len(sets) < maxSets {
				sets = append(sets, Set{Skipped: true, Why: text.Truncate(text.Collapse(set.Why), maxSetNote)})
			}
		}
		ex.Why = text.Truncate(text.Collapse(ex.Why), maxSetNote)
		if len(exercises) >= maxItems {
			continue
		}
		if len(doneSets(sets)) > 0 {
			ex.Sets, ex.Skipped, ex.Why = sets, false, ""
			exercises = append(exercises, ex)
		} else if ex.Skipped {
			ex.Sets = nil
			exercises = append(exercises, ex)
		}
	}
	if len(exercises) == 0 || (Workout{Exercises: exercises}).DoneSets() == 0 {
		return Workout{}, problem.Invalidf("a workout needs at least one set with reps (or delete it)")
	}
	w.Exercises = exercises
	return w, nil
}

// ShownLifts is the chart's lifts by slot: the ones picked, or the most-done ones.
func (d Data) ShownLifts(now time.Time) [ChartSlots]string {
	if d.Strength.Picked {
		return d.Strength.Lifts
	}
	var slots [ChartSlots]string
	for i, lift := range d.Lifts(now) {
		if i == 3 { // three by default: the light colours past them are harder to tell apart
			break
		}
		slots[i] = lift.Name
	}
	return slots
}

// PickLifts sets the chart's lifts. A lift already shown keeps its slot (its colour); new ones
// take the free slots in order; past ChartSlots are left out.
func (d *Data) PickLifts(names []string, now time.Time) {
	current := d.ShownLifts(now)
	var slots [ChartSlots]string
	var added []string
	for _, name := range names {
		name = cleanName(name)
		kept := false
		for i, old := range current {
			if old != "" && sameExercise(old, name) {
				slots[i], kept = old, true
			}
		}
		if !kept && name != "" {
			added = append(added, name)
		}
	}
	for _, name := range added {
		for i := range slots {
			if slots[i] == "" {
				slots[i] = name
				break
			}
		}
	}
	d.Strength.Picked, d.Strength.Lifts = true, slots
}

// Point is one session of a lift: its heaviest weight, its best estimated one-rep max and the
// set that gave it.
type Point struct {
	Day     time.Time
	Heavy   float64
	Best    float64
	BestSet Set
}

// Points are a lift's weighted sessions since a time (all with a zero time), oldest first.
func (d Data) Points(name string, since time.Time) []Point {
	var points []Point
	for _, w := range d.Workouts {
		if w.Started.Before(since) {
			continue
		}
		for _, ex := range w.Exercises {
			if !sameExercise(ex.Name, name) {
				continue
			}
			point := Point{Day: w.Started}
			for _, set := range doneSets(ex.Sets) {
				point.Heavy = math.Max(point.Heavy, set.LB)
				if e := E1RM(set); e > point.Best {
					point.Best, point.BestSet = e, set
				}
			}
			if point.Heavy > 0 {
				points = append(points, point)
			}
		}
	}
	return points
}

// ---------- export ----------

// CSVHeader is the first row of WriteCSV: one row per set, like other lifting apps export, so
// the history opens in a spreadsheet or moves to another app.
var CSVHeader = []string{"workout_id", "date", "start", "workout", "duration_min", "exercise", "set", "status",
	"weight_lb", "weight_kg", "reps", "est_1rm_lb", "set_note", "skip_reason", "workout_note"}

// WriteCSV writes every finished workout's sets, oldest first. A skipped exercise is one row
// with no set number; weights are blank for bodyweight sets.
func (d Data) WriteCSV(out io.Writer) error {
	w := csv.NewWriter(out)
	w.Write(CSVHeader)
	number := func(v float64) string {
		if v <= 0 {
			return ""
		}
		return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	}
	for _, workout := range d.Workouts {
		minutes := strconv.Itoa(int(math.Round(workout.Duration(workout.Ended).Minutes())))
		start := []string{workout.ID, workout.Started.Format("2006-01-02"), workout.Started.Format("15:04"), workout.Title, minutes}
		for _, ex := range workout.Exercises {
			if ex.Skipped {
				w.Write(append(append([]string{}, start...), ex.Name, "", "skipped", "", "", "", "", "", ex.Why, workout.Note))
				continue
			}
			for i, set := range ex.Sets {
				status, reps := "done", strconv.Itoa(set.Reps)
				if set.Skipped {
					status, reps = "skipped", ""
				}
				w.Write(append(append([]string{}, start...), ex.Name, strconv.Itoa(i+1), status,
					number(set.LB), number(set.LB*kgPerLB), reps, number(E1RM(set)), set.Note, set.Why, workout.Note))
			}
		}
	}
	w.Flush()
	return w.Error()
}

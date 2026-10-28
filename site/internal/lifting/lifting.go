// Package lifting is the Lifting tab's data: routines (days of planned exercises), the workout in
// progress and the finished ones, kept in lifting.json in the user's folder.
//
// Weights are kept in pounds; Unit only says how they're shown and typed. A weight of 0 is a
// bodyweight set (push-ups, pull-ups). A hold exercise (plank, dead hang) counts seconds instead
// of reps.
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
	Goals    []Goal    `json:"goals,omitempty"`   // one-rep max goals, one per lift
	// MyFitnessPal is whether finished workouts go to MyFitnessPal's exercise diary, and how.
	MyFitnessPal MyFitnessPal `json:"myfitnesspal,omitzero"`
}

// Goal is a one-rep max to reach on a lift: an actual single (one rep) at LB or more, never an
// estimate from a set of more reps.
type Goal struct {
	Name string  `json:"name"`
	LB   float64 `json:"lb"`
}

// Goal is the lift's one-rep max goal in pounds, or 0.
func (d Data) Goal(name string) float64 {
	for _, goal := range d.Goals {
		if sameExercise(goal.Name, name) {
			return goal.LB
		}
	}
	return 0
}

// SetGoal sets a lift's one-rep max goal (pounds), or clears it with 0.
func (d *Data) SetGoal(name string, lb float64) error {
	if name = cleanName(name); name == "" {
		return problem.Invalidf("name the lift")
	}
	if lb < 0 || lb > 2000 || math.IsNaN(lb) {
		return problem.Invalidf("a one-rep max goal is a weight up to 2000 lb")
	}
	d.Goals = slices.DeleteFunc(d.Goals, func(g Goal) bool { return sameExercise(g.Name, name) })
	if lb > 0 {
		if len(d.Goals) >= maxGoals {
			return problem.Invalidf("that's a lot of goals: clear one first")
		}
		d.Goals = append(d.Goals, Goal{Name: name, LB: math.Round(lb*100) / 100})
	}
	return nil
}

// OneRM is a lift's actual one-rep max: the heaviest single (a done set of one rep) in workouts
// that ended before `before` (any time when zero), and the day it was done.
func (d Data) OneRM(name string, before time.Time) (lb float64, day time.Time) {
	for _, w := range d.Workouts {
		if !before.IsZero() && !w.Ended.Before(before) {
			continue
		}
		for _, ex := range w.Exercises {
			if !sameExercise(ex.Name, name) {
				continue
			}
			if single := Single(ex.Sets); single > lb {
				lb, day = single, w.Started
			}
		}
	}
	return lb, day
}

// Single is the heaviest done set of exactly one rep, in pounds, or 0.
func Single(sets []Set) float64 {
	heaviest := 0.0
	for _, set := range doneSets(sets) {
		if set.Reps == 1 && set.LB > heaviest {
			heaviest = set.LB
		}
	}
	return heaviest
}

// GoalsReached names the lifts whose one-rep max goal a finished workout reached for the first
// time.
func (d Data) GoalsReached(w Workout) []string {
	var reached []string
	for _, ex := range w.Exercises {
		goal := d.Goal(ex.Name)
		if goal == 0 || slices.Contains(reached, ex.Name) {
			continue
		}
		if before, _ := d.OneRM(ex.Name, w.Started); Single(ex.Sets) >= goal && before < goal {
			reached = append(reached, ex.Name)
		}
	}
	return reached
}

// MyFitnessPal is the Lifting tab's link to MyFitnessPal's exercise diary.
type MyFitnessPal struct {
	Off bool `json:"off,omitempty"` // the finish panel's checkbox, unticked
	// Matches pairs exercise names (lower case) with MyFitnessPal's exercises.
	Matches map[string]Match `json:"matches,omitempty"`
}

// Match is the MyFitnessPal exercise a lift is logged as.
type Match struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MatchKey is how an exercise name is looked up in MyFitnessPal.Matches.
func MatchKey(name string) string { return strings.ToLower(cleanName(name)) }

// SetMatch remembers which MyFitnessPal exercise a lift is logged as.
func (d *Data) SetMatch(name string, match Match) {
	if d.MyFitnessPal.Matches == nil {
		d.MyFitnessPal.Matches = map[string]Match{}
	}
	d.MyFitnessPal.Matches[MatchKey(name)] = match
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
	Name     string   `json:"name"`
	Sets     int      `json:"sets"`
	Reps     string   `json:"reps,omitempty"` // "10", "8-12", "max"; a hold's seconds: "45"
	Hold     bool     `json:"hold,omitempty"` // held for Reps seconds instead of done for reps
	Rest     int      `json:"rest"`           // seconds between sets
	Optional bool     `json:"optional,omitempty"`
	Options  []string `json:"options,omitempty"`  // other exercises that can take its place
	Superset bool     `json:"superset,omitempty"` // done back to back with the next exercise
}

// Workout is a training session: in progress (Data.Active) or finished.
type Workout struct {
	Hold      *HoldTimer `json:"holdTimer,omitempty"` // the hold being timed
	ID        string     `json:"id"`
	Routine   string     `json:"routine,omitempty"` // the routine and day it came from; empty for a free one
	Day       string     `json:"day,omitempty"`
	Title     string     `json:"title"`
	Task      string     `json:"task,omitempty"`
	Started   time.Time  `json:"started"`
	Ended     time.Time  `json:"ended,omitzero"`
	Minutes   int        `json:"minutes,omitempty"` // how long it took, as confirmed when finishing
	RestUntil time.Time  `json:"restUntil,omitzero"`
	RestFor   int        `json:"restFor,omitempty"`   // seconds the current rest started with
	RestAfter string     `json:"restAfter,omitempty"` // the exercise whose rest it is
	Exercises []Exercise `json:"exercises"`
	Note      string     `json:"note,omitempty"`
	Sent      bool       `json:"sentToMyFitnessPal,omitempty"` // logged in MyFitnessPal's exercise diary
}

// HoldTimer is a hold set being timed: it counts down from For seconds.
type HoldTimer struct {
	Exercise int       `json:"exercise"`
	Set      int       `json:"set"`
	Started  time.Time `json:"started"`
	Until    time.Time `json:"until"`
	For      int       `json:"for"`
}

// Exercise is an exercise in a workout and its sets.
type Exercise struct {
	Name     string   `json:"name"`
	Target   string   `json:"target,omitempty"` // the planned reps (a hold's seconds)
	Hold     bool     `json:"hold,omitempty"`
	Rest     int      `json:"rest"`
	Sets     []Set    `json:"sets"`
	FromLast bool     `json:"fromLast,omitempty"` // its sets were filled in from the last session
	Skipped  bool     `json:"skipped,omitempty"`  // left out on purpose, maybe with a reason
	Why      string   `json:"why,omitempty"`
	Optional bool     `json:"optional,omitempty"`
	Options  []string `json:"options,omitempty"`  // exercises it can be swapped for
	Superset bool     `json:"superset,omitempty"` // done back to back with the next exercise
	Instead  string   `json:"instead,omitempty"`  // the planned exercise it took the place of
	SwapWhy  string   `json:"swapWhy,omitempty"`  // why it was swapped in
}

// Set is one set: done, or still a plan (pre-filled from last time, or as typed so far).
type Set struct {
	LB     float64 `json:"lb"`
	Reps   int     `json:"reps"`
	Secs   int     `json:"secs,omitempty"` // a hold's seconds (it has no reps)
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
	maxGoals    = 100
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
			if secs, ok := Seconds(ex.Reps); ok && (ex.Hold || secondsTarget.MatchString(ex.Reps)) {
				ex.Hold, ex.Reps = true, strconv.Itoa(secs)
			}
			ex.Rest = clamp(ex.Rest, 0, 3600)
			ex.Options = cleanOptions(ex.Options, ex.Name)
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
		if n := len(day.Exercises); n > 0 {
			day.Exercises[n-1].Superset = false // nothing after it to pair with
		}
		days = append(days, day)
	}
	routine.Days = days[:min(len(days), maxItems)]
	return routine
}

func cleanName(name string) string { return text.Truncate(text.Collapse(name), 80) }

// cleanOptions trims an exercise's alternatives: named, each once, not the exercise itself.
func cleanOptions(options []string, name string) []string {
	var kept []string
	for _, option := range options {
		option = cleanName(option)
		if option != "" && !sameExercise(option, name) && !slices.ContainsFunc(kept, func(k string) bool { return sameExercise(k, option) }) {
			kept = append(kept, option)
		}
	}
	return kept[:min(len(kept), 8)]
}

// SplitOptions reads alternatives as typed: "Cable row, DB row" (commas, "|" or ";").
func SplitOptions(typed string) []string {
	return strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '|' || r == ';' })
}

var (
	secondsTarget = regexp.MustCompile(`(?i)^\s*\d+\s*(s|sec|secs|seconds?)\s*$|^\s*\d+:\d\d\s*$`)
	clockTarget   = regexp.MustCompile(`^\s*(\d+):(\d\d)\s*$`)
)

// Seconds reads a hold's length as typed: "45", "45 s", "1:30". ok is false for anything else.
func Seconds(typed string) (int, bool) {
	if m := clockTarget.FindStringSubmatch(typed); m != nil {
		minutes, _ := strconv.Atoi(m[1])
		secs, _ := strconv.Atoi(m[2])
		return clamp(minutes*60+secs, 1, 3600), secs < 60
	}
	trimmed := strings.TrimSpace(typed)
	n := leadingNumber(trimmed)
	if n <= 0 || (strings.TrimLeft(trimmed, "0123456789") != "" && !secondsTarget.MatchString(trimmed)) {
		return 0, false
	}
	return clamp(n, 1, 3600), true
}

func clamp(n, low, high int) int { return max(low, min(n, high)) }

// Summary is a day's exercises in a line: supersets joined with "+", holds with their seconds,
// optional ones marked: "Bench press · Row + Face pull · Plank 45 s · Curls (optional)".
func (d Day) Summary() string {
	var line strings.Builder
	for i, ex := range d.Exercises {
		if i > 0 {
			if d.Exercises[i-1].Superset {
				line.WriteString(" + ")
			} else {
				line.WriteString(" · ")
			}
		}
		line.WriteString(ex.Name)
		if ex.Hold && ex.Reps != "" {
			line.WriteString(" " + ex.Reps + " s")
		}
		if ex.Optional {
			line.WriteString(" (optional)")
		}
	}
	return line.String()
}

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

// prefill makes a planned exercise's sets: last time's weights and reps (or seconds), set by
// set, or the planned ones. A target in seconds ("45 s") from before holds existed is a hold.
func (d Data) prefill(planned Planned) Exercise {
	last := d.LastSets(planned.Name)
	hold := planned.Hold || secondsTarget.MatchString(planned.Reps)
	target := planned.Reps
	if secs, ok := Seconds(planned.Reps); hold && ok {
		target = strconv.Itoa(secs)
	}
	exercise := Exercise{Name: planned.Name, Target: target, Hold: hold, Rest: planned.Rest, FromLast: len(last) > 0,
		Optional: planned.Optional, Options: planned.Options, Superset: planned.Superset}
	count := planned.Sets
	if count == 0 {
		count = max(len(last), 3)
	}
	for i := range count {
		set := firstSet(exercise)
		if len(last) > 0 {
			previous := last[min(i, len(last)-1)]
			set.LB = previous.LB
			if hold && previous.Secs > 0 {
				set.Secs = previous.Secs
			} else if !hold && previous.Reps > 0 {
				set.Reps = previous.Reps
			}
		}
		exercise.Sets = append(exercise.Sets, set)
	}
	return exercise
}

// firstSet is a new set of an exercise with nothing to go by: its planned reps or seconds.
func firstSet(ex Exercise) Set {
	if ex.Hold {
		secs, _ := Seconds(ex.Target)
		return Set{Secs: secs}
	}
	return Set{Reps: leadingNumber(ex.Target)}
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
	// done, undo, add-set, remove-set, add-exercise, remove-exercise, up, down, rest, skip-rest,
	// skip-set, unskip-set, skip-exercise, unskip-exercise, hold (start timing a set), hold-time,
	// stop-hold, swap (to Name), swap-to (Set is the option's index), superset (with the next one)
	Op       string
	Exercise int
	Set      int
	Name     string // for add-exercise and swap
	Seconds  int    // for rest and hold-time
	Why      string // for skip-set, skip-exercise and swap
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
		} else if i := slices.IndexFunc(w.Exercises, func(ex Exercise) bool { return ex.Name == w.RestAfter }); i >= 0 {
			// Time added to or taken off a rest that's going is kept for that exercise's next rests.
			ex := &w.Exercises[i]
			ex.Rest = clamp(ex.Rest+change.Seconds, 15, 3600)
		}
		w.RestUntil = w.RestUntil.Add(time.Duration(change.Seconds) * time.Second)
		left := int(w.RestUntil.Sub(now).Seconds())
		// The bar fills back up by what was added (and never shows more than is left).
		w.RestFor = max(left, w.RestFor+change.Seconds)
		if left <= 0 {
			w.RestUntil, w.RestFor = time.Time{}, 0
		}
		return nil
	case "skip-rest":
		w.RestUntil, w.RestFor = time.Time{}, 0
		return nil
	case "hold-time":
		if w.Hold == nil {
			return problem.Conflictf("no hold is being timed")
		}
		w.Hold.Until = w.Hold.Until.Add(time.Duration(change.Seconds) * time.Second)
		if w.Hold.Until.Before(now) {
			w.Hold.Until = now
		}
		w.Hold.For = max(1, int(w.Hold.Until.Sub(w.Hold.Started).Seconds()))
		return nil
	case "stop-hold":
		w.Hold = nil
		return nil
	}
	if change.Exercise < 0 || change.Exercise >= len(w.Exercises) {
		return problem.Invalidf("that exercise isn't in this workout")
	}
	ex := &w.Exercises[change.Exercise]
	switch change.Op {
	case "hold", "done", "undo", "skip-set", "unskip-set":
	default:
		w.Hold = nil // the sets may move: the hold's place would be wrong
	}
	switch change.Op {
	case "swap":
		return d.swap(change.Exercise, change.Name, change.Why)
	case "swap-to":
		if change.Set < 0 || change.Set >= len(ex.Options) {
			return problem.Invalidf("that choice isn't there")
		}
		return d.swap(change.Exercise, ex.Options[change.Set], change.Why)
	case "superset":
		if change.Exercise == len(w.Exercises)-1 {
			return problem.Invalidf("add the exercise to pair it with below it first")
		}
		ex.Superset = !ex.Superset
		return nil
	case "add-set":
		if len(ex.Sets) >= maxSets {
			return problem.Invalidf("that's a lot of sets")
		}
		set := firstSet(*ex)
		if len(ex.Sets) > 0 {
			last := ex.Sets[len(ex.Sets)-1]
			set = Set{LB: last.LB, Reps: last.Reps, Secs: last.Secs}
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
	holding := w.Hold != nil && w.Hold.Exercise == change.Exercise && w.Hold.Set == change.Set
	switch change.Op {
	case "hold":
		if !ex.Hold {
			return problem.Invalidf("that exercise is done for reps, not held")
		}
		secs := set.Secs
		if secs <= 0 {
			secs, _ = Seconds(ex.Target)
		}
		if secs <= 0 {
			return problem.Invalidf("type how many seconds to hold")
		}
		w.Hold = &HoldTimer{Exercise: change.Exercise, Set: change.Set, Started: now, Until: now.Add(time.Duration(secs) * time.Second), For: secs}
		w.RestUntil, w.RestFor = time.Time{}, 0
		return nil
	case "skip-set":
		set.Skipped, set.Done = true, false
		set.Why, set.Note = text.Truncate(text.Collapse(set.Note), maxSetNote), ""
		return nil
	case "unskip-set":
		set.Skipped, set.Note, set.Why = false, set.Why, ""
		return nil
	case "done":
		if holding { // stopped early, or the timer ran out: what was held counts
			end := now
			if w.Hold.Until.Before(now) {
				end = w.Hold.Until
			}
			set.Secs, w.Hold = max(1, int(math.Round(end.Sub(w.Hold.Started).Seconds()))), nil
		}
		switch {
		case ex.Hold && set.Secs <= 0:
			return problem.Invalidf("type how many seconds you held it")
		case !ex.Hold && set.Reps <= 0:
			return problem.Invalidf("type how many reps you did")
		}
		set.Done, set.Skipped = true, false
		// With no last session to go by, the sets after it start from this one (unless typed in).
		for i := change.Set + 1; i < len(ex.Sets) && !ex.FromLast; i++ {
			if next := &ex.Sets[i]; !next.Done && !next.Edited {
				next.LB, next.Reps, next.Secs = set.LB, set.Reps, set.Secs
			}
		}
		// In a superset the next exercise comes straight after; the rest is after the last one.
		if d.pairedWithNext(change.Exercise) {
			w.RestUntil, w.RestFor = time.Time{}, 0
		} else if ex.Rest > 0 {
			w.RestUntil, w.RestFor, w.RestAfter = now.Add(time.Duration(ex.Rest)*time.Second), ex.Rest, ex.Name
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

// pairedWithNext says whether an exercise of the workout is supersetted with the one after it
// (and that one isn't skipped).
func (d Data) pairedWithNext(e int) bool {
	w := d.Active
	return w.Exercises[e].Superset && e+1 < len(w.Exercises) && !w.Exercises[e+1].Skipped
}

// swap puts another exercise in an exercise's place, maybe saying why. Its sets are filled in
// from the new one's last session. Sets already done stay with the first exercise, and the new
// one follows it with the sets that were left. Swapping back to the planned one undoes the swap.
func (d *Data) swap(e int, name, why string) error {
	w := d.Active
	ex := w.Exercises[e]
	if name = cleanName(name); name == "" {
		return problem.Invalidf("pick or type the exercise to do instead")
	}
	for _, known := range d.ExerciseNames() {
		if sameExercise(known, name) {
			name = known
			break
		}
	}
	if sameExercise(name, ex.Name) {
		return nil
	}
	planned := ex.Instead
	if planned == "" {
		planned = ex.Name
	}
	var done, left []Set
	for _, set := range ex.Sets {
		if set.Done {
			done = append(done, set)
		} else if !set.Skipped {
			left = append(left, set)
		}
	}
	count := max(len(left), 1)
	if len(done) == 0 {
		count = max(len(ex.Sets), 1)
	}
	swapped := d.prefill(Planned{Name: name, Sets: count, Reps: ex.Target, Hold: ex.Hold, Rest: ex.Rest,
		Optional: ex.Optional, Superset: ex.Superset})
	// The choices stay the same, with the one swapped out among them so it's one tap back.
	swapped.Options = cleanOptions(append(append([]string{}, ex.Options...), ex.Name), name)
	if !sameExercise(name, planned) {
		swapped.Instead, swapped.SwapWhy = planned, text.Truncate(text.Collapse(why), maxSetNote)
	}
	if len(done) == 0 {
		w.Exercises[e] = swapped
		return nil
	}
	if len(w.Exercises) >= maxItems {
		return problem.Invalidf("that's a lot of exercises: finish this workout first")
	}
	w.Exercises[e].Sets, w.Exercises[e].Superset = done, false
	w.Exercises = slices.Insert(w.Exercises, e+1, swapped)
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
		ex := &d.Active.Exercises[e]
		set := &ex.Sets[s]
		// The page sends every input each time, so only a value that changed counts as typed
		// (weights within a quarter pound: kg shown to a tenth comes back a little off).
		if value.LB != nil && *value.LB >= 0 && *value.LB < 5000 && math.Abs(*value.LB-set.LB) > 0.25 {
			set.LB, set.Edited = math.Round(*value.LB*1000)/1000, true
		}
		// A hold's second box is its seconds.
		if value.Reps != nil && ex.Hold && *value.Reps >= 0 && *value.Reps <= 3600 && *value.Reps != set.Secs {
			set.Secs, set.Edited = *value.Reps, true
		} else if value.Reps != nil && !ex.Hold && *value.Reps >= 0 && *value.Reps < 1000 && *value.Reps != set.Reps {
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
	finished.RestUntil, finished.RestFor, finished.RestAfter, finished.Hold = time.Time{}, 0, "", nil
	finished.Note = text.Truncate(text.Collapse(note), 500)
	d.Workouts = append(d.Workouts, finished)
	if len(d.Workouts) > maxWorkouts {
		d.Workouts = d.Workouts[len(d.Workouts)-maxWorkouts:]
	}
	d.Active = nil
	return finished, nil
}

func (d *Data) Discard() { d.Active = nil }

// Summary is an exercise as one row of MyFitnessPal's exercise diary, which has one weight and
// rep count for all the sets: the heaviest set's (the most reps at that weight).
type Summary struct {
	Name       string
	Sets, Reps int
	LB         float64
}

// Summaries are the workout's exercises with done sets, as MyFitnessPal rows. Holds are left out:
// MyFitnessPal counts reps, not seconds.
func (w Workout) Summaries() []Summary {
	var rows []Summary
	for _, ex := range w.Exercises {
		if ex.Hold {
			continue
		}
		row := Summary{Name: ex.Name}
		for _, set := range doneSets(ex.Sets) {
			if set.Reps < 1 {
				continue
			}
			row.Sets++
			if set.LB > row.LB || set.LB == row.LB && set.Reps > row.Reps {
				row.LB, row.Reps = set.LB, set.Reps
			}
		}
		if row.Sets > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// MarkSent records that a finished workout is in MyFitnessPal.
func (d *Data) MarkSent(id string) {
	for i := range d.Workouts {
		if d.Workouts[i].ID == id {
			d.Workouts[i].Sent = true
		}
	}
}

// quickLogWindow is how soon after a finished workout a logged set still joins it.
const quickLogWindow = 2 * time.Hour

// LogSets logs sets of an exercise someone just did, as done: into the workout in progress
// (filling its sets still to do first), else into a workout that finished in the last two hours,
// else as a new finished workout of its own. It answers where they went.
func (d *Data) LogSets(name string, sets []Set, hold bool, now time.Time) (string, error) {
	if name = cleanName(name); name == "" {
		return "", problem.Invalidf("name the exercise")
	}
	for _, known := range d.ExerciseNames() {
		if sameExercise(known, name) {
			name = known
			break
		}
	}
	if len(sets) == 0 || len(sets) > maxSets {
		return "", problem.Invalidf("log 1 to 30 sets")
	}
	needs := "reps"
	if hold {
		needs = "seconds held"
	}
	for i, set := range sets {
		if set.LB < 0 || set.LB >= 5000 || (hold && (set.Secs <= 0 || set.Secs > 3600)) || (!hold && (set.Reps <= 0 || set.Reps >= 1000)) {
			return "", problem.Invalidf("each set needs a weight from 0 (bodyweight) and " + needs)
		}
		sets[i] = Set{LB: math.Round(set.LB*1000) / 1000, Reps: set.Reps, Secs: set.Secs, Done: true, Note: text.Truncate(text.Collapse(set.Note), maxSetNote)}
		if hold {
			sets[i].Reps = 0
		} else {
			sets[i].Secs = 0
		}
	}
	if w := d.Active; w != nil {
		e := slices.IndexFunc(w.Exercises, func(ex Exercise) bool { return sameExercise(ex.Name, name) && !ex.Skipped })
		if e < 0 {
			if len(w.Exercises) >= maxItems {
				return "", problem.Invalidf("that's a lot of exercises: finish this workout first")
			}
			w.Exercises = append(w.Exercises, Exercise{Name: name, Hold: hold, Rest: DefaultRest})
			e = len(w.Exercises) - 1
		}
		ex := &w.Exercises[e]
		for _, set := range sets {
			if i := slices.IndexFunc(ex.Sets, func(s Set) bool { return !s.Done && !s.Skipped }); i >= 0 {
				ex.Sets[i] = set
			} else if len(ex.Sets) < maxSets {
				ex.Sets = append(ex.Sets, set)
			}
		}
		return "the workout in progress (" + w.Title + ")", nil
	}
	if n := len(d.Workouts); n > 0 && !d.Workouts[n-1].Ended.After(now) && now.Sub(d.Workouts[n-1].Ended) < quickLogWindow {
		w := &d.Workouts[n-1]
		e := slices.IndexFunc(w.Exercises, func(ex Exercise) bool { return sameExercise(ex.Name, name) && !ex.Skipped })
		if e < 0 {
			w.Exercises = append(w.Exercises, Exercise{Name: name, Hold: hold, Rest: DefaultRest})
			e = len(w.Exercises) - 1
		}
		w.Exercises[e].Sets = append(w.Exercises[e].Sets, sets...)
		return "the workout “" + w.Title + "” that finished at " + w.Ended.Format("15:04"), nil
	}
	d.Workouts = append(d.Workouts, Workout{ID: newID(), Title: "Quick log", Started: now, Ended: now,
		Exercises: []Exercise{{Name: name, Hold: hold, Rest: DefaultRest, Sets: sets}}})
	return "a new workout, “Quick log”", nil
}

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
	Single  float64 // the heaviest single (an actual one-rep max), pounds; 0 without one
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
			session := Session{Workout: w.ID, Day: w.Started, Title: w.Title, Sets: doneSets(ex.Sets), Single: Single(ex.Sets)}
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
	if secs, ok := Seconds(planned.Reps); ok && secondsTarget.MatchString(planned.Reps) {
		planned.Hold, planned.Reps = true, strconv.Itoa(secs)
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
	w.RestUntil, w.RestFor, w.Hold = time.Time{}, 0, nil
	var exercises []Exercise
	for _, ex := range w.Exercises {
		if ex.Name = cleanName(ex.Name); ex.Name == "" {
			continue
		}
		ex.Instead, ex.SwapWhy = cleanName(ex.Instead), text.Truncate(text.Collapse(ex.SwapWhy), maxSetNote)
		ex.Options = cleanOptions(ex.Options, ex.Name)
		var sets []Set
		for _, set := range ex.Sets {
			counted := (!ex.Hold && set.Reps > 0 && set.Reps < 1000) || (ex.Hold && set.Secs > 0 && set.Secs <= 3600)
			if !set.Skipped && counted && set.LB >= 0 && set.LB < 5000 && len(sets) < maxSets {
				kept := Set{LB: math.Round(set.LB*1000) / 1000, Done: true, Note: text.Truncate(text.Collapse(set.Note), maxSetNote)}
				if ex.Hold {
					kept.Secs = set.Secs
				} else {
					kept.Reps = set.Reps
				}
				sets = append(sets, kept)
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
		return Workout{}, problem.Invalidf("a workout needs at least one set with reps or seconds (or delete it)")
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
	Single  float64 // the heaviest single: an actual one-rep max
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
			point := Point{Day: w.Started, Single: Single(ex.Sets)}
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
	"weight_lb", "weight_kg", "reps", "est_1rm_lb", "set_note", "skip_reason", "workout_note", "hold_s", "instead_of", "swap_reason"}

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
				w.Write(append(append([]string{}, start...), ex.Name, "", "skipped", "", "", "", "", "", ex.Why, workout.Note, "", ex.Instead, ex.SwapWhy))
				continue
			}
			for i, set := range ex.Sets {
				status, reps, secs := "done", "", ""
				if set.Reps > 0 {
					reps = strconv.Itoa(set.Reps)
				}
				if set.Secs > 0 {
					secs = strconv.Itoa(set.Secs)
				}
				if set.Skipped {
					status, reps, secs = "skipped", "", ""
				}
				w.Write(append(append([]string{}, start...), ex.Name, strconv.Itoa(i+1), status,
					number(set.LB), number(set.LB*kgPerLB), reps, number(E1RM(set)), set.Note, set.Why, workout.Note, secs, ex.Instead, ex.SwapWhy))
			}
		}
	}
	w.Flush()
	return w.Error()
}

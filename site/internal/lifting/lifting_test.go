package lifting

import (
	"math"
	"strings"
	"testing"
	"time"
)

var monday = time.Date(2026, 10, 19, 18, 0, 0, 0, time.Local)

func routineData() Data {
	d := Data{Unit: "lb"}
	d.SaveRoutine(Routine{Name: "Push / Pull", Days: []Day{{Name: "Push", Task: "Routine 1: Push",
		Exercises: []Planned{{Name: "Bench press", Sets: 3, Reps: "8-12", Rest: 120}, {Name: " ", Sets: 3}}}}})
	return d
}

func start(t *testing.T, d *Data, at time.Time) {
	t.Helper()
	r := d.Routines[0]
	if err := d.Start(r.ID, r.Days[0].ID, at); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRoutineCleansUp(t *testing.T) {
	d := routineData()
	day := d.Routines[0].Days[0]
	if day.ID == "" || len(day.Exercises) != 1 {
		t.Fatalf("day %+v: want an id and the unnamed exercise dropped", day)
	}
	if _, found, ok := d.DayForTask("routine 1: push "); !ok || found.ID != day.ID {
		t.Fatalf("the task doesn't find its day")
	}
}

func TestStartFillsInLastTime(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	sets := d.Active.Exercises[0].Sets
	if len(sets) != 3 || sets[0].Reps != 8 || sets[0].LB != 0 {
		t.Fatalf("first time: %+v, want 3 sets of the target's 8 reps", sets)
	}
	lb, reps := 135.0, 10
	d.SetTyped(map[[2]int]Typed{{0, 0}: {LB: &lb, Reps: &reps}, {0, 1}: {LB: &lb}})
	for s := range 2 {
		if err := d.Apply(Change{Op: "done", Exercise: 0, Set: s}, monday.Add(time.Duration(s)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if want := monday.Add(time.Minute + 120*time.Second); !d.Active.RestUntil.Equal(want) {
		t.Errorf("rest until %v, want %v", d.Active.RestUntil, want)
	}
	if _, err := d.Finish(monday.Add(40*time.Minute), 40, ""); err != nil {
		t.Fatal(err)
	}
	if got := d.Workouts[0].Exercises[0].Sets; len(got) != 2 {
		t.Fatalf("finished with %d sets, want only the 2 done", len(got))
	}
	start(t, &d, monday.AddDate(0, 0, 7))
	sets = d.Active.Exercises[0].Sets
	if len(sets) != 3 || sets[0] != (Set{LB: 135, Reps: 10}) || sets[1] != (Set{LB: 135, Reps: 8}) || sets[2] != (Set{LB: 135, Reps: 8}) {
		t.Fatalf("second time: %+v, want last time's sets (the last one repeated)", sets)
	}
}

func TestRecords(t *testing.T) {
	d := routineData()
	for i, set := range []Set{{LB: 135, Reps: 8}, {LB: 145, Reps: 8}} {
		start(t, &d, monday.AddDate(0, 0, 7*i))
		d.Active.Exercises[0].Sets = []Set{{LB: set.LB, Reps: set.Reps, Done: true}}
		d.Finish(monday.AddDate(0, 0, 7*i).Add(time.Hour), 60, "")
	}
	history := d.History("bench press")
	if len(history) != 2 || history[0].Record || !history[1].Record {
		t.Fatalf("history %+v: want only the heavier second session marked", history)
	}
	if got := d.Records(d.Workouts[1]); !got[[2]int{0, 0}] {
		t.Errorf("records %v: want the 145 × 8 set", got)
	}
}

func TestFinishNeedsASet(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	if _, err := d.Finish(monday, 10, ""); err == nil {
		t.Fatal("finished a workout with no sets done")
	}
	if d.Active == nil {
		t.Fatal("the workout was lost")
	}
}

func TestUnits(t *testing.T) {
	if got := FromShown(100, "kg"); FormatShown(got, "kg") != "100" || FormatShown(got, "lb") != "220.5" {
		t.Errorf("100 kg came back as %s kg, %s lb", FormatShown(got, "kg"), FormatShown(got, "lb"))
	}
	if E1RM(Set{LB: 100, Reps: 1}) != 100 || E1RM(Set{LB: 0, Reps: 12}) != 0 {
		t.Error("e1RM of a single or a bodyweight set")
	}
}

func TestParsePlanned(t *testing.T) {
	cases := map[string]Planned{
		"3 × 10":     {Sets: 3, Reps: "10"},
		"4x8-12":     {Sets: 4, Reps: "8-12"},
		"3 × max":    {Sets: 3, Reps: "max"},
		"to failure": {Sets: 3, Reps: "to failure"},
	}
	for target, want := range cases {
		got := ParsePlanned("x", target)
		if got.Sets != want.Sets || got.Reps != want.Reps {
			t.Errorf("%q: %d × %q, want %d × %q", target, got.Sets, got.Reps, want.Sets, want.Reps)
		}
	}
}

// A first-time exercise: logging a set fills the sets after it with its weight and reps,
// except one typed in. With a last session, the sets keep last time's numbers.
func TestDoneFillsTheSetsAfter(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	lb, reps, mine := 95.0, 12, 6
	d.SetTyped(map[[2]int]Typed{{0, 0}: {LB: &lb, Reps: &reps}, {0, 2}: {Reps: &mine}})
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday)
	got := d.Active.Exercises[0].Sets
	if got[1] != (Set{LB: 95, Reps: 12}) || got[2].LB != 0 || got[2].Reps != 6 {
		t.Fatalf("first time: %+v", got)
	}
	d.Apply(Change{Op: "done", Exercise: 0, Set: 1}, monday)
	d.Finish(monday.Add(time.Hour), 60, "")
	if d.Workouts[0].Exercises[0].Sets[0].Edited {
		t.Error("a finished set keeps the typing flag")
	}

	start(t, &d, monday.AddDate(0, 0, 7))
	heavier := 105.0
	d.SetTyped(map[[2]int]Typed{{0, 0}: {LB: &heavier}})
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday.AddDate(0, 0, 7))
	if got := d.Active.Exercises[0].Sets; got[1].LB != 95 || got[1].Reps != 12 {
		t.Fatalf("second time, set 2 should stay last time's 95 × 12: %+v", got)
	}
}

// The page posts every input on each change: unchanged values aren't "typed in".
func TestUnchangedInputsArentTyped(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	reps := 8
	kgBack := FromShown(Shown(0, "kg"), "kg")
	d.SetTyped(map[[2]int]Typed{{0, 1}: {LB: &kgBack, Reps: &reps}})
	if d.Active.Exercises[0].Sets[1].Edited {
		t.Error("an unchanged set counted as typed")
	}
}

// 5 reps at 225: Epley says 262.5, Brzycki 253.1.
func TestOneRepMaxFormulas(t *testing.T) {
	if got := E1RM(Set{LB: 225, Reps: 5}); got != 262.5 {
		t.Errorf("Epley: %v", got)
	}
	if got := math.Round(Brzycki(225, 5)*10) / 10; got != 253.1 {
		t.Errorf("Brzycki: %v", got)
	}
	if got := WeightFor(262.5, 5); math.Abs(got-225) > 1e-9 {
		t.Errorf("back to 5 reps: %v", got)
	}
}

func TestLifts(t *testing.T) {
	d := routineData()
	now := monday.AddDate(0, 0, 60)
	for _, w := range []struct {
		at  time.Time
		set Set
	}{{monday, Set{LB: 135, Reps: 5}}, {now.AddDate(0, 0, -3), Set{LB: 155, Reps: 5}}} {
		start(t, &d, w.at)
		d.Active.Exercises[0].Sets = []Set{{LB: w.set.LB, Reps: w.set.Reps, Done: true}}
		d.Finish(w.at.Add(time.Hour), 60, "")
	}
	lifts := d.Lifts(now)
	if len(lifts) != 1 || lifts[0].Sessions != 2 || lifts[0].Best != E1RM(Set{LB: 155, Reps: 5}) {
		t.Fatalf("lifts %+v", lifts)
	}
	if got := lifts[0].Change(); math.Abs(got-20*(1+5.0/30)) > 0.001 {
		t.Errorf("change over 30 days: %v", got)
	}
}

func TestReplaceWorkout(t *testing.T) {
	d := routineData()
	for i := range 2 {
		start(t, &d, monday.AddDate(0, 0, 7*i))
		d.Active.Exercises[0].Sets = []Set{{LB: 135 + float64(10*i), Reps: 8, Done: true}}
		d.Finish(monday.AddDate(0, 0, 7*i).Add(time.Hour), 60, "")
	}
	edited := d.Workouts[1]
	edited.Started = monday.AddDate(0, 0, -7) // it was really the week before the first
	edited.Minutes = 45
	edited.Exercises = []Exercise{{Name: "Bench press", Sets: []Set{{LB: 150, Reps: 6}, {LB: 150, Reps: 0}}}, {Name: "Dips"}}
	if err := d.ReplaceWorkout(edited); err != nil {
		t.Fatal(err)
	}
	moved := d.Workouts[0]
	if moved.ID != edited.ID || moved.Task != "Routine 1: Push" || len(moved.Exercises) != 1 || len(moved.Exercises[0].Sets) != 1 {
		t.Fatalf("after the edit: %+v", d.Workouts)
	}
	if !moved.Exercises[0].Sets[0].Done || !moved.Ended.Equal(moved.Started.Add(45*time.Minute)) {
		t.Errorf("edited workout %+v", moved)
	}
	if last := d.LastSets("bench press"); last[0].LB != 135 {
		t.Errorf("last time is now the later workout's 135, got %v", last)
	}
	edited.Exercises = []Exercise{{Name: "Bench press"}}
	if err := d.ReplaceWorkout(edited); err == nil {
		t.Error("saved a workout with no sets")
	}
}

func TestPickLiftsKeepsColours(t *testing.T) {
	d := Data{}
	d.PickLifts([]string{"Squat", "Bench", "Row"}, monday)
	d.PickLifts([]string{"squat", "Row", "Deadlift"}, monday) // Bench is dropped
	if d.Strength.Lifts != [ChartSlots]string{"Squat", "Deadlift", "Row", ""} {
		t.Errorf("slots %q: Squat and Row keep theirs, Deadlift takes the free one", d.Strength.Lifts)
	}
}

func TestPoints(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	d.Active.Exercises[0].Sets = []Set{{LB: 135, Reps: 10, Done: true}, {LB: 155, Reps: 3, Done: true}, {LB: 200, Reps: 5}}
	d.Finish(monday.Add(time.Hour), 60, "")
	points := d.Points("Bench press", time.Time{})
	if len(points) != 1 || points[0].Heavy != 155 || points[0].BestSet.LB != 135 {
		t.Fatalf("points %+v: heaviest 155, best estimate from 135 × 10 (180 > 170.5)", points)
	}
}

func TestRoundToPlate(t *testing.T) {
	cases := []struct {
		lb   float64
		unit string
		want string
	}{{262.5, "lb", "262.5"}, {253.1, "lb", "252.5"}, {246.1, "lb", "245"}, {231.6, "lb", "232.5"}, {FromShown(100.7, "kg"), "kg", "101.25"}}
	for _, c := range cases {
		if got := FormatPlate(c.lb, c.unit); got != c.want {
			t.Errorf("%v %s: %s, want %s", c.lb, c.unit, got, c.want)
		}
	}
}

func TestSetNotes(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	note, reps := "  felt   easy ", 8
	d.SetTyped(map[[2]int]Typed{{0, 0}: {Note: &note, Reps: &reps}})
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday)
	if got := d.Active.Exercises[0].Sets; got[0].Note != "felt easy" || got[1].Note != "" {
		t.Fatalf("notes %+v: the note is the set's own, not carried", got)
	}
	d.Finish(monday.Add(time.Hour), 60, "")
	if d.Workouts[0].Exercises[0].Sets[0].Note != "felt easy" {
		t.Error("the note was lost when finishing")
	}
}

func TestSkipping(t *testing.T) {
	d := routineData()
	d.SaveRoutine(Routine{ID: d.Routines[0].ID, Name: "Push", Days: []Day{{ID: d.Routines[0].Days[0].ID, Name: "Push",
		Exercises: []Planned{{Name: "Bench press", Sets: 3, Reps: "8", Rest: 90}, {Name: "Dips", Sets: 3, Reps: "10"}}}}})
	start(t, &d, monday)
	lb := 135.0
	d.SetTyped(map[[2]int]Typed{{0, 0}: {LB: &lb}})
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday)
	why := "elbow"
	d.SetTyped(map[[2]int]Typed{{0, 2}: {Note: &why}})
	d.Apply(Change{Op: "skip-set", Exercise: 0, Set: 2}, monday)
	d.Apply(Change{Op: "skip-exercise", Exercise: 1, Why: "no dip bars"}, monday)
	if set := d.Active.Exercises[0].Sets[2]; !set.Skipped || set.Why != "elbow" || set.Note != "" {
		t.Fatalf("skipped set %+v", set)
	}
	w, err := d.Finish(monday.Add(time.Hour), 60, "")
	if err != nil {
		t.Fatal(err)
	}
	bench, dips := w.Exercises[0], w.Exercises[1]
	if len(bench.Sets) != 2 || !bench.Sets[1].Skipped || bench.Sets[1].Why != "elbow" {
		t.Errorf("bench kept %+v: the done set and the skipped one, not the untouched one", bench.Sets)
	}
	if !dips.Skipped || dips.Why != "no dip bars" || len(dips.Sets) != 0 {
		t.Errorf("dips %+v", dips)
	}
	if w.DoneSets() != 1 || len(d.LastSets("Bench press")) != 1 || len(d.History("Dips")) != 0 {
		t.Error("skipped sets count as done somewhere")
	}
	cleaned, err := CleanWorkout(w)
	if err != nil || len(cleaned.Exercises) != 2 || !cleaned.Exercises[0].Sets[1].Skipped || !cleaned.Exercises[1].Skipped {
		t.Errorf("editing drops the skips: %+v, %v", cleaned.Exercises, err)
	}
}

func TestWriteCSV(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	d.Active.Exercises[0].Sets = []Set{{LB: 225, Reps: 5, Done: true, Note: "easy, fast"}, {Skipped: true, Why: "elbow"}}
	d.Active.Exercises = append(d.Active.Exercises, Exercise{Name: "Dips", Skipped: true, Why: "no bars"})
	d.Finish(monday.Add(45*time.Minute), 45, "")
	var out strings.Builder
	if err := d.WriteCSV(&out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{
		strings.Join(CSVHeader, ","),
		d.Workouts[0].ID + `,2026-10-19,18:00,Push,45,Bench press,1,done,225,102.06,5,262.5,"easy, fast",,,,,`,
		d.Workouts[0].ID + `,2026-10-19,18:00,Push,45,Bench press,2,skipped,,,,,,elbow,,,,`,
		d.Workouts[0].ID + `,2026-10-19,18:00,Push,45,Dips,,skipped,,,,,,no bars,,,,`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got\n%s", out.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i, lines[i], want[i])
		}
	}
}

func TestSeconds(t *testing.T) {
	for typed, want := range map[string]int{"45": 45, "45 s": 45, "45s": 45, "60 sec": 60, "1:30": 90} {
		if got, ok := Seconds(typed); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", typed, got, ok, want)
		}
	}
	for _, typed := range []string{"", "max", "8-12", "1:75"} {
		if _, ok := Seconds(typed); ok {
			t.Errorf("%q shouldn't read as seconds", typed)
		}
	}
	if p := ParsePlanned("Plank", "3 × 45 s"); !p.Hold || p.Reps != "45" || p.Sets != 3 {
		t.Errorf("a plan's hold: %+v", p)
	}
}

func holdData() Data {
	d := Data{Unit: "lb"}
	d.SaveRoutine(Routine{Name: "Core", Days: []Day{{Name: "Core", Exercises: []Planned{
		{Name: "Plank", Sets: 2, Reps: "1:00", Hold: true, Rest: 60},
		{Name: "Curl", Sets: 2, Reps: "10", Rest: 60, Superset: true, Options: []string{"Hammer curl", "curl", " "}},
		{Name: "Pushdown", Sets: 2, Reps: "12", Rest: 90, Optional: true, Superset: true},
	}}}})
	return d
}

func TestRoutineFeaturesAreCleaned(t *testing.T) {
	d := holdData()
	ex := d.Routines[0].Days[0].Exercises
	if !ex[0].Hold || ex[0].Reps != "60" {
		t.Errorf("hold: %+v", ex[0])
	}
	if len(ex[1].Options) != 1 || ex[1].Options[0] != "Hammer curl" {
		t.Errorf("options should drop blanks and the exercise itself: %q", ex[1].Options)
	}
	if ex[2].Superset {
		t.Error("the last exercise has nothing to superset with")
	}
	if got := d.Routines[0].Days[0].Summary(); got != "Plank 60 s · Curl + Pushdown (optional)" {
		t.Errorf("summary: %q", got)
	}
}

func TestHoldTimer(t *testing.T) {
	d := holdData()
	start(t, &d, monday)
	if set := d.Active.Exercises[0].Sets[0]; set.Secs != 60 || set.Reps != 0 {
		t.Fatalf("a hold starts at its planned seconds: %+v", set)
	}
	if err := d.Apply(Change{Op: "hold", Exercise: 0, Set: 0}, monday); err != nil || d.Active.Hold == nil {
		t.Fatalf("hold: %v", err)
	}
	d.Apply(Change{Op: "hold-time", Seconds: 15}, monday.Add(time.Second))
	if d.Active.Hold.For != 75 {
		t.Errorf("adding time: %d s, want 75", d.Active.Hold.For)
	}
	// Stopped after 40 seconds: 40 are logged, the next set starts from it, and the rest starts.
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday.Add(40*time.Second))
	ex := d.Active.Exercises[0]
	if !ex.Sets[0].Done || ex.Sets[0].Secs != 40 || d.Active.Hold != nil || d.Active.RestUntil.IsZero() {
		t.Errorf("stopping early: %+v, hold %+v", ex.Sets[0], d.Active.Hold)
	}
	// Running out logs the full time, however late Done comes.
	d.Apply(Change{Op: "hold", Exercise: 0, Set: 1}, monday.Add(time.Minute))
	d.Apply(Change{Op: "done", Exercise: 0, Set: 1}, monday.Add(5*time.Minute))
	if got := d.Active.Exercises[0].Sets[1].Secs; got != 40 {
		t.Errorf("ran out: %d s, want the 40 it started from", got)
	}
	finished, err := d.Finish(monday.Add(10*time.Minute), 10, "")
	if err != nil || finished.DoneSets() != 2 {
		t.Fatalf("finish: %v", err)
	}
	cleaned, err := CleanWorkout(finished)
	if err != nil || cleaned.Exercises[0].Sets[0].Secs != 40 {
		t.Errorf("editing keeps the seconds: %+v %v", cleaned.Exercises, err)
	}
	start(t, &d, monday.AddDate(0, 0, 1))
	if got := d.Active.Exercises[0].Sets[0].Secs; got != 40 {
		t.Errorf("next time starts from last time's hold: %d", got)
	}
}

func TestSupersetRests(t *testing.T) {
	d := holdData()
	start(t, &d, monday)
	d.Apply(Change{Op: "done", Exercise: 1, Set: 0}, monday)
	if !d.Active.RestUntil.IsZero() {
		t.Error("the first exercise of a superset goes straight on to the next")
	}
	d.Apply(Change{Op: "done", Exercise: 2, Set: 0}, monday)
	if d.Active.RestUntil.IsZero() {
		t.Error("the rest comes after the superset's last exercise")
	}
	d.Apply(Change{Op: "superset", Exercise: 1}, monday)
	d.Apply(Change{Op: "done", Exercise: 1, Set: 1}, monday)
	if d.Active.RestUntil.IsZero() {
		t.Error("unpaired, it rests as usual")
	}
}

func TestSwap(t *testing.T) {
	d := holdData()
	start(t, &d, monday)
	if err := d.Apply(Change{Op: "swap-to", Exercise: 1, Set: 0, Why: "bar taken"}, monday); err != nil {
		t.Fatal(err)
	}
	ex := d.Active.Exercises[1]
	if ex.Name != "Hammer curl" || ex.Instead != "Curl" || ex.SwapWhy != "bar taken" || !ex.Superset || len(ex.Sets) != 2 {
		t.Fatalf("swapped: %+v", ex)
	}
	if len(ex.Options) != 1 || ex.Options[0] != "Curl" {
		t.Errorf("the planned one should be a choice back: %q", ex.Options)
	}
	d.Apply(Change{Op: "swap", Exercise: 1, Name: "curl"}, monday)
	if ex := d.Active.Exercises[1]; ex.Name != "Curl" || ex.Instead != "" || ex.SwapWhy != "" {
		t.Errorf("swapping back undoes it: %+v", ex)
	}
	// With a set done, the done set stays and the new exercise follows with what's left.
	d.Apply(Change{Op: "done", Exercise: 1, Set: 0}, monday)
	d.Apply(Change{Op: "swap", Exercise: 1, Name: "Cable curl", Why: "elbow"}, monday)
	if len(d.Active.Exercises) != 4 || len(d.Active.Exercises[1].Sets) != 1 || d.Active.Exercises[2].Name != "Cable curl" ||
		len(d.Active.Exercises[2].Sets) != 1 || d.Active.Exercises[1].Superset || !d.Active.Exercises[2].Superset {
		t.Errorf("split: %+v", d.Active.Exercises)
	}
}

func TestOptionalCarriesOver(t *testing.T) {
	d := holdData()
	start(t, &d, monday)
	if !d.Active.Exercises[2].Optional {
		t.Error("the workout should know the exercise is optional")
	}
	d.Apply(Change{Op: "done", Exercise: 1, Set: 0}, monday)
	finished, _ := d.Finish(monday.Add(time.Hour), 60, "")
	for _, ex := range finished.Exercises {
		if ex.Name == "Pushdown" {
			t.Error("an optional exercise left untouched isn't saved")
		}
	}
}

func TestSummaries(t *testing.T) {
	w := Workout{Exercises: []Exercise{
		{Name: "Bench", Sets: []Set{{LB: 135, Reps: 10, Done: true}, {LB: 155, Reps: 6, Done: true}, {LB: 155, Reps: 8, Done: true}, {LB: 185, Reps: 5}}},
		{Name: "Plank", Hold: true, Sets: []Set{{Secs: 60, Done: true}}},
		{Name: "Pull-up", Sets: []Set{{Reps: 8, Done: true}, {Reps: 6, Done: true}}},
		{Name: "Dips", Skipped: true},
	}}
	got := w.Summaries()
	want := []Summary{{Name: "Bench", Sets: 3, Reps: 8, LB: 155}, {Name: "Pull-up", Sets: 2, Reps: 8}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
}

func TestOneRMAndGoals(t *testing.T) {
	day := time.Date(2026, 1, 5, 18, 0, 0, 0, time.UTC)
	d := Data{Workouts: []Workout{
		{ID: "a", Started: day, Ended: day.Add(time.Hour), Exercises: []Exercise{{Name: "Squat", Sets: []Set{{LB: 200, Reps: 5, Done: true}, {LB: 205, Reps: 1, Done: true}, {LB: 215, Reps: 1}}}}},
	}}
	if lb, _ := d.OneRM("squat", time.Time{}); lb != 205 {
		t.Fatalf("one-rep max %v: estimates and undone sets don't count", lb)
	}
	if err := d.SetGoal("Squat", 225); err != nil || d.Goal("SQUAT") != 225 {
		t.Fatalf("goal %v, %v", d.Goal("squat"), err)
	}
	next := Workout{ID: "b", Started: day.AddDate(0, 0, 3), Ended: day.AddDate(0, 0, 3).Add(time.Hour),
		Exercises: []Exercise{{Name: "squat", Sets: []Set{{LB: 220, Reps: 3, Done: true}}}}}
	if reached := d.GoalsReached(next); len(reached) != 0 {
		t.Fatalf("an estimate reached the goal: %v", reached)
	}
	next.Exercises[0].Sets = append(next.Exercises[0].Sets, Set{LB: 225, Reps: 1, Done: true})
	d.Workouts = append(d.Workouts, next)
	if reached := d.GoalsReached(next); len(reached) != 1 {
		t.Fatalf("a 225 single didn't reach the goal: %v", reached)
	}
	later := Workout{Started: day.AddDate(0, 0, 7), Exercises: []Exercise{{Name: "Squat", Sets: []Set{{LB: 230, Reps: 1, Done: true}}}}}
	if reached := d.GoalsReached(later); len(reached) != 0 {
		t.Fatal("a goal is reached once")
	}
	if err := d.SetGoal("squat", 0); err != nil || len(d.Goals) != 0 {
		t.Fatalf("clearing left %v", d.Goals)
	}
}

func TestBumpedRestIsKept(t *testing.T) {
	d := routineData()
	start(t, &d, monday)
	reps := 10
	d.SetTyped(map[[2]int]Typed{{0, 0}: {Reps: &reps}, {0, 1}: {Reps: &reps}})
	d.Apply(Change{Op: "done", Exercise: 0, Set: 0}, monday)
	d.Apply(Change{Op: "rest", Seconds: 30}, monday.Add(10*time.Second))
	if got := d.Active.Exercises[0].Rest; got != 150 {
		t.Fatalf("rest after +30s: %d, want 150", got)
	}
	d.Apply(Change{Op: "done", Exercise: 0, Set: 1}, monday.Add(5*time.Minute))
	if got := d.Active.RestFor; got != 150 {
		t.Fatalf("next rest: %d s, want 150", got)
	}
	// Once the rest is over, adding time starts a short one and leaves the exercise's rest.
	d.Apply(Change{Op: "rest", Seconds: 30}, monday.Add(10*time.Minute))
	if got := d.Active.Exercises[0].Rest; got != 150 {
		t.Fatalf("rest after a bump once it was over: %d, want 150", got)
	}
}

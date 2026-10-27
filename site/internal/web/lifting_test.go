package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"dashboard/internal/lifting"
)

func liftingData() lifting.Data {
	d := lifting.Data{Unit: "lb"}
	d.SaveRoutine(lifting.Routine{Name: "Push / Pull", Days: []lifting.Day{{Name: "Push", Task: "Routine 1: Push",
		Exercises: []lifting.Planned{{Name: "Bench press", Sets: 3, Reps: "8", Rest: 90}}}}})
	return d
}

func TestLiftingTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	now := time.Now()
	d := liftingData()
	idle := liftingPageView{Unit: "lb", Workout: workoutViewOf(d, now), Routines: routinesViewOf(d),
		Progress: progressViewOf(d, ""), History: liftingHistoryOf(d, 0)}
	r := d.Routines[0]
	for week := range 2 {
		d.Start(r.ID, r.Days[0].ID, now.AddDate(0, 0, week*7-14))
		lb := 135.0 + float64(week)*10
		d.SetTyped(map[[2]int]lifting.Typed{{0, 0}: {LB: &lb}})
		d.Apply(lifting.Change{Op: "done"}, now)
		d.Finish(now.AddDate(0, 0, week*7-14).Add(time.Hour), 60, "felt good")
	}
	d.Start(r.ID, r.Days[0].ID, now)
	d.Apply(lifting.Change{Op: "done"}, now)
	going := liftingPageView{Unit: "lb", Workout: workoutViewOf(d, now), Routines: routinesViewOf(d),
		Progress: progressViewOf(d, ""), History: liftingHistoryOf(d, 0)}
	cases := []struct {
		name string
		view any
		want string
	}{
		{"lifting", idle, "Start an empty workout"},
		{"lifting", going, `data-rest-until=`},
		{"lifting", going, `value="145"`}, // last time's weight, filled in
		{"lifting", going, "lift-pr"},     // 145 × 8 beat 135 × 8
		{"lifting", going, "in progress"},
		{"lift-finish", finishView{Took: "45 min", Task: "Routine 1: Push", TaskDue: true}, "Ticks “Routine 1: Push”"},
		{"lift-routine-editor", routineEditorView{Routine: d.Routines[0], Tasks: []string{"Routine 1: Push"}}, `name="d0.e0.rest" value="90"`},
		{"card-tasks", dueTasksView{Rows: []taskRow{{Workout: &workoutLink{Routine: "r", Day: "d"}}}}, "Start workout"},
	}
	for _, c := range cases {
		html, err := ui.renderToHTML(c.name, c.view)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(string(html), c.want) {
			t.Errorf("%s: no %q", c.name, c.want)
		}
	}
}

func TestRoutineEditorForm(t *testing.T) {
	form := url.Values{"name": {"PPL"}, "d0.id": {"abc"}, "d0.name": {"Push"}, "d0.task": {""},
		"d0.e0.name": {"Bench"}, "d0.e0.sets": {"4"}, "d0.e0.reps": {"6-8"}, "d0.e0.rest": {"150"},
		"d0.e1.name": {"Dips"}, "d0.e1.sets": {"3"}, "d0.e1.reps": {"max"}, "d0.e1.rest": {"90"},
		"d1.id": {""}, "d1.name": {"Pull"}}
	r := httptest.NewRequest("POST", "/lifting/routines/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	routine := routineFromForm(r)
	if len(routine.Days) != 2 || routine.Days[0].ID != "abc" || len(routine.Days[0].Exercises) != 2 {
		t.Fatalf("read %+v", routine)
	}
	if ex := routine.Days[0].Exercises[0]; ex.Sets != 4 || ex.Reps != "6-8" || ex.Rest != 150 {
		t.Errorf("first exercise %+v", ex)
	}
	editRoutine(&routine, "down-ex:0:0")
	if routine.Days[0].Exercises[0].Name != "Dips" {
		t.Errorf("down-ex didn't move Bench down")
	}
	editRoutine(&routine, "remove-day:1")
	if len(routine.Days) != 1 {
		t.Errorf("remove-day left %d days", len(routine.Days))
	}
}

// The finish panel offers the time in a form the Took box reads back.
func TestFinishTookReadsBack(t *testing.T) {
	for _, minutes := range []int{1, 45, 60, 75, 150} {
		if got, err := parseTook(minutesText(minutes)); err != nil || got != minutes {
			t.Errorf("%d → %q → %d, %v", minutes, minutesText(minutes), got, err)
		}
	}
}

func TestOneRepMaxCalculator(t *testing.T) {
	view := calcViewOf("lb", "225", "5")
	if view.Epley != "262.5" || view.Brzycki != "252.5" { // to the nearest 2.5 lb
		t.Fatalf("225 × 5: Epley %s, Brzycki %s", view.Epley, view.Brzycki)
	}
	if first, five := view.Table[0], view.Table[4]; first.Weight != "262.5" || five.Reps != 5 || five.Weight != "225" || five.Percent != 86 || view.Table[1].Weight != "245" {
		t.Errorf("table: %+v", view.Table)
	}
	if calcViewOf("lb", "225", "0").Problem == "" || calcViewOf("lb", "heavy", "5").Problem == "" {
		t.Error("bad input passed")
	}
	ui := &UI{templates: newTemplateSet("../../web")}
	for name, view := range map[string]any{
		"lift-calc-result": view,
		"card-strength": strengthView{Unit: "lb", Body: "225", Calc: calcView{Unit: "lb"}, HasLifts: true, Measure: "heaviest", Range: "6m", Ranges: strengthRanges,
			Series: []strengthSeries{{Slot: 1, Name: "Bench press", Latest: "262", Path: "M0,10", Points: []strengthPoint{{Value: "262", Set: "225 × 5", E1RM: "262"}}}},
			Chart:  &strengthChart{Ticks: []strengthTick{{Top: 50, Label: "250"}}}},
	} {
		html, err := ui.renderToHTML(name, view)
		if err != nil || !strings.Contains(string(html), "262") {
			t.Errorf("%s: %v\n%s", name, err, html)
		}
	}
}

func TestWorkoutEditorForm(t *testing.T) {
	d := liftingData()
	r0 := d.Routines[0]
	now := time.Date(2026, 10, 20, 18, 0, 0, 0, time.Local)
	d.Start(r0.ID, r0.Days[0].ID, now)
	d.Apply(lifting.Change{Op: "done"}, now)
	base, _ := d.Finish(now.Add(time.Hour), 60, "")
	form := url.Values{"title": {"Push"}, "day": {"2026-10-19"}, "time": {"07:30"}, "took": {"50m"}, "note": {""},
		"e0.name": {"Bench press"}, "e0.s0.w": {"100"}, "e0.s0.r": {"5"}, "e0.s1.w": {""}, "e0.s1.r": {"12"}}
	post := func(values url.Values) (lifting.Workout, error) {
		r := httptest.NewRequest("POST", "/lifting/workouts/x", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return workoutFromForm(r, base, "kg")
	}
	edited, err := post(form)
	if err != nil {
		t.Fatal(err)
	}
	if !edited.Started.Equal(time.Date(2026, 10, 19, 7, 30, 0, 0, time.Local)) || edited.Minutes != 50 || edited.Exercises[0].Rest != 90 {
		t.Errorf("read %+v", edited)
	}
	if sets := edited.Exercises[0].Sets; len(sets) != 2 || lifting.FormatShown(sets[0].LB, "kg") != "100" || sets[1].LB != 0 || sets[1].Reps != 12 {
		t.Errorf("sets %+v", sets)
	}
	form.Set("took", "soon")
	if edited, err := post(form); err == nil || len(edited.Exercises) != 1 {
		t.Errorf("a bad duration: %v, and the exercises must still be read: %+v", err, edited.Exercises)
	}
	editWorkout(&edited, "add-set:0")
	if len(edited.Exercises[0].Sets) != 3 || edited.Exercises[0].Sets[2].Reps != 12 {
		t.Errorf("add-set copies the last set: %+v", edited.Exercises[0].Sets)
	}
	ui := &UI{templates: newTemplateSet("../../web")}
	if html, err := ui.renderToHTML("lift-workout-editor", workoutEditorOf(base, d)); err != nil || !strings.Contains(string(html), `name="e0.s0.r"`) {
		t.Errorf("editor: %v", err)
	}
}

// The Strength chart shows the estimated one-rep max unless "Heaviest" was picked.
func TestStrengthDefaultsToEstimate(t *testing.T) {
	for saved, want := range map[string]string{"": "e1rm", "e1rm": "e1rm", "heaviest": "heaviest", "junk": "e1rm"} {
		if got := strengthMeasure(saved); got != want {
			t.Errorf("saved %q: %q, want %q", saved, got, want)
		}
	}
}

// An exercise's heading shows its best set's estimate to the plate; notes and skips reach the history.
func TestEstimateNotesAndSkips(t *testing.T) {
	d := liftingData()
	r0 := d.Routines[0]
	d.Start(r0.ID, r0.Days[0].ID, time.Now())
	d.Active.Exercises[0].Sets = []lifting.Set{{LB: 200, Reps: 12}, {LB: 135, Reps: 7}, {LB: 400, Reps: 5, Skipped: true}}
	if got := workoutViewOf(d, time.Now()).Exercises[0].Est; got != "280" {
		t.Errorf("heading estimate %q: 200 × 12's 280, not the skipped set's", got)
	}
	if got := setsWithNotes([]lifting.Set{{LB: 135, Reps: 8, Note: "easy"}, {Reps: 10}, {Skipped: true, Why: "elbow"}}, "lb"); got != "135 × 8 (easy), BW × 10, skipped (elbow)" {
		t.Errorf("history line %q", got)
	}
}

func TestGroupSets(t *testing.T) {
	got := groupSets([]lifting.Set{{LB: 200, Reps: 5}, {LB: 200, Reps: 5}, {LB: 200, Reps: 5}, {LB: 200, Reps: 4}, {LB: 225, Reps: 3}, {LB: 225, Reps: 3}, {Reps: 10}}, "lb")
	want := []setGroup{{"200", "5/5/5/4"}, {"225", "2 × 3"}, {"BW", "10"}}
	if len(got) != len(want) {
		t.Fatalf("groups %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWorkoutFeaturesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	now := time.Now()
	d := lifting.Data{Unit: "lb"}
	d.SaveRoutine(lifting.Routine{Name: "Core", Days: []lifting.Day{{Name: "Core", Exercises: []lifting.Planned{
		{Name: "Plank", Sets: 2, Reps: "45", Hold: true, Rest: 60},
		{Name: "Curl", Sets: 2, Reps: "10", Rest: 60, Superset: true, Options: []string{"Hammer curl"}},
		{Name: "Pushdown", Sets: 2, Reps: "12", Rest: 90, Optional: true},
	}}}})
	r := d.Routines[0]
	d.Start(r.ID, r.Days[0].ID, now)
	d.Apply(lifting.Change{Op: "hold", Exercise: 0, Set: 0}, now)
	html, err := ui.renderToHTML("lift-workout", workoutViewOf(d, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"data-auto-done", `value="done:0:0"`, "Hold · Plank, set 1", `value="hold:0:1"`, // the hold banner, the next set's ▶
		`class="lift-ex ss-first`, "Superset", `class="lift-ex ss-last`, // Curl + Pushdown
		`value="swap-to:1:0">Hammer curl`, "lift-ex ss-last closed", // the choice, the folded optional
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("no %q", want)
		}
	}
	editor, err := ui.renderToHTML("lift-routine-editor", routineEditorView{Routine: d.Routines[0]})
	if err != nil || !strings.Contains(string(editor), `value="Hammer curl"`) || !strings.Contains(string(editor), `name="d0.e0.hold" value="1" checked`) {
		t.Errorf("editor: %v", err)
	}
}

func TestRoutineEditorReadsFeatures(t *testing.T) {
	form := url.Values{"name": {"Core"}, "d0.id": {""}, "d0.name": {"Core"},
		"d0.e0.name": {"Plank"}, "d0.e0.sets": {"3"}, "d0.e0.reps": {"1:00"}, "d0.e0.rest": {"60"}, "d0.e0.hold": {"1"},
		"d0.e0.superset": {"1"}, "d0.e0.options": {"Side plank, Dead bug"},
		"d0.e1.name": {"Curl"}, "d0.e1.sets": {"3"}, "d0.e1.reps": {"10"}, "d0.e1.rest": {"60"}, "d0.e1.optional": {"1"}}
	r := httptest.NewRequest("POST", "/lifting/routines/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	routine := lifting.CleanRoutine(routineFromForm(r))
	plank, curl := routine.Days[0].Exercises[0], routine.Days[0].Exercises[1]
	if !plank.Hold || plank.Reps != "60" || !plank.Superset || len(plank.Options) != 2 || plank.Options[1] != "Dead bug" {
		t.Errorf("plank: %+v", plank)
	}
	if !curl.Optional || curl.Hold || curl.Superset {
		t.Errorf("curl: %+v", curl)
	}
}

func TestOneRMGoalRenders(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	now := time.Now()
	d := liftingData()
	r := d.Routines[0]
	d.Start(r.ID, r.Days[0].ID, now.Add(-2*time.Hour))
	lb, one := 205.0, 1
	d.SetTyped(map[[2]int]lifting.Typed{{0, 0}: {LB: &lb, Reps: &one}})
	d.Apply(lifting.Change{Op: "done"}, now)
	d.Finish(now.Add(-time.Hour), 60, "")
	if err := d.SetGoal("Bench press", 225); err != nil {
		t.Fatal(err)
	}
	progress := progressViewOf(d, "Bench press")
	if progress.OneRM != "205" || progress.Goal != "225" || progress.ToGo != "20" || progress.Reached {
		t.Fatalf("progress %+v", progress)
	}
	d.Start(r.ID, r.Days[0].ID, now)
	lb = 225
	d.SetTyped(map[[2]int]lifting.Typed{{0, 0}: {LB: &lb, Reps: &one}})
	d.Apply(lifting.Change{Op: "done"}, now)
	strength := strengthView{Unit: "lb", Measure: "max", HasLifts: true, Series: []strengthSeries{{Slot: 1, Name: "Bench press",
		Latest: "205", Path: "M0,60", Goal: "225", GoalY: 20, ToGo: "20"}}, Chart: &strengthChart{From: now, To: now}}
	cases := []struct {
		name string
		view any
		want string
	}{
		{"lift-progress", progress, `name="goal" value="225"`},
		{"lift-progress", progress, "20 lb to go"},
		{"lift-workout", workoutViewOf(d, now), ">GOAL<"},
		{"lift-workout", workoutViewOf(d, now), "/ goal 225"},
		{"card-strength", strength, `stroke-dasharray="5 4"`},
		{"card-strength", strength, "goal 225 (20 to go)"},
	}
	for _, c := range cases {
		html, err := ui.renderToHTML(c.name, c.view)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(string(html), c.want) {
			t.Errorf("%s: no %q", c.name, c.want)
		}
	}
}

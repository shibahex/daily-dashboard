package lifting

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestImport(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "lifting.json"))
	started := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	exported := Data{
		Unit:     "kg",
		Routines: []Routine{{ID: "r1", Name: "Full body", Days: []Day{{ID: "d1", Name: "A", Exercises: []Planned{{Name: "Squat", Sets: 3, Reps: "5"}}}}}},
		Workouts: []Workout{
			{ID: "w1", Title: "A", Started: started, Minutes: 50, Exercises: []Exercise{{Name: "Squat", Sets: []Set{{LB: 225, Reps: 5, Done: true}}}}},
			{ID: "w2", Title: "Nothing done", Started: started.Add(time.Hour), Exercises: []Exercise{{Name: "Squat"}}},
		},
	}
	data, _ := json.Marshal(exported)
	routines, workouts, err := s.Import(data)
	if err != nil || routines != 1 || workouts != 1 {
		t.Fatalf("Import = %d, %d, %v", routines, workouts, err)
	}
	got := s.Load()
	if got.Unit != "kg" || got.Routines[0].ID != "r1" || got.Workouts[0].ID != "w1" || got.Workouts[0].Exercises[0].Sets[0].LB != 225 {
		t.Errorf("after import: %+v", got)
	}
	// Again: nothing new.
	if routines, workouts, err := s.Import(data); err != nil || routines != 0 || workouts != 0 {
		t.Errorf("second Import = %d, %d, %v", routines, workouts, err)
	}
	if _, _, err := s.Import([]byte(`[1]`)); err == nil {
		t.Error("imported something that isn't lifting.json")
	}
}

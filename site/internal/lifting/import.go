package lifting

import (
	"encoding/json"
	"slices"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// Import adds an exported lifting.json: routines and finished workouts that aren't here yet
// (by id), cleaned like any others. With nothing logged here yet, the settings (lb or kg, the
// Strength chart) come along too. The workout in progress stays as it is. It answers how many
// routines and workouts it added.
func (s *Store) Import(data []byte) (routines, workouts int, err error) {
	var imported Data
	if err := json.Unmarshal(data, &imported); err != nil {
		return 0, 0, problem.Invalidf("lifting.json isn't the Lifting tab's data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.Load()
	fresh := len(current.Routines) == 0 && len(current.Workouts) == 0
	for _, r := range imported.Routines {
		if r.ID == "" || slices.ContainsFunc(current.Routines, func(have Routine) bool { return have.ID == r.ID }) {
			continue
		}
		cleaned := CleanRoutine(r)
		cleaned.ID = r.ID
		current.Routines = append(current.Routines, cleaned)
		routines++
	}
	for _, w := range imported.Workouts {
		if w.ID == "" || slices.ContainsFunc(current.Workouts, func(have Workout) bool { return have.ID == w.ID }) {
			continue
		}
		cleaned, err := CleanWorkout(w)
		if err != nil {
			continue
		}
		current.Workouts = append(current.Workouts, cleaned)
		workouts++
	}
	slices.SortStableFunc(current.Workouts, func(a, b Workout) int { return a.Started.Compare(b.Started) })
	if fresh {
		if imported.Unit == "kg" {
			current.Unit = "kg"
		}
		current.Strength = imported.Strength
		current.Seeded = current.Seeded || imported.Seeded
	}
	return routines, workouts, jsonfile.Write(s.path, current)
}

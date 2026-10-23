package store

import (
	"os"
	"testing"

	"dashboard/internal/config"
)

func testStore(t *testing.T) *Store {
	dir := t.TempDir()
	return New(config.NewPaths(dir).ForUser(dir))
}

func TestImportPlan(t *testing.T) {
	s := testStore(t)
	example, err := os.ReadFile("../../plan.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportPlan(example); err != nil {
		t.Fatal(err)
	}
	if got := s.PlanChecklist(); len(got) == 0 {
		t.Errorf("no checklist after importing the example plan")
	}
	for _, bad := range []string{`[]`, `"plan"`, `{"goals":`} {
		if err := s.ImportPlan([]byte(bad)); err == nil {
			t.Errorf("ImportPlan(%s) worked", bad)
		}
	}
	if data, _ := os.ReadFile(s.paths.Plan()); len(data) == 0 {
		t.Error("the plan is gone after a bad import")
	}
}

func TestImportChecklistAndWeights(t *testing.T) {
	s := testStore(t)
	s.SaveChecklistDay("2026-10-01", map[string]any{"Walk": "done"})
	lb := 180.0
	s.SetWeight("2026-10-01", &lb)

	n, err := s.ImportChecklist([]byte(`{"2026-10-01": {"Walk": "skip"}, "2026-10-02": {"Walk": true}, "junk": {"x": "done"}}`))
	if err != nil || n != 2 {
		t.Fatalf("ImportChecklist = %d, %v", n, err)
	}
	days := s.Checklist()
	if days["2026-10-01"]["Walk"] != "skip" || days["2026-10-02"]["Walk"] != "done" || days["junk"] != nil {
		t.Errorf("checklist = %v", days)
	}

	n, err = s.ImportWeights([]byte(`[{"date": "2026-10-02", "lb": 179.5}, {"date": "bad", "lb": 1}, {"date": "2026-10-03", "lb": -4}]`))
	if err != nil || n != 1 {
		t.Fatalf("ImportWeights = %d, %v", n, err)
	}
	if got := s.Weights(); len(got) != 2 || got[0].LB != 180 || got[1].LB != 179.5 {
		t.Errorf("weights = %v", got)
	}
}

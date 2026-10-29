package store

import "testing"

func TestWeightPlanFromWeighIns(t *testing.T) {
	weights := []Weight{{Date: "2026-08-06", LB: 226}, {Date: "2026-09-01", LB: 221}, {Date: "2026-10-28", LB: 223.9}}
	if got := (WeightPlan{}).WithStart(weights); got.Start != 226 || got.StartDate != "2026-08-06" {
		t.Errorf("no start: got %v on %q, want the first weigh-in", got.Start, got.StartDate)
	}
	if got := (WeightPlan{StartDate: "2026-09-01"}).WithStart(weights); got.Start != 221 {
		t.Errorf("a start date only: got %v, want that day's weigh-in", got.Start)
	}
	if got := (WeightPlan{Start: 230, StartDate: "2026-01-01"}).WithStart(weights); got.Start != 230 {
		t.Errorf("a set start is kept, got %v", got.Start)
	}
	if got := (WeightPlan{}).Lowest(weights); got != 221 {
		t.Errorf("lowest: got %v, want the lowest weigh-in", got)
	}
	if got := (WeightPlan{Best: 200}).Lowest(weights); got != 200 {
		t.Errorf("lowest: got %v, want the plan's lower one", got)
	}
}

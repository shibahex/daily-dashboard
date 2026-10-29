package web

import (
	"testing"
	"time"

	"dashboard/internal/store"
)

func TestWeightRange(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.Local)
	weights := []store.Weight{
		{Date: "2025-06-01", LB: 240},
		{Date: "2026-10-19", LB: 226}, {Date: "2026-10-21", LB: 225}, // the past 2 weeks
		{Date: "2026-10-25", LB: 224}, {Date: "2026-10-26", LB: 224}, {Date: "2026-10-27", LB: 224.1}, {Date: "2026-10-28", LB: 224}, // the past week
	}
	cases := []struct {
		id, trend string
		count     int
		average   float64
	}{
		{"7d", "stable", 4, 224},
		{"14d", "losing", 6, 224.5},
		{"30d", "losing", 6, 224.5},
		{"3m", "losing", 6, 224.5},
		{"all", "losing", 7, 226.7},
	}
	for _, c := range cases {
		from, to := weightRangeDays(c.id, now)
		got := weightAverageOf(weighInsBetween(weights, from, to))
		if got.Count != c.count || got.Average != c.average || got.Trend != c.trend {
			t.Errorf("%q: got %d weigh-ins, average %v, %q (%v lb a week); want %d, %v, %q",
				c.id, got.Count, got.Average, got.Trend, got.PerWeek, c.count, c.average, c.trend)
		}
	}
	if got := weightAverageOf(weights[1:2]); got.Trend != "" {
		t.Errorf("one weigh-in has no trend, got %q", got.Trend)
	}
	if weightTrend(weights[1:2]) != nil {
		t.Error("one weigh-in has no chart")
	}
}

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
		weekly    bool
	}{
		{"7d", "stable", 4, 224, false},
		{"14d", "losing", 6, 224.5, true},
		{"30d", "losing", 6, 224.5, true},
		{"all", "losing", 7, 226.7, true},
	}
	for _, c := range cases {
		from, to := weightRangeDays(c.id, now)
		got := weightAverageOf(weighInsBetween(weights, from, to))
		if got.Count != c.count || got.Average != c.average || got.Trend != c.trend || got.Weekly != c.weekly {
			t.Errorf("%q: got %d weigh-ins, average %v, %q, weekly %v (%v lb a week); want %d, %v, %q, %v",
				c.id, got.Count, got.Average, got.Trend, got.Weekly, got.PerWeek, c.count, c.average, c.trend, c.weekly)
		}
	}
	if got := weightAverageOf(weights[1:2]); got.Trend != "" {
		t.Errorf("one weigh-in has no trend, got %q", got.Trend)
	}
	if weightTrend(weights[1:2]) != nil {
		t.Error("one weigh-in has no chart")
	}
}

// A few days' drop isn't stretched into a week's.
func TestWeightAverageWithinAWeek(t *testing.T) {
	got := weightAverageOf([]store.Weight{{Date: "2026-10-25", LB: 226}, {Date: "2026-10-26", LB: 225}, {Date: "2026-10-28", LB: 224}})
	if got.Weekly || got.Change != -2 || got.Days != 3 || got.Trend != "losing" {
		t.Errorf("got weekly %v, %v lb in %d days, %q; want -2 lb in 3 days, losing", got.Weekly, got.Change, got.Days, got.Trend)
	}
}

// Weeks without weigh-ins are left out, but the time still counts: 4 lb over 4 weeks is 1 a week.
func TestWeightAverageSkipsEmptyWeeks(t *testing.T) {
	got := weightAverageOf([]store.Weight{
		{Date: "2026-09-30", LB: 230}, {Date: "2026-10-01", LB: 229.6}, {Date: "2026-10-02", LB: 230.4}, // 4 weeks ago
		{Date: "2026-10-28", LB: 226}, // this week; nothing logged in between
	})
	if !got.Weekly || got.PerWeek != -1 || got.Trend != "losing" {
		t.Errorf("got weekly %v, %v lb a week, %q; want -1 lb a week, losing", got.Weekly, got.PerWeek, got.Trend)
	}
}

package taskwarrior

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// RepeatRule is a repeat schedule: every 2 days, every other Thursday, the same day each month.
type RepeatRule struct {
	Type  string // "days", "weekly" or "monthly"
	Every int
	Days  []int // weekdays for "weekly", 0 = Sunday
	Start time.Time
}

// Series is one Taskwarrior repeating series: Taskwarrior has one period per series, so
// "Mon, Wed, Fri" is three weekly series with the same name.
type Series struct {
	FirstDue string // YYYY-MM-DD
	Recur    string // "daily", "3d", "weekly", "2w", "monthly"
}

const dateLayout = "2006-01-02"

// RepeatForm is a repeat schedule as the page's form sends it.
type RepeatForm struct {
	Type  string
	Every string
	Days  []string
	Start string // YYYY-MM-DD; empty is today
}

// Rule reads and checks the form.
func (f RepeatForm) Rule(today time.Time) (RepeatRule, error) {
	rule := RepeatRule{Type: f.Type, Every: 1, Start: midnight(today)}
	if f.Type != "days" && f.Type != "weekly" && f.Type != "monthly" {
		return RepeatRule{}, errors.New("bad rule type")
	}
	if f.Every != "" {
		every, err := strconv.Atoi(f.Every)
		if err != nil || every < 1 || every > 60 {
			return RepeatRule{}, errors.New("bad interval")
		}
		rule.Every = every
	}
	if f.Start != "" {
		start, err := time.ParseInLocation(dateLayout, f.Start, time.Local)
		if err != nil {
			return RepeatRule{}, errors.New("bad start date")
		}
		rule.Start = start
	}
	for _, text := range f.Days {
		day, err := strconv.Atoi(text)
		if err != nil || day < 0 || day > 6 {
			return RepeatRule{}, errors.New("bad weekday")
		}
		rule.Days = append(rule.Days, day)
	}
	return rule, nil
}

// Series works out the series for the rule. The first due date is never in the past, so no
// missed copies get created.
func (r RepeatRule) Series(today time.Time) ([]Series, error) {
	today = midnight(today)
	start := midnight(r.Start)
	switch r.Type {
	case "days":
		for start.Before(today) {
			start = start.AddDate(0, 0, r.Every)
		}
		recur := "daily"
		if r.Every > 1 {
			recur = fmt.Sprintf("%dd", r.Every)
		}
		return []Series{{FirstDue: start.Format(dateLayout), Recur: recur}}, nil

	case "weekly":
		if len(r.Days) == 0 || r.Every > 8 {
			return nil, errors.New("pick at least one weekday")
		}
		recur := "weekly"
		if r.Every > 1 {
			recur = fmt.Sprintf("%dw", r.Every)
		}
		var series []Series
		for _, weekday := range uniqueSorted(r.Days) {
			first := start.AddDate(0, 0, (weekday-int(start.Weekday())+7)%7)
			for first.Before(today) {
				first = first.AddDate(0, 0, 7*r.Every)
			}
			series = append(series, Series{FirstDue: first.Format(dateLayout), Recur: recur})
		}
		return series, nil

	case "monthly":
		// Days past the 28th would skip short months, so they move to the 28th.
		day := min(start.Day(), 28)
		month := time.Date(start.Year(), start.Month(), day, 0, 0, 0, 0, time.Local)
		for month.Before(today) {
			month = month.AddDate(0, 1, 0)
		}
		return []Series{{FirstDue: month.Format(dateLayout), Recur: "monthly"}}, nil
	}
	return nil, errors.New("bad rule type")
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func uniqueSorted(days []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, d := range days {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Ints(out)
	return out
}

// Upcoming lists the rule's next dates, at most `limit` of them, for a preview.
func (r RepeatRule) Upcoming(today time.Time, limit int) []time.Time {
	series, err := r.Series(today)
	if err != nil {
		return nil
	}
	var dates []time.Time
	for _, one := range series {
		first, _ := time.ParseInLocation(dateLayout, one.FirstDue, time.Local)
		for i := 0; i < limit; i++ {
			switch r.Type {
			case "days":
				dates = append(dates, first.AddDate(0, 0, i*r.Every))
			case "weekly":
				dates = append(dates, first.AddDate(0, 0, 7*i*r.Every))
			case "monthly":
				dates = append(dates, first.AddDate(0, i, 0))
			}
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	if len(dates) > limit {
		dates = dates[:limit]
	}
	return dates
}

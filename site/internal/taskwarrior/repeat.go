package taskwarrior

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// RepeatRule is a repeat schedule from the page:
//
//	{"type": "days", "every": 2, "start": "2026-10-15"}         every 2 days
//	{"type": "weekly", "days": [4], "every": 2, "start": ...}    every other Thursday (0 = Sunday)
//	{"type": "monthly", "start": ...}                            the same day each month
type RepeatRule struct {
	Type       string
	Every      int
	Days       []int // weekdays for "weekly", 0 = Sunday
	Start      time.Time
	badWeekday bool // a day that isn't 0-6 (only an error for "weekly" rules)
}

// Series is one Taskwarrior repeating series: Taskwarrior has one period per series, so
// "Mon, Wed, Fri" is three weekly series with the same name.
type Series struct {
	FirstDue string // YYYY-MM-DD
	Recur    string // "daily", "3d", "weekly", "2w", "monthly"
}

const dateLayout = "2006-01-02"

// ParseRepeatRule reads and checks a rule sent by the page.
func ParseRepeatRule(raw json.RawMessage, today time.Time) (RepeatRule, error) {
	var input struct {
		Type  string            `json:"type"`
		Every json.RawMessage   `json:"every"`
		Days  []json.RawMessage `json:"days"`
		Start string            `json:"start"`
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}") // no rule at all: fails below as "bad rule type"
	}
	if json.Unmarshal(raw, &input) != nil {
		return RepeatRule{}, errors.New("bad rule")
	}
	rule := RepeatRule{Type: input.Type, Every: 1, Start: today}
	if len(input.Every) > 0 {
		every, ok := wholeNumber(input.Every)
		if !ok || every < 1 || every > 60 {
			return RepeatRule{}, errors.New("bad interval")
		}
		rule.Every = every
	}
	if input.Start != "" {
		start, err := time.ParseInLocation(dateLayout, input.Start, time.Local)
		if err != nil {
			return RepeatRule{}, errors.New("bad start date")
		}
		rule.Start = start
	}
	for _, d := range input.Days {
		day, ok := wholeNumber(d)
		if !ok || day < 0 || day > 6 {
			rule.badWeekday = true
			continue
		}
		rule.Days = append(rule.Days, day)
	}
	return rule, nil
}

func wholeNumber(raw json.RawMessage) (int, bool) {
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
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
		if len(r.Days) == 0 || r.badWeekday || r.Every > 8 {
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

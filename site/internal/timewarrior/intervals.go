package timewarrior

import (
	"encoding/json"
	"time"

	"dashboard/internal/problem"
)

// timestampLayout is how Timewarrior (and Taskwarrior) write times: UTC, to the second.
const timestampLayout = "20060102T150405Z"

// ParseTimestamp reads "20261020T140000Z" into local time.
func ParseTimestamp(s string) (time.Time, bool) {
	t, err := time.Parse(timestampLayout, s)
	return t.Local(), err == nil
}

func (i Interval) StartTime() time.Time { t, _ := ParseTimestamp(i.Start); return t }

// EndTime is when the entry ended; running is true while it's still being tracked.
func (i Interval) EndTime() (end time.Time, running bool) {
	if i.End == "" {
		return time.Time{}, true
	}
	t, _ := ParseTimestamp(i.End)
	return t, false
}

// Duration is how long the entry lasted (until now, while running).
func (i Interval) Duration(now time.Time) time.Duration {
	end, running := i.EndTime()
	if running {
		end = now
	}
	return end.Sub(i.StartTime())
}

// IntervalsThisWeek is everything tracked since Monday.
func (c Client) IntervalsThisWeek() ([]Interval, error) { return c.intervals(":week") }

// IntervalsBetween is everything tracked from `from` up to (not including) `until`.
func (c Client) IntervalsBetween(from, until time.Time) ([]Interval, error) {
	return c.intervals("from", from.Format("2006-01-02"), "to", until.Format("2006-01-02"))
}

func (c Client) intervals(rangeArgs ...string) ([]Interval, error) {
	exported, result := c.Export(rangeArgs...)
	switch {
	case result.ExitCode == ExitNotInstalled:
		return nil, problem.Unavailablef(result.Error())
	case !result.OK():
		return nil, problem.Failedf(result.FirstErrorLine("timew export failed"))
	}
	var intervals []Interval
	json.Unmarshal(exported, &intervals)
	return intervals, nil
}

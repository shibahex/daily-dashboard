package taskwarrior

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Period is a parsed recur value: every N days, weeks, months or years.
type Period struct {
	Every int
	Unit  string // "d", "w", "m", "y", "weekdays", or the raw value when unknown
}

var namedPeriods = map[string]Period{
	"daily": {1, "d"}, "day": {1, "d"}, "weekly": {1, "w"}, "week": {1, "w"}, "biweekly": {2, "w"},
	"fortnight": {2, "w"}, "monthly": {1, "m"}, "month": {1, "m"}, "quarterly": {3, "m"},
	"yearly": {1, "y"}, "annual": {1, "y"}, "weekdays": {1, "weekdays"},
}

var (
	isoPeriod     = regexp.MustCompile(`^p(\d+)([dwmy])$`)
	writtenPeriod = regexp.MustCompile(`^(\d+)\s*(d|days?|w|wks?|weeks?|mo|mths?|months?|y|yrs?|years?)$`)
)

// ParsePeriod reads Taskwarrior recur values: "weekly", "3d", "P14D", "2 weeks"...
func ParsePeriod(recur string) Period {
	recur = strings.ToLower(strings.TrimSpace(recur))
	if p, ok := namedPeriods[recur]; ok {
		return p
	}
	var every int
	var unit string
	if m := isoPeriod.FindStringSubmatch(recur); m != nil {
		every, _ = strconv.Atoi(m[1])
		unit = m[2]
	} else if m := writtenPeriod.FindStringSubmatch(recur); m != nil {
		every, _ = strconv.Atoi(m[1])
		unit = m[2][:1]
	} else {
		return Period{Unit: recur}
	}
	if unit == "d" && every%7 == 0 {
		return Period{Every: every / 7, Unit: "w"}
	}
	return Period{Every: every, Unit: unit}
}

var shortWeekdays = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// ShortWeekday is "Mon" for time.Monday.
func ShortWeekday(day time.Weekday) string { return shortWeekdays[day] }

// DescribeSchedule says how a repeating task repeats, from its series templates:
// "Every week on Mon, Thu", "Every 2 days", "Monthly on the 15th".
func DescribeSchedule(templates []Task) string {
	if len(templates) == 0 {
		return "Doesn't repeat"
	}
	type series struct {
		period Period
		due    time.Time
		hasDue bool
	}
	all := make([]series, len(templates))
	for i, template := range templates {
		due, hasDue := template.DueTime()
		all[i] = series{ParsePeriod(template.Recur()), due, hasDue}
	}
	sameWeekly := true
	for _, s := range all {
		sameWeekly = sameWeekly && s.period.Unit == "w" && s.period.Every == all[0].period.Every
	}
	if sameWeekly {
		seen := map[time.Weekday]bool{}
		var days []int
		for _, s := range all {
			if !seen[s.due.Weekday()] {
				seen[s.due.Weekday()] = true
				days = append(days, int(s.due.Weekday()))
			}
		}
		sort.Ints(days)
		names := make([]string, len(days))
		for i, d := range days {
			names[i] = shortWeekdays[d]
		}
		every := "Every week"
		if n := all[0].period.Every; n != 1 {
			every = fmt.Sprintf("Every %d weeks", n)
		}
		return every + " on " + strings.Join(names, ", ")
	}

	parts := make([]string, len(all))
	for i, s := range all {
		n := s.period.Every
		switch s.period.Unit {
		case "d":
			parts[i] = pluralEvery(n, "Every day", "days")
		case "w":
			parts[i] = pluralEvery(n, "Every week", "weeks") + " on " + shortWeekdays[s.due.Weekday()]
		case "m":
			on := "same day"
			if s.hasDue {
				on = Ordinal(s.due.Day())
			}
			monthly := "Monthly"
			if n != 1 {
				monthly = fmt.Sprintf("Every %d months", n)
			}
			parts[i] = monthly + " on the " + on
		case "y":
			parts[i] = "Yearly"
		case "weekdays":
			parts[i] = "Every weekday"
		default:
			parts[i] = "Repeats (" + s.period.Unit + ")"
		}
	}
	return strings.Join(parts, " + ")
}

func pluralEvery(n int, one, unit string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf("Every %d %s", n, unit)
}

// Ordinal is "1st", "2nd", "11th", "23rd".
func Ordinal(n int) string {
	switch {
	case n%10 == 1 && n != 11:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2 && n != 12:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3 && n != 13:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}

package taskwarrior

import (
	"dashboard/internal/problem"
	"regexp"
	"strings"
)

// Taskwarrior word characters, including letters from any language.
const word = `[\p{L}\p{N}_]`

var (
	projectPattern = regexp.MustCompile(`^[\p{L}\p{N}_.-]{1,40}$`)
	datePattern    = regexp.MustCompile(`^[\p{L}\p{N}_:+-]{1,30}$`) // "2026-10-21", "tomorrow", "eow+2d"
	tagPattern     = regexp.MustCompile(`^` + word + `{1,30}$`)
	recurPattern   = regexp.MustCompile(`^` + word + `{1,20}$`) // "daily", "2w", "monthly"
)

// Changes are the attributes to set on a task. A nil pointer leaves the attribute alone and an
// empty string clears it.
type Changes struct {
	Project    *string
	Priority   *string // "H", "M", "L" or ""
	Due        *string
	Scheduled  *string
	Wait       *string
	Recur      string // only when adding a task
	AddTags    []string
	RemoveTags []string
}

// Args turns the changes into `task` arguments, checking each value. Series args (project,
// priority, recur, tags) can go to a whole repeating series; date args only make sense for
// one copy.
func (c Changes) Args() (seriesArgs, dateArgs []string, err error) {
	if c.Project != nil {
		project := strings.TrimSpace(*c.Project)
		if project != "" && !projectPattern.MatchString(project) {
			return nil, nil, problem.Invalidf("bad project")
		}
		seriesArgs = append(seriesArgs, "project:"+project)
	}
	if c.Priority != nil {
		priority := strings.ToUpper(strings.TrimSpace(*c.Priority))
		if priority != "" && priority != "H" && priority != "M" && priority != "L" {
			return nil, nil, problem.Invalidf("bad priority")
		}
		seriesArgs = append(seriesArgs, "priority:"+priority)
	}
	for _, date := range []struct {
		name  string
		value *string
	}{{"due", c.Due}, {"scheduled", c.Scheduled}, {"wait", c.Wait}} {
		if date.value == nil {
			continue
		}
		value := strings.TrimSpace(*date.value)
		if value != "" && !datePattern.MatchString(value) {
			return nil, nil, problem.Invalidf("bad " + date.name)
		}
		dateArgs = append(dateArgs, date.name+":"+value)
	}
	if c.Recur != "" {
		recur := strings.TrimSpace(c.Recur)
		if !recurPattern.MatchString(recur) {
			return nil, nil, problem.Invalidf("bad recur")
		}
		seriesArgs = append(seriesArgs, "recur:"+recur)
	}
	for _, tag := range c.AddTags {
		if !tagPattern.MatchString(tag) {
			return nil, nil, problem.Invalidf("bad tag")
		}
		seriesArgs = append(seriesArgs, "+"+tag)
	}
	for _, tag := range c.RemoveTags {
		if !tagPattern.MatchString(tag) {
			return nil, nil, problem.Invalidf("bad tag")
		}
		seriesArgs = append(seriesArgs, "-"+tag)
	}
	return seriesArgs, dateArgs, nil
}

// IsDateValue reports whether s is a value Taskwarrior accepts for a date ("tomorrow", "2026-10-21").
func IsDateValue(s string) bool { return datePattern.MatchString(s) }

// IsTag reports whether s can be a Taskwarrior tag.
func IsTag(s string) bool { return tagPattern.MatchString(s) }

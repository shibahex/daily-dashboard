package timewarrior

import (
	"fmt"
	"regexp"

	"dashboard/internal/problem"
)

// "2026-10-20T14:30": a local time to the minute, as the page's inputs send it.
var localMinute = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)

// Track logs a stretch of time. start and end are "2026-10-20T14:30".
func (c Client) Track(start, end string, tags []string) error {
	if !localMinute.MatchString(start) || !localMinute.MatchString(end) || end <= start {
		return problem.Invalidf("end must be after start")
	}
	tags, err := CleanTags(tags)
	if err != nil {
		return problem.Invalidf(err.Error())
	}
	result := c.Run(append([]string{"track", start, "-", end}, tags...)...)
	if !result.OK() {
		return problem.Conflictf(result.FirstErrorLine("timew error"))
	}
	return nil
}

// IntervalEdit changes an entry; empty fields stay as they are.
type IntervalEdit struct {
	Tags  []string // nil keeps the tags
	Start string   // "2026-10-20T14:30"
	End   string
}

// EditInterval changes entry @id, checking it still starts at startWas (ids shift as entries
// are added). Retagging and moving don't shift ids, so @id stays valid across the steps.
func (c Client) EditInterval(id int, startWas string, edit IntervalEdit) error {
	if err := c.checkStillThere(id, startWas); err != nil {
		return err
	}
	ref := fmt.Sprintf("@%d", id)
	var steps [][]string
	if edit.Tags != nil {
		tags, err := CleanTags(edit.Tags)
		if err != nil {
			return problem.Invalidf(err.Error())
		}
		steps = append(steps, append([]string{"retag", ref}, tags...))
	}
	for _, change := range []struct{ edge, when string }{{"start", edit.Start}, {"end", edit.End}} {
		if change.when == "" {
			continue
		}
		if !localMinute.MatchString(change.when) {
			return problem.Invalidf("bad " + change.edge)
		}
		steps = append(steps, []string{"modify", change.edge, ref, change.when})
	}
	for _, step := range steps {
		if result := c.Run(step...); !result.OK() {
			return problem.Conflictf(result.FirstErrorLine("timew error"))
		}
	}
	return nil
}

// DeleteInterval removes entry @id, checking it still starts at startWas.
func (c Client) DeleteInterval(id int, startWas string) error {
	if err := c.checkStillThere(id, startWas); err != nil {
		return err
	}
	if result := c.Run("delete", fmt.Sprintf("@%d", id)); !result.OK() {
		return problem.Failedf(result.Error())
	}
	return nil
}

func (c Client) checkStillThere(id int, startWas string) error {
	if id < 1 || !c.StillThere(id, startWas) {
		return problem.Conflictf("that entry changed since the page loaded; refresh and try again")
	}
	return nil
}

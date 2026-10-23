package timewarrior

import (
	"encoding/json"
	"fmt"

	"dashboard/internal/problem"
)

// exported is one entry of `timew export`.
type exported struct {
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Tags       []string `json:"tags"`
	Annotation string   `json:"annotation"`
}

// Import adds the entries of a `timew export` (Timewarrior has no import of its own): finished
// entries that start when none here does, with their tags and notes. Entries that are already
// here, still running, or overlapping one that is are skipped.
func (c Client) Import(export []byte) (added, skipped int, err error) {
	var entries []exported
	if err := json.Unmarshal(export, &entries); err != nil {
		return 0, 0, problem.Invalidf("time.json isn't a Timewarrior export")
	}
	have, err := c.starts()
	if err != nil {
		return 0, 0, err
	}
	annotate := map[string]string{}
	for _, e := range entries {
		_, okStart := ParseTimestamp(e.Start)
		_, okEnd := ParseTimestamp(e.End)
		if !okStart || !okEnd || e.End <= e.Start || have[e.Start] != 0 {
			skipped++
			continue
		}
		if !c.Run(append([]string{"track", e.Start, "-", e.End}, TaskTags(e.Tags)...)...).OK() {
			skipped++ // overlaps an entry already here
			continue
		}
		added++
		if e.Annotation != "" {
			annotate[e.Start] = e.Annotation
		}
	}
	if len(annotate) > 0 {
		ids, err := c.starts()
		if err != nil {
			return added, skipped, err
		}
		for start, note := range annotate {
			if id := ids[start]; id != 0 {
				c.Run("annotate", fmt.Sprintf("@%d", id), note)
			}
		}
	}
	return added, skipped, nil
}

// starts maps each entry's start to its id.
func (c Client) starts() (map[string]int, error) {
	raw, result := c.Export()
	if !result.OK() {
		return nil, problem.Unavailablef(result.FirstErrorLine("timew export failed"))
	}
	var intervals []Interval
	if err := json.Unmarshal(raw, &intervals); err != nil {
		return nil, fmt.Errorf("reading timew export: %w", err)
	}
	ids := make(map[string]int, len(intervals))
	for _, i := range intervals {
		ids[i.Start] = i.ID
	}
	return ids, nil
}

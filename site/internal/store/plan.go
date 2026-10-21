package store

import (
	"encoding/json"
	"strconv"

	"dashboard/internal/jsonfile"
)

// Plan is plan.json: the user's goals, checklist, training week and reminders.
type Plan struct {
	Goals     []Goal             `json:"goals"`
	Weight    WeightPlan         `json:"weight"`
	Checklist []string           `json:"checklist"`
	Schedule  map[string]string  `json:"schedule"` // weekday ("Monday") → session key
	Sessions  map[string]Session `json:"sessions"`
	Notes     []string           `json:"notes"`
	Layout    [][]string         `json:"layout,omitempty"` // Today's card ids per column, if moved
}

type Goal struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type WeightPlan struct {
	Start     float64  `json:"start"`
	StartDate string   `json:"startDate"`
	Target    FlexText `json:"target"` // "170" or "207–218"
	Best      float64  `json:"best"`
}

// Session is one kind of training day.
type Session struct {
	Title     string      `json:"title"`
	Exercises [][2]string `json:"exercises,omitempty"` // [name, sets × reps]
	Note      string      `json:"note,omitempty"`
}

// Weekdays in week order, Monday first.
var Weekdays = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// SessionFor is the session planned for a weekday, and its key.
func (p Plan) SessionFor(weekday string) (key string, session Session, planned bool) {
	key = p.Schedule[weekday]
	session, planned = p.Sessions[key]
	return key, session, planned
}

// DaysUsing lists the weekdays a session is planned on.
func (p Plan) DaysUsing(key string) []string {
	var days []string
	for _, day := range Weekdays {
		if p.Schedule[day] == key {
			days = append(days, day)
		}
	}
	return days
}

// LoadPlan reads plan.json (creating it from the example on first run).
func (s *Store) LoadPlan() (Plan, error) {
	data, err := s.PlanJSON()
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Plan{}, err
	}
	if plan.Schedule == nil {
		plan.Schedule = map[string]string{}
	}
	if plan.Sessions == nil {
		plan.Sessions = map[string]Session{}
	}
	return plan, nil
}

// UpdatePlan loads the plan, applies change, and saves it, as one step.
func (s *Store) UpdatePlan(change func(*Plan)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatePlan(change)
}

// updatePlan is UpdatePlan for a caller that holds s.mu.
func (s *Store) updatePlan(change func(*Plan)) error {
	plan, err := s.LoadPlan()
	if err != nil {
		return err
	}
	change(&plan)
	return jsonfile.Write(s.paths.Plan(), plan)
}

// FlexText is text that plan.json may also hold as a number (a target of 170).
type FlexText string

func (t *FlexText) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*t = FlexText(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*t = FlexText(n.String())
	return nil
}

func (t FlexText) String() string { return string(t) }

// FormatPounds writes a weight the way it's shown: 182.4, 230 (no trailing .0).
func FormatPounds(lb float64) string { return strconv.FormatFloat(lb, 'f', -1, 64) }

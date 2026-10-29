package store

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

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
	// NoMyFitnessPal keeps new weigh-ins out of MyFitnessPal (the Weight card's checkbox, unticked).
	NoMyFitnessPal bool `json:"noMyFitnessPal,omitempty"`
	// Range is what the card's chart, change, average and trend cover: "7d", "14d", "30d",
	// "3m" (the default), "6m", "1y", "3y" or "all".
	Range string `json:"range,omitempty"`
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

var nonWordRun = regexp.MustCompile(`[^a-z0-9]+`)

// SessionKey is the key of the session with this title (or key), not case sensitive. One that
// isn't there is added.
func (p *Plan) SessionKey(name string) string {
	for key, session := range p.Sessions {
		if strings.EqualFold(key, name) || strings.EqualFold(strings.TrimSpace(session.Title), strings.TrimSpace(name)) {
			return key
		}
	}
	key := strings.Trim(nonWordRun.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if key == "" {
		key = "session"
	}
	for p.Sessions[key].Title != "" {
		key += "-2"
	}
	p.Sessions[key] = Session{Title: name}
	return key
}

var targetNumber = regexp.MustCompile(`\d+(?:\.\d+)?`)

// TargetRange reads the weight target: one weight ("170") or a range ("207–218"), both ends the
// same for one weight. ok is false without a number.
func (w WeightPlan) TargetRange() (low, high float64, ok bool) {
	numbers := targetNumber.FindAllString(strings.ReplaceAll(string(w.Target), ",", ""), 2)
	if len(numbers) == 0 {
		return 0, 0, false
	}
	low, _ = strconv.ParseFloat(numbers[0], 64)
	high = low
	if len(numbers) == 2 {
		high, _ = strconv.ParseFloat(numbers[1], 64)
	}
	if high < low {
		low, high = high, low
	}
	return low, high, low > 0
}

// WithStart fills in the starting weight from the weigh-ins when none is set: the first one on or
// after the start date (any, without a date). Then "since" counts from a weight you logged.
func (w WeightPlan) WithStart(weights []Weight) WeightPlan {
	if w.Start > 0 {
		return w
	}
	for _, weight := range weights {
		if weight.Date >= w.StartDate {
			w.Start, w.StartDate = weight.LB, weight.Date
			break
		}
	}
	return w
}

// Lowest is the lowest weight: the plan's "lowest ever" or a lower weigh-in, 0 with neither.
func (w WeightPlan) Lowest(weights []Weight) float64 {
	lowest := w.Best
	for _, weight := range weights {
		if weight.LB > 0 && (lowest == 0 || weight.LB < lowest) {
			lowest = weight.LB
		}
	}
	return lowest
}

// TargetReached says whether a weight meets the plan's target: inside a range, or at or past a
// single target in the plan's direction (down to it when the start was above it, up to it when
// the start was below).
func TargetReached(current float64, w WeightPlan) bool {
	low, high, ok := w.TargetRange()
	if !ok || current <= 0 {
		return false
	}
	if current >= low && current <= high {
		return true
	}
	if low == high {
		if w.Start >= low {
			return current <= low
		}
		return current >= low
	}
	return false
}

// ToGo is how far a weight is from the target's nearest end, in lb (0 when it's reached).
func ToGo(current float64, w WeightPlan) float64 {
	low, high, ok := w.TargetRange()
	if !ok || current <= 0 || TargetReached(current, w) {
		return 0
	}
	if current > high {
		return math.Round((current-high)*10) / 10
	}
	return math.Round((low-current)*10) / 10
}

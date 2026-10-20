// Package store owns the dashboard's own files: the plan, checklist history, weigh-ins and docs.
package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dashboard/internal/config"
	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
	"dashboard/internal/text"
)

// Store guards the files that are read, changed and written back (plan, checklist, weights),
// so two requests at once can't lose each other's changes.
type Store struct {
	paths config.Paths
	mu    sync.Mutex
}

func New(paths config.Paths) *Store {
	os.MkdirAll(paths.Data, 0o700)
	return &Store{paths: paths}
}

// ---------- plan ----------

// PlanJSON returns plan.json, creating it from plan.example.json on first run.
func (s *Store) PlanJSON() ([]byte, error) {
	if _, err := os.Stat(s.paths.Plan()); os.IsNotExist(err) {
		example, err := os.ReadFile(s.paths.PlanExample())
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(s.paths.Plan(), example, 0o600); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(s.paths.Plan())
}

// PlanChecklist is the plan's list of daily checklist items.
func (s *Store) PlanChecklist() []string {
	var plan struct {
		Checklist []string `json:"checklist"`
	}
	jsonfile.Read(s.paths.Plan(), &plan)
	return plan.Checklist
}

// ---------- checklist ----------
// Each day maps an item to "done", "skip", or {"state": "skip", "note": "why"}.

func (s *Store) checklistFile() string { return s.paths.DataFile("checklist.json") }

// Checklist returns every day's items, cleaned up (older saves used true for done).
func (s *Store) Checklist() map[string]map[string]any {
	var days map[string]map[string]any
	jsonfile.Read(s.checklistFile(), &days)
	cleaned := make(map[string]map[string]any, len(days))
	for day, items := range days {
		cleaned[day] = cleanItems(items)
	}
	return cleaned
}

// SaveChecklistDay replaces one day's items.
func (s *Store) SaveChecklistDay(day string, items map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	days := s.Checklist()
	trimmed := make(map[string]any, len(items))
	for name, state := range items {
		trimmed[text.Truncate(name, 80)] = state
	}
	days[day] = cleanItems(trimmed)
	return jsonfile.Write(s.checklistFile(), days)
}

// SetSkipReason changes why a checklist item was skipped on a day.
func (s *Store) SetSkipReason(day, item, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	days := s.Checklist()
	if state, _ := checkState(days[day][item]); state == nil || state == "done" {
		return problem.Invalidf("that item wasn't skipped that day")
	}
	if reason = text.Truncate(text.Collapse(reason), 300); reason != "" {
		days[day][item] = map[string]any{"state": "skip", "note": reason}
	} else {
		days[day][item] = "skip"
	}
	return jsonfile.Write(s.checklistFile(), days)
}

func cleanItems(items map[string]any) map[string]any {
	cleaned := map[string]any{}
	for name, state := range items {
		if value, ok := checkState(state); ok {
			cleaned[name] = value
		}
	}
	return cleaned
}

// checkState normalizes one checklist value; ok is false for "not done".
func checkState(v any) (value any, ok bool) {
	switch state := v.(type) {
	case bool:
		return "done", state
	case string:
		return state, state == "done" || state == "skip"
	case map[string]any:
		if state["state"] != "skip" {
			return nil, false
		}
		note, _ := state["note"].(string)
		if note = text.Truncate(strings.TrimSpace(note), 300); note != "" {
			return map[string]any{"state": "skip", "note": note}, true
		}
		return "skip", true
	}
	return nil, false
}

// ---------- weigh-ins ----------

type Weight struct {
	Date string  `json:"date"` // YYYY-MM-DD
	LB   float64 `json:"lb"`
}

func (s *Store) weightsFile() string { return s.paths.DataFile("weights.json") }

func (s *Store) Weights() []Weight {
	weights := []Weight{}
	jsonfile.Read(s.weightsFile(), &weights)
	return weights
}

// SetWeight logs (or, with nil, removes) the weigh-in for a day.
func (s *Store) SetWeight(day string, lb *float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byDay := s.weightsByDay()
	delete(byDay, day)
	if lb != nil {
		byDay[day] = Weight{Date: day, LB: *lb}
	}
	return s.saveWeights(byDay)
}

// MergeWeighIns adds weigh-ins from MyFitnessPal, which wins for days it has. A weigh-in on
// `today` also ticks the "Weigh in" checklist item, if the plan has one.
func (s *Store) MergeWeighIns(weighIns []Weight, today string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byDay := s.weightsByDay()
	weighedToday := false
	for _, w := range weighIns {
		byDay[w.Date] = w
		weighedToday = weighedToday || w.Date == today
	}
	if err := s.saveWeights(byDay); err != nil || !weighedToday {
		return err
	}
	return s.tickWeighIn(today)
}

// TickWeighIn ticks the plan's "Weigh in" checklist item for the day, unless it's already
// ticked or skipped.
func (s *Store) TickWeighIn(day string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tickWeighIn(day)
}

func (s *Store) tickWeighIn(day string) error {
	item := ""
	for _, name := range s.PlanChecklist() {
		if strings.Contains(strings.ToLower(name), "weigh") {
			item = name
			break
		}
	}
	if item == "" {
		return nil
	}
	var days map[string]map[string]any
	jsonfile.Read(s.checklistFile(), &days)
	if days == nil {
		days = map[string]map[string]any{}
	}
	if days[day] == nil {
		days[day] = map[string]any{}
	}
	if current, _ := checkState(days[day][item]); current != nil {
		return nil // already ticked or skipped
	}
	days[day][item] = "done"
	return jsonfile.Write(s.checklistFile(), days)
}

func (s *Store) weightsByDay() map[string]Weight {
	byDay := map[string]Weight{}
	for _, w := range s.Weights() {
		byDay[w.Date] = w
	}
	return byDay
}

func (s *Store) saveWeights(byDay map[string]Weight) error {
	weights := make([]Weight, 0, len(byDay))
	for _, w := range byDay {
		weights = append(weights, w)
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i].Date < weights[j].Date })
	return jsonfile.Write(s.weightsFile(), weights)
}

// ---------- docs ----------

// SeedDocs copies the starter notes into docs/ on first run. It never overwrites a file.
func (s *Store) SeedDocs() {
	templates, _ := filepath.Glob(filepath.Join(s.paths.DocTemplates, "*.md"))
	for _, template := range templates {
		target := filepath.Join(s.paths.Docs, filepath.Base(template))
		if _, err := os.Stat(target); os.IsNotExist(err) {
			if content, err := os.ReadFile(template); err == nil {
				os.WriteFile(target, content, 0o600)
			}
		}
	}
}

// DocNames lists the Markdown files in docs/, sorted.
func (s *Store) DocNames() []string {
	files, _ := filepath.Glob(filepath.Join(s.paths.Docs, "*.md"))
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	sort.Strings(names)
	return names
}

// Doc returns a doc's text. Only names from DocNames work, so no other file can be read.
func (s *Store) Doc(name string) ([]byte, bool) {
	for _, known := range s.DocNames() {
		if known == name {
			content, err := os.ReadFile(filepath.Join(s.paths.Docs, name))
			return content, err == nil
		}
	}
	return nil, false
}

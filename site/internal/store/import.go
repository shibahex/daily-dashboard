package store

import (
	"bytes"
	"encoding/json"
	"regexp"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// Importing data exported from another dashboard (Account → Privacy). Nothing here deletes:
// the plan is replaced (there's one), and days and weigh-ins are added, the imported ones
// winning for days both have.

// ImportPlan replaces plan.json with an exported one.
func (s *Store) ImportPlan(data []byte) error {
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return problem.Invalidf("plan.json isn't a dashboard plan")
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bytes.TrimSpace(data), "", " "); err != nil {
		return problem.Invalidf("plan.json isn't a dashboard plan")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return jsonfile.WriteBytes(s.paths.Plan(), pretty.Bytes())
}

// ImportChecklist adds an exported checklist's days; it answers how many.
func (s *Store) ImportChecklist(data []byte) (int, error) {
	var imported map[string]map[string]any
	if err := json.Unmarshal(data, &imported); err != nil {
		return 0, problem.Invalidf("checklist.json isn't a dashboard checklist")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	days := s.Checklist()
	count := 0
	for day, items := range imported {
		if !dayPattern.MatchString(day) {
			continue
		}
		if items = cleanItems(items); len(items) > 0 {
			days[day] = items
			count++
		}
	}
	return count, jsonfile.Write(s.checklistFile(), days)
}

// ImportWeights adds exported weigh-ins; it answers how many.
func (s *Store) ImportWeights(data []byte) (int, error) {
	var imported []Weight
	if err := json.Unmarshal(data, &imported); err != nil {
		return 0, problem.Invalidf("weights.json isn't a list of weigh-ins")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byDay := s.weightsByDay()
	count := 0
	for _, w := range imported {
		if dayPattern.MatchString(w.Date) && w.LB > 0 && w.LB < 2000 {
			byDay[w.Date] = w
			count++
		}
	}
	return count, s.saveWeights(byDay)
}

// dayPattern is a day's key, "2026-10-20".
var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

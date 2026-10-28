package myfitnesspal

import (
	"slices"
	"strings"
	"sync"
	"time"

	"dashboard/internal/jsonfile"
)

// History is what the user logged from the dashboard (data/mfp_history.json), so the food panel
// can offer the foods and saved meals they eat most, without searching MyFitnessPal again.

const (
	historyKeep = 120 // days of logs kept
	frequentFor = 30  // days "most eaten" counts over
	historyMax  = 60  // foods the panel lists
)

// Logged is one food or saved meal logged to the diary.
type Logged struct {
	Food     Food    `json:"food"`
	Serving  int     `json:"serving"` // index into Food.ServingOptions()
	Quantity float64 `json:"quantity"`
	Meal     string  `json:"meal"`
	Date     string  `json:"date"` // the diary day
	At       int64   `json:"at"`   // unix seconds
}

// Usual is a food from the history with how often it was eaten and its last amount.
type Usual struct {
	Logged      // the latest log
	Times  int  // logs in the last 30 days
	Recent bool // logged at all in the last 30 days
	Rank   int  // place in the list
}

type History struct {
	path string
	mu   sync.Mutex
}

func (h *History) load() []Logged {
	var logs []Logged
	jsonfile.Read(h.path, &logs)
	return logs
}

// Add keeps a log, dropping ones older than historyKeep days.
func (h *History) Add(entry Logged) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry.At == 0 {
		entry.At = time.Now().Unix()
	}
	cutoff := time.Now().AddDate(0, 0, -historyKeep).Unix()
	logs := slices.DeleteFunc(h.load(), func(l Logged) bool { return l.At < cutoff })
	return jsonfile.Write(h.path, append(logs, entry))
}

// Forget removes every log of a food.
func (h *History) Forget(foodID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	logs := h.load()
	kept := slices.DeleteFunc(slices.Clone(logs), func(l Logged) bool { return l.Food.FoodID == foodID })
	if len(kept) == len(logs) {
		return nil
	}
	return jsonfile.Write(h.path, kept)
}

// Usual lists the logged foods whose name or brand has every word of query, by how often they
// were eaten in the last 30 days ("frequent") or by when they were last eaten ("recent").
func (h *History) Usual(query, order string) []Usual {
	h.mu.Lock()
	logs := h.load()
	h.mu.Unlock()
	return usual(logs, query, order, time.Now())
}

func usual(logs []Logged, query, order string, now time.Time) []Usual {
	words := strings.Fields(strings.ToLower(query))
	since := now.AddDate(0, 0, -frequentFor).Unix()
	byFood := map[string]*Usual{}
	var foods []*Usual
	for _, l := range logs {
		if l.Food.FoodID == "" || !MatchesWords(l.Food.Name+" "+l.Food.Brand, words) {
			continue
		}
		u := byFood[l.Food.FoodID]
		if u == nil {
			u = &Usual{}
			byFood[l.Food.FoodID] = u
			foods = append(foods, u)
		}
		if l.At >= u.At {
			u.Logged = l
		}
		if l.At >= since {
			u.Times++
			u.Recent = true
		}
	}
	slices.SortFunc(foods, func(a, b *Usual) int {
		if order != "recent" && a.Times != b.Times {
			return b.Times - a.Times
		}
		return int(b.At - a.At)
	})
	list := make([]Usual, 0, min(len(foods), historyMax))
	for i, u := range foods[:min(len(foods), historyMax)] {
		u.Rank = i
		list = append(list, *u)
	}
	return list
}

// MatchesWords reports whether s has each of the (lower-case) words.
func MatchesWords(s string, words []string) bool {
	s = strings.ToLower(s)
	for _, word := range words {
		if !strings.Contains(s, word) {
			return false
		}
	}
	return true
}

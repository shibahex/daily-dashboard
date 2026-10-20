package anki

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/jsonfile"
)

// Status says whether AnkiConnect answers, and if not, what's missing.
type Status struct {
	Connected bool   `json:"connected"`
	Version   *int   `json:"version,omitempty"`
	Installed *bool  `json:"installed,omitempty"`
	Running   *bool  `json:"running,omitempty"`
	Addon     string `json:"addon,omitempty"`
}

func (c Client) Status() Status {
	var version int
	if err := c.callInto("version", nil, &version); err == nil {
		return Status{Connected: true, Version: &version}
	}
	installed, running := AppInstalled(), AppRunning()
	return Status{Installed: &installed, Running: &running, Addon: AnkiConnectAddon}
}

// Deck is one deck with today's counts. Parent decks include their subdecks.
type Deck struct {
	Name   string `json:"name"` // full name, e.g. "Japanese::Verbs"
	ID     int64  `json:"id"`
	New    int    `json:"new"`
	Learn  int    `json:"learn"`
	Review int    `json:"review"`
	Total  int    `json:"total"` // cards directly in this deck
}

// Overview is everything the Anki tab shows at once.
type Overview struct {
	Decks         []Deck            `json:"decks"`
	ReviewedToday int               `json:"reviewed_today"`
	ByDay         []json.RawMessage `json:"by_day"` // [["2026-10-20", 31], ...], newest first
	Models        []string          `json:"models"`
}

func (c Client) Overview() (Overview, error) {
	var deckIDs map[string]int64
	if err := c.callInto("deckNamesAndIds", nil, &deckIDs); err != nil {
		return Overview{}, err
	}
	names := make([]string, 0, len(deckIDs))
	fullName := map[string]string{} // getDeckStats only has the last part of each name
	for name, id := range deckIDs {
		names = append(names, name)
		fullName[strconv.FormatInt(id, 10)] = name
	}

	var stats map[string]struct {
		Name   string `json:"name"`
		New    int    `json:"new_count"`
		Learn  int    `json:"learn_count"`
		Review int    `json:"review_count"`
		Total  int    `json:"total_in_deck"`
	}
	if err := c.callInto("getDeckStats", map[string]any{"decks": names}, &stats); err != nil {
		return Overview{}, err
	}
	overview := Overview{Decks: []Deck{}}
	for id, stat := range stats {
		name := fullName[id]
		if name == "" {
			name = stat.Name
		}
		deckID, _ := strconv.ParseInt(id, 10, 64)
		overview.Decks = append(overview.Decks, Deck{Name: name, ID: deckID, New: stat.New, Learn: stat.Learn, Review: stat.Review, Total: stat.Total})
	}
	sort.Slice(overview.Decks, func(i, j int) bool {
		return strings.ToLower(overview.Decks[i].Name) < strings.ToLower(overview.Decks[j].Name)
	})

	if err := c.callInto("getNumCardsReviewedToday", nil, &overview.ReviewedToday); err != nil {
		return Overview{}, err
	}
	if err := c.callInto("getNumCardsReviewedByDay", nil, &overview.ByDay); err != nil {
		return Overview{}, err
	}
	if len(overview.ByDay) > 60 {
		overview.ByDay = overview.ByDay[:60]
	}
	if err := c.callInto("modelNames", nil, &overview.Models); err != nil {
		return Overview{}, err
	}
	return overview, nil
}

// History is cards reviewed per day, all time. Live is false when Anki was closed and this is
// the copy saved the last time it was open.
type History struct {
	ByDay []json.RawMessage `json:"by_day"`
	Live  bool              `json:"live"`
	At    *float64          `json:"at,omitempty"`
}

// History reads the review history from Anki and saves it to savePath, or returns the saved
// copy when Anki isn't reachable.
func (c Client) History(savePath string) History {
	var byDay []json.RawMessage
	err := c.callInto("getNumCardsReviewedByDay", nil, &byDay)
	if err == nil {
		now := float64(time.Now().UnixNano()) / 1e9
		jsonfile.Write(savePath, map[string]any{"by_day": byDay, "at": now})
		return History{ByDay: byDay, Live: true}
	}
	var saved struct {
		ByDay []json.RawMessage `json:"by_day"`
		At    *float64          `json:"at"`
	}
	jsonfile.Read(savePath, &saved)
	if saved.ByDay == nil {
		saved.ByDay = []json.RawMessage{}
	}
	return History{ByDay: saved.ByDay, Live: false, At: saved.At}
}

// IsUnreachable reports whether err means Anki didn't answer at all.
func IsUnreachable(err error) bool { return errors.Is(err, ErrUnreachable) }

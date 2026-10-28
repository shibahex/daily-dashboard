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
	Connected bool
	Addon     string
	NotSetUp  bool // its address isn't set yet
}

func (c Client) Status() Status {
	var version int
	if err := c.callInto("version", nil, &version); err == nil {
		return Status{Connected: true}
	}
	return Status{NotSetUp: c.URL == "", Addon: AnkiConnectAddon}
}

// Deck is one deck with today's counts. Parent decks include their subdecks.
type Deck struct {
	Name   string // full name, e.g. "Japanese::Verbs"
	ID     int64
	New    int
	Learn  int
	Review int
	Total  int // cards directly in this deck
}

func (d Deck) Due() int         { return d.New + d.Learn + d.Review }
func (d Deck) IsTopLevel() bool { return !strings.Contains(d.Name, "::") }
func (d Deck) Depth() int       { return strings.Count(d.Name, "::") }

// ShortName is the last part of the name: "Verbs" for "Japanese::Verbs".
func (d Deck) ShortName() string {
	parts := strings.Split(d.Name, "::")
	return parts[len(parts)-1]
}

// ReviewsByDay is cards reviewed per day ("2026-10-20" → 31). Days without reviews are missing.
type ReviewsByDay map[string]int

// On is the reviews on a day.
func (r ReviewsByDay) On(day time.Time) int { return r[day.Format("2006-01-02")] }

// Overview is everything the Anki tab shows at once.
type Overview struct {
	Decks         []Deck
	ReviewedToday int
	Reviews       ReviewsByDay
	Models        []string
}

func (c Client) Overview() (Overview, error) {
	var deckIDs map[string]int64
	if err := c.callInto("deckNamesAndIds", nil, &deckIDs); err != nil {
		return Overview{}, describe(err)
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
		return Overview{}, describe(err)
	}
	var overview Overview
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
		return Overview{}, describe(err)
	}
	reviews, err := c.reviewsByDay()
	if err != nil {
		return Overview{}, describe(err)
	}
	overview.Reviews = reviews
	if err := c.callInto("modelNames", nil, &overview.Models); err != nil {
		return Overview{}, describe(err)
	}
	return overview, nil
}

// TopLevelDecks are the decks that aren't subdecks. Their counts include their subdecks.
func (o Overview) TopLevelDecks() []Deck {
	var top []Deck
	for _, deck := range o.Decks {
		if deck.IsTopLevel() {
			top = append(top, deck)
		}
	}
	return top
}

// CardsIn is how many cards a deck and its subdecks have.
func (o Overview) CardsIn(deck Deck) int {
	cards := 0
	for _, other := range o.Decks {
		if other.Name == deck.Name || strings.HasPrefix(other.Name, deck.Name+"::") {
			cards += other.Total
		}
	}
	return cards
}

// reviewsByDay asks AnkiConnect for reviews per day; it answers [["2026-10-20", 31], ...].
func (c Client) reviewsByDay() (ReviewsByDay, error) {
	var pairs [][2]json.RawMessage
	if err := c.callInto("getNumCardsReviewedByDay", nil, &pairs); err != nil {
		return nil, err
	}
	return reviewsFromPairs(pairs), nil
}

func reviewsFromPairs(pairs [][2]json.RawMessage) ReviewsByDay {
	reviews := ReviewsByDay{}
	for _, pair := range pairs {
		var day string
		var count int
		if json.Unmarshal(pair[0], &day) == nil && json.Unmarshal(pair[1], &count) == nil {
			reviews[day] = count
		}
	}
	return reviews
}

// History is cards reviewed per day, all time. Live is false when Anki was closed and this is
// the copy saved the last time it was open.
type History struct {
	Reviews ReviewsByDay
	Live    bool
}

// History reads the review history from Anki and saves it to savePath, or returns the saved
// copy when Anki isn't reachable.
func (c Client) History(savePath string) History {
	var pairs [][2]json.RawMessage
	if err := c.callInto("getNumCardsReviewedByDay", nil, &pairs); err == nil {
		jsonfile.Write(savePath, map[string]any{"by_day": pairs, "at": float64(time.Now().UnixNano()) / 1e9})
		return History{Reviews: reviewsFromPairs(pairs), Live: true}
	}
	var saved struct {
		ByDay [][2]json.RawMessage `json:"by_day"`
	}
	jsonfile.Read(savePath, &saved)
	return History{Reviews: reviewsFromPairs(saved.ByDay)}
}

// IsUnreachable reports whether err means Anki didn't answer at all.
func IsUnreachable(err error) bool {
	return errors.Is(err, ErrUnreachable) || errors.Is(err, ErrNotSetUp)
}

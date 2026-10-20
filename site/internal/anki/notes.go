package anki

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"dashboard/internal/problem"
)

// Note is one note with its fields in the note type's order.
type Note struct {
	ID        int64
	ModelName string
	Tags      []string
	Fields    []Field
	CardIDs   []int64
	Deck      string // the first card's deck
	Suspended bool   // every card is suspended
}

type Field struct {
	Name  string
	Value string // HTML, as Anki stores it
}

// SearchNotes finds notes with Anki's search syntax, newest first, at most limit of them, and
// returns how many matched in all.
func (c Client) SearchNotes(query string, limit int) (notes []Note, total int, err error) {
	var ids []int64
	if err := c.callInto("findNotes", map[string]any{"query": query}, &ids); err != nil {
		return nil, 0, describe(err)
	}
	// Note ids are creation times, so the biggest are the newest.
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	total = len(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return []Note{}, total, nil
	}

	var infos []struct {
		NoteID    int64    `json:"noteId"`
		ModelName string   `json:"modelName"`
		Tags      []string `json:"tags"`
		Cards     []int64  `json:"cards"`
		Fields    map[string]struct {
			Value string `json:"value"`
			Order int    `json:"order"`
		} `json:"fields"`
	}
	if err := c.callInto("notesInfo", map[string]any{"notes": ids}, &infos); err != nil {
		return nil, 0, describe(err)
	}
	var cardIDs []int64
	for _, info := range infos {
		cardIDs = append(cardIDs, info.Cards...)
	}
	var cards []struct {
		CardID   int64  `json:"cardId"`
		DeckName string `json:"deckName"`
		Queue    int    `json:"queue"` // -1 = suspended
	}
	if len(cardIDs) > 0 {
		if err := c.callInto("cardsInfo", map[string]any{"cards": cardIDs}, &cards); err != nil {
			return nil, 0, describe(err)
		}
	}
	deckOf, suspended := map[int64]string{}, map[int64]bool{}
	for _, card := range cards {
		deckOf[card.CardID], suspended[card.CardID] = card.DeckName, card.Queue == -1
	}

	for _, info := range infos {
		note := Note{ID: info.NoteID, ModelName: info.ModelName, Tags: info.Tags, CardIDs: info.Cards}
		for name, field := range info.Fields {
			note.Fields = append(note.Fields, Field{Name: name, Value: field.Value})
		}
		sort.Slice(note.Fields, func(i, j int) bool {
			return info.Fields[note.Fields[i].Name].Order < info.Fields[note.Fields[j].Name].Order
		})
		note.Suspended = len(info.Cards) > 0
		for i, cardID := range info.Cards {
			if i == 0 {
				note.Deck = deckOf[cardID]
			}
			note.Suspended = note.Suspended && suspended[cardID]
		}
		notes = append(notes, note)
	}
	return notes, total, nil
}

// FieldNames are a note type's fields, in order.
func (c Client) FieldNames(modelName string) ([]string, error) {
	var names []string
	err := c.callInto("modelFieldNames", map[string]any{"modelName": modelName}, &names)
	return names, describe(err)
}

// AddNote adds a note; duplicates (same first field in the deck) are refused.
func (c Client) AddNote(deck, modelName string, fields map[string]string, tags []string) error {
	note := map[string]any{"deckName": deck, "modelName": modelName, "fields": fields, "tags": tags,
		"options": map[string]any{"allowDuplicate": false}}
	_, err := c.Call("addNote", map[string]any{"note": note})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return problem.Invalidf("That card already exists")
	}
	return describe(err)
}

// UpdateNote replaces a note's fields and tags. Anki can't change a note that's open in its
// browser, so that error explains what to do.
func (c Client) UpdateNote(id int64, fields map[string]string, tags []string) error {
	if _, err := c.Call("updateNoteFields", map[string]any{"note": map[string]any{"id": id, "fields": fields}}); err != nil {
		return describe(err)
	}
	_, err := c.Call("updateNoteTags", map[string]any{"note": id, "tags": tags})
	return describe(err)
}

func (c Client) DeleteNote(id int64) error {
	_, err := c.Call("deleteNotes", map[string]any{"notes": []int64{id}})
	return describe(err)
}

// SetSuspended suspends (hides from reviews) or unsuspends cards.
func (c Client) SetSuspended(cardIDs []int64, suspended bool) error {
	action := "unsuspend"
	if suspended {
		action = "suspend"
	}
	_, err := c.Call(action, map[string]any{"cards": cardIDs})
	return describe(err)
}

func (c Client) CreateDeck(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return problem.Invalidf("name the deck")
	}
	_, err := c.Call("createDeck", map[string]any{"deck": name})
	return describe(err)
}

// OpenDeck shows a deck's overview in Anki, ready to study.
func (c Client) OpenDeck(name string) error {
	_, err := c.Call("guiDeckOverview", map[string]any{"name": name})
	return describe(err)
}

// OpenNoteInBrowser shows a note in Anki's browser.
func (c Client) OpenNoteInBrowser(id int64) error {
	_, err := c.Call("guiBrowse", map[string]any{"query": "nid:" + strconv.FormatInt(id, 10)})
	return describe(err)
}

// Sync syncs the collection with AnkiWeb.
func (c Client) Sync() error {
	_, err := c.Call("sync", nil)
	return describe(err)
}

// describe turns AnkiConnect errors into problems: unreachable, or Anki's own message.
func describe(err error) error {
	var actionErr ActionError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &actionErr):
		return problem.Invalidf(actionErr.Message)
	case IsUnreachable(err):
		return problem.Unavailablef(err.Error())
	}
	return err
}

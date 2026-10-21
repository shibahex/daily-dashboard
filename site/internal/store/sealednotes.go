package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// Sealed notes are notes encrypted end to end: the user's browser encrypts them with a key only
// it has (see web/js/keys.js), and the server keeps one opaque blob it can't read. The version
// goes up with every save, so a tab working on an old copy can't overwrite a newer one.

// SealedNotesMax is the most the blob may hold (base64).
const SealedNotesMax = 8 << 20

type sealedNotes struct {
	Version int    `json:"version"`
	Blob    string `json:"blob"`
}

func (s *Store) sealedFile() string { return filepath.Join(s.paths.Data, "notes.sealed.json") }

// SealedNotes is the blob and its version (0 and "" before the first save).
func (s *Store) SealedNotes() (version int, blob string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved sealedNotes
	jsonfile.Read(s.sealedFile(), &saved)
	return saved.Version, saved.Blob
}

// SaveSealedNotes replaces the blob if it's still at version, and returns the new version.
func (s *Store) SaveSealedNotes(version int, blob string) (int, error) {
	if len(blob) > SealedNotesMax {
		return 0, problem.Invalidf("your notes are too big to save")
	}
	if _, err := base64.StdEncoding.DecodeString(blob); err != nil || blob == "" {
		return 0, problem.Invalidf("that isn't encrypted notes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved sealedNotes
	jsonfile.Read(s.sealedFile(), &saved)
	if saved.Version != version {
		return saved.Version, problem.Conflictf("your notes changed in another tab: reload to see them")
	}
	saved = sealedNotes{Version: version + 1, Blob: blob}
	return saved.Version, jsonfile.Write(s.sealedFile(), saved)
}

// ClearSealedNotes forgets the blob, when a reset starts the account over.
func (s *Store) ClearSealedNotes() error {
	err := os.Remove(s.sealedFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PlainNote is a note from before notes were sealed, for the browser to seal.
type PlainNote struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PlainNotes are the user's notes still in plain files (not the guides), in their order.
func (s *Store) PlainNotes() []PlainNote {
	var notes []PlainNote
	for _, name := range s.DocNames() {
		if slices.Contains(GuideNames, name) {
			continue
		}
		if content, err := os.ReadFile(s.notePath(name)); err == nil {
			notes = append(notes, PlainNote{Name: name, Text: string(content)})
		}
	}
	return notes
}

// DropPlainNotes deletes the plain note files, once the browser has sealed them.
func (s *Store) DropPlainNotes(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if !slices.Contains(GuideNames, name) && filepath.Base(name) == name {
			os.Remove(s.notePath(name))
		}
	}
}

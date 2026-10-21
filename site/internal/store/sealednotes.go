package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// Sealed blobs are encrypted end to end: the user's browser encrypts them with a key only it
// has (see web/js/keys.js), and the server keeps blobs it can't read: the notes ("notes") and
// the AI chat ("chat"). The version goes up with every save, so a tab working on an old copy
// can't overwrite a newer one.

// SealedMax is the most a blob may hold (base64).
const SealedMax = 8 << 20

// SealedKinds are the blobs there are.
var SealedKinds = []string{"notes", "chat"}

type sealedBlob struct {
	Version int    `json:"version"`
	Blob    string `json:"blob"`
}

func (s *Store) sealedFile(kind string) string {
	return filepath.Join(s.paths.Data, kind+".sealed.json")
}

// Sealed is a blob and its version (0 and "" before the first save).
func (s *Store) Sealed(kind string) (version int, blob string) {
	if !slices.Contains(SealedKinds, kind) {
		return 0, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved sealedBlob
	jsonfile.Read(s.sealedFile(kind), &saved)
	return saved.Version, saved.Blob
}

// SaveSealed replaces a blob if it's still at version, and returns the new version.
func (s *Store) SaveSealed(kind string, version int, blob string) (int, error) {
	if !slices.Contains(SealedKinds, kind) {
		return 0, problem.Invalidf("unknown kind of data")
	}
	if len(blob) > SealedMax {
		return 0, problem.Invalidf("that's too big to save")
	}
	if _, err := base64.StdEncoding.DecodeString(blob); err != nil || blob == "" {
		return 0, problem.Invalidf("that isn't encrypted data")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved sealedBlob
	jsonfile.Read(s.sealedFile(kind), &saved)
	if saved.Version != version {
		return saved.Version, problem.Conflictf("this changed in another tab: reload to see it")
	}
	saved = sealedBlob{Version: version + 1, Blob: blob}
	return saved.Version, jsonfile.Write(s.sealedFile(kind), saved)
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

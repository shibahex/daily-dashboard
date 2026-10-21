package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// The user's notes are the Markdown files in their docs folder (not the guides). The Docs tab
// edits, adds, renames, deletes and reorders them; their order is kept in the plan.

// NoteMaxBytes is the most a note may hold.
const NoteMaxBytes = 1 << 20

// noteName is what a note's file may be called: lowercase words joined by dashes.
var noteName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,59}\.md$`)

// NoteFileName is the file a note with this title goes in: "Nutrition plan" → nutrition-plan.md.
func NoteFileName(title string) (string, error) {
	var slug strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	name := strings.TrimSuffix(slug.String(), "-md")
	if len(name) > 60 {
		name = strings.TrimRight(name[:60], "-")
	}
	if name == "" {
		return "", problem.Invalidf("give the note a name with letters or numbers")
	}
	return name + ".md", nil
}

// IsNote reports whether name is one of the user's notes (not a guide).
func (s *Store) IsNote(name string) bool {
	return name != "" && !slices.Contains(GuideNames, name) && slices.Contains(s.DocNames(), name)
}

func (s *Store) notePath(name string) string { return filepath.Join(s.paths.Docs, name) }

// CreateNote starts a new note from a title, with the title as its heading, and returns its
// file name. A title already in use gets a number ("ideas-2.md").
func (s *Store) CreateNote(title string) (string, error) {
	base, err := NoteFileName(title)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.paths.Docs, 0o700); err != nil {
		return "", err
	}
	name := base
	for i := 2; s.taken(name); i++ {
		name = fmt.Sprintf("%s-%d.md", strings.TrimSuffix(base, ".md"), i)
	}
	heading := "# " + strings.Join(strings.Fields(title), " ") + "\n\n"
	return name, jsonfile.WriteBytes(s.notePath(name), []byte(heading))
}

// taken reports whether a note or guide already has this name.
func (s *Store) taken(name string) bool {
	if slices.Contains(GuideNames, name) {
		return true
	}
	_, err := os.Stat(s.notePath(name))
	return err == nil
}

// SaveNote replaces a note's text and, when newName differs, renames it (keeping its place).
// It returns the note's name afterwards.
func (s *Store) SaveNote(name, newName string, content []byte) (string, error) {
	if !s.IsNote(name) {
		return "", problem.NotFoundf("that note doesn't exist")
	}
	if len(content) > NoteMaxBytes {
		return "", problem.Invalidf("that note is too long")
	}
	if newName == "" {
		newName = name
	}
	if newName != name && !noteName.MatchString(newName) {
		return "", problem.Invalidf("give the note a name with letters or numbers")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if newName != name && s.taken(newName) {
		return "", problem.Conflictf("there's already a note called " + strings.TrimSuffix(newName, ".md"))
	}
	if err := jsonfile.WriteBytes(s.notePath(name), content); err != nil {
		return "", err
	}
	if newName == name {
		return name, nil
	}
	if err := os.Rename(s.notePath(name), s.notePath(newName)); err != nil {
		return "", err
	}
	return newName, s.updatePlan(func(plan *Plan) {
		if i := slices.Index(plan.DocOrder, name); i >= 0 {
			plan.DocOrder[i] = newName
		}
	})
}

// DeleteNote moves a note into the docs folder's .deleted/ (stamped with the time), so a note
// deleted by mistake can still be found there.
func (s *Store) DeleteNote(name string) error {
	if !s.IsNote(name) {
		return problem.NotFoundf("that note doesn't exist")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trash := filepath.Join(s.paths.Docs, ".deleted")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	kept := strings.TrimSuffix(name, ".md") + "-" + time.Now().Format("2006-01-02-150405") + ".md"
	if err := os.Rename(s.notePath(name), filepath.Join(trash, kept)); err != nil {
		return err
	}
	return s.updatePlan(func(plan *Plan) {
		plan.DocOrder = slices.DeleteFunc(plan.DocOrder, func(n string) bool { return n == name })
	})
}

// SaveNoteOrder saves the order of the notes in the Docs tab.
func (s *Store) SaveNoteOrder(names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatePlan(func(plan *Plan) { plan.DocOrder = names })
}

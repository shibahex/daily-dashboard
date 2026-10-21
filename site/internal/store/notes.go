package store

import "path/filepath"

// notePath is where a note from before notes were sealed is (see PlainNotes).
func (s *Store) notePath(name string) string { return filepath.Join(s.paths.Docs, name) }

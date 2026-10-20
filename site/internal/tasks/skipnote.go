package tasks

import "strings"

// SkipNote is the note a skipped task keeps: "Skipped: tired" or just "Dismissed".
type SkipNote struct {
	Label  string // "Skipped" or "Dismissed"
	Reason string
}

func (n SkipNote) String() string {
	if n.Reason == "" {
		return n.Label
	}
	return n.Label + ": " + n.Reason
}

func (n SkipNote) IsDismiss() bool { return n.Label == "Dismissed" }

// ParseSkipNote reads a skip note from an annotation.
func ParseSkipNote(annotation string) (SkipNote, bool) {
	var note SkipNote
	switch {
	case strings.HasPrefix(annotation, "Skipped"):
		note.Label = "Skipped"
	case strings.HasPrefix(annotation, "Dismissed"):
		note.Label = "Dismissed"
	default:
		return SkipNote{}, false
	}
	if _, reason, found := strings.Cut(annotation, ":"); found {
		note.Reason = strings.TrimSpace(reason)
	}
	return note, true
}

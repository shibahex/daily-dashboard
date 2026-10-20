package taskwarrior

import "regexp"

// Task is one task as `task export` prints it. Taskwarrior adds fields freely (UDAs,
// annotations, ...), so it stays a map; the methods read the common ones.
type Task map[string]any

func (t Task) field(name string) string { s, _ := t[name].(string); return s }

func (t Task) UUID() string        { return t.field("uuid") }
func (t Task) Description() string { return t.field("description") }
func (t Task) Project() string     { return t.field("project") }
func (t Task) Priority() string    { return t.field("priority") }
func (t Task) Status() string      { return t.field("status") }
func (t Task) Parent() string      { return t.field("parent") } // set on a copy of a repeating task
func (t Task) Due() any            { return t["due"] }

func (t Task) IsRecurringTemplate() bool { return t.Status() == "recurring" }
func (t Task) IsFinished() bool          { return t.Status() == "completed" || t.Status() == "deleted" }

func (t Task) Tags() []string {
	raw, _ := t["tags"].([]any)
	tags := make([]string, 0, len(raw))
	for _, tag := range raw {
		if s, ok := tag.(string); ok {
			tags = append(tags, s)
		}
	}
	return tags
}

// Annotation is a note attached to a task, with when it was added.
type Annotation struct {
	Entry       string
	Description string
}

func (t Task) Annotations() []Annotation {
	raw, _ := t["annotations"].([]any)
	notes := make([]Annotation, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			entry, _ := m["entry"].(string)
			desc, _ := m["description"].(string)
			notes = append(notes, Annotation{Entry: entry, Description: desc})
		}
	}
	return notes
}

// SetAnnotationText changes the text of the i-th annotation (for re-importing the task).
func (t Task) SetAnnotationText(i int, text string) {
	if raw, ok := t["annotations"].([]any); ok && i < len(raw) {
		if m, ok := raw[i].(map[string]any); ok {
			m["description"] = text
		}
	}
}

// TimewarriorTags are the tags the on-modify hook gives a task's time:
// its description, its project and its tags.
func (t Task) TimewarriorTags() []string {
	tags := []string{t.Description()}
	if t.Project() != "" {
		tags = append(tags, t.Project())
	}
	return append(tags, t.Tags()...)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsUUID reports whether s is a task uuid (and nothing else).
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// FilterUUIDs keeps only the valid uuids.
func FilterUUIDs(candidates []string) []string {
	uuids := []string{}
	for _, c := range candidates {
		if IsUUID(c) {
			uuids = append(uuids, c)
		}
	}
	return uuids
}

// AllUUIDs reports whether every candidate is a uuid (and there is at least one).
func AllUUIDs(candidates []string) bool {
	if len(candidates) == 0 {
		return false
	}
	for _, c := range candidates {
		if !IsUUID(c) {
			return false
		}
	}
	return true
}

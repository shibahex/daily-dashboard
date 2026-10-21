package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"dashboard/internal/jsonfile"
	"dashboard/internal/store"
)

// The AI agent never gets the user's real folder. It gets a copy of what it may read (Stage),
// and only edits the policy allows come back (ApplyEdits): notes and the plan, checked.

// agentDataFiles are the data files the agent may read (in data/ of its workspace).
var agentDataFiles = []string{"weights.json", "checklist.json", "mfp.json"}

const maxAgentFile = 256 << 10

var noteName = regexp.MustCompile(`^[\w][\w .-]{0,79}\.md$`)

// Staged remembers what was copied out, to tell the agent's edits from changes made meanwhile.
type Staged struct {
	notes  map[string][32]byte
	plan   [32]byte
	sealed bool // the notes came from the browser: changes to them are dropped
}

// StageFiles fills dir (emptied first) with a copy of the user's notes, plan and data when
// read is allowed, and leaves it empty otherwise.
func (w *Workspace) StageFiles(dir string, read bool) (Staged, error) {
	staged := Staged{notes: map[string][32]byte{}}
	if err := os.RemoveAll(dir); err != nil {
		return staged, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil || !read {
		return staged, err
	}
	notesDir, dataDir := filepath.Join(dir, "notes"), filepath.Join(dir, "data")
	os.MkdirAll(notesDir, 0o700)
	os.MkdirAll(dataDir, 0o700)
	notes, _ := filepath.Glob(filepath.Join(w.Paths.Docs, "*.md"))
	for _, note := range notes {
		name := filepath.Base(note)
		if content, err := os.ReadFile(note); err == nil && !slices.Contains(store.GuideNames, name) {
			os.WriteFile(filepath.Join(notesDir, name), content, 0o600)
			staged.notes[name] = sha256.Sum256(content)
		}
	}
	if plan, err := w.Store.PlanJSON(); err == nil {
		os.WriteFile(filepath.Join(dir, "plan.json"), plan, 0o600)
		staged.plan = sha256.Sum256(plan)
	}
	for _, name := range agentDataFiles {
		if content, err := os.ReadFile(w.Paths.DataFile(name)); err == nil {
			os.WriteFile(filepath.Join(dataDir, name), content, 0o600)
		}
	}
	return staged, nil
}

// ApplyEdits brings the agent's changes to notes and the plan back from dir, and returns what
// changed. New notes are added; nothing is deleted. A file the user changed meanwhile (in the
// page) is left alone, so the agent never overwrites it. Anything else in dir is ignored.
func (w *Workspace) ApplyEdits(dir string, staged Staged) []string {
	var changed []string
	edited, _ := filepath.Glob(filepath.Join(dir, "notes", "*.md"))
	if staged.sealed {
		edited = nil // sealed notes are saved only by the browser, never here in plain text
	}
	for _, path := range edited {
		name := filepath.Base(path)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxAgentFile || !noteName.MatchString(name) ||
			slices.Contains(store.GuideNames, name) {
			continue
		}
		content, _ := os.ReadFile(path)
		before, existed := staged.notes[name]
		if existed && sha256.Sum256(content) == before {
			continue
		}
		target := filepath.Join(w.Paths.Docs, name)
		current, err := os.ReadFile(target)
		switch {
		case existed && (err != nil || sha256.Sum256(current) != before):
			continue // changed or removed in the page meanwhile
		case !existed && err == nil:
			continue // a note with that name appeared meanwhile
		}
		if os.WriteFile(target, content, 0o600) == nil {
			changed = append(changed, "notes/"+name)
		}
	}

	planPath := filepath.Join(dir, "plan.json")
	if info, err := os.Lstat(planPath); err == nil && info.Mode().IsRegular() && info.Size() <= maxAgentFile {
		plan, _ := os.ReadFile(planPath)
		var object map[string]any
		current, _ := os.ReadFile(w.Paths.Plan())
		if sha256.Sum256(plan) != staged.plan && json.Unmarshal(plan, &object) == nil && object != nil &&
			sha256.Sum256(current) == staged.plan {
			var pretty bytes.Buffer
			if json.Indent(&pretty, plan, "", " ") == nil && jsonfile.WriteBytes(w.Paths.Plan(), pretty.Bytes()) == nil {
				changed = append(changed, "plan.json")
			}
		}
	}
	return changed
}

// StageSealedNotes puts notes the browser decrypted for this one answer into the staged copy
// (in place of any left in plain files), when reading files is allowed. The agent's changes to
// them aren't brought back: only the browser can save sealed notes.
func (w *Workspace) StageSealedNotes(dir string, staged *Staged, notes []store.PlainNote, read bool) {
	staged.sealed = true
	if !read {
		return
	}
	notesDir := filepath.Join(dir, "notes")
	os.RemoveAll(notesDir)
	os.MkdirAll(notesDir, 0o700)
	for _, note := range notes {
		if noteName.MatchString(note.Name) && len(note.Text) <= maxAgentFile {
			os.WriteFile(filepath.Join(notesDir, note.Name), []byte(note.Text), 0o600)
		}
	}
}

package web

import (
	"os"
	"path/filepath"
	"testing"
)

// The notes go into the AI's workspace as files; what it changes or adds comes back by title.
func TestServerAINotesComeBackChanged(t *testing.T) {
	run := &serverRun{}
	dir := filepath.Join(t.TempDir(), "notes")
	notes := []sealedNote{{Title: "Diet / plan", Text: "eat less"}, {Title: "Diet : plan", Text: "same file name"}, {Title: "Gym", Text: "legs"}}
	if err := run.writeNotes(dir, notes); err != nil {
		t.Fatal(err)
	}
	if run.notes["Diet _ plan (2).md"].Title != "Diet : plan" {
		t.Fatalf("clashing titles need their own files: %v", run.notes)
	}
	if changed := run.changedNotes(); len(changed) != 0 {
		t.Fatalf("nothing changed yet: %+v", changed)
	}
	os.WriteFile(filepath.Join(dir, "Gym.md"), []byte("legs and arms"), 0o600)
	os.WriteFile(filepath.Join(dir, "New idea.md"), []byte("run more"), 0o600)
	changed := run.changedNotes()
	if len(changed) != 2 || changed[0] != (sealedNote{Title: "Gym", Text: "legs and arms"}) || changed[1] != (sealedNote{Title: "New idea", Text: "run more"}) {
		t.Fatalf("changed: %+v", changed)
	}
}

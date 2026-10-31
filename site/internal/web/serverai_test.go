package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/assistant"
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

func TestReadAttachmentsChecksWhatEachAITakes(t *testing.T) {
	get := func(id string) assistant.Provider { p, _ := serverProviders.Get(id); return p }
	image := `{"kind":"image","name":"meal.jpg","mediaType":"image/jpeg","data":"anBlZw=="}`
	pdf := `{"kind":"pdf","name":"label.pdf","mediaType":"application/pdf","data":"JVBERg=="}`
	got, err := readAttachments("["+image+","+pdf+"]", get("claude-code"))
	if err != nil || len(got) != 2 || string(got[0].Data) != "jpeg" || got[1].Name != "label.pdf" {
		t.Fatalf("claude code: %+v, %v", got, err)
	}
	for _, c := range []struct{ id, raw string }{
		{"codex", "[" + pdf + "]"},
		{"gemini", "[" + image + "]"},
		{"claude-code", `[{"kind":"image","mediaType":"text/html","data":"eA=="}]`},
		{"claude-code", "[" + strings.Repeat(image+",", maxAttachments) + image + "]"},
		{"claude-code", "not json"},
	} {
		if _, err := readAttachments(c.raw, get(c.id)); err == nil {
			t.Errorf("%s took %.60s", c.id, c.raw)
		}
	}
}

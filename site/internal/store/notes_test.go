package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dashboard/internal/config"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{Docs: filepath.Join(dir, "notes"), Guides: filepath.Join(dir, "guides"), PlanFile: filepath.Join(dir, "plan.json")}
	os.MkdirAll(paths.Docs, 0o700)
	os.MkdirAll(paths.Guides, 0o700)
	os.WriteFile(filepath.Join(paths.Guides, "dashboard.md"), []byte("# Guide"), 0o600)
	os.WriteFile(paths.PlanFile, []byte("{}"), 0o600)
	return New(paths)
}

func TestNoteFileName(t *testing.T) {
	for title, want := range map[string]string{"Nutrition plan": "nutrition-plan.md", "  Ideas!! 2026 ": "ideas-2026.md", "notes.md": "notes.md"} {
		if got, err := NoteFileName(title); err != nil || got != want {
			t.Errorf("NoteFileName(%q) = %q, %v; want %q", title, got, err, want)
		}
	}
	if _, err := NoteFileName("!!!"); err == nil {
		t.Error("a title without letters should fail")
	}
}

func TestNotes(t *testing.T) {
	s := testStore(t)

	name, err := s.CreateNote("Gym ideas")
	if err != nil || name != "gym-ideas.md" {
		t.Fatalf("CreateNote = %q, %v", name, err)
	}
	if again, _ := s.CreateNote("Gym ideas"); again != "gym-ideas-2.md" {
		t.Errorf("a second note with the same title got %q", again)
	}
	if content, _ := s.Doc(name); string(content) != "# Gym ideas\n\n" {
		t.Errorf("new note holds %q", content)
	}

	// Saving with a new name renames the file and keeps its place in the order.
	if err := s.SaveNoteOrder([]string{"gym-ideas-2.md", name}); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.SaveNote(name, "lifting.md", []byte("squats"))
	if err != nil || renamed != "lifting.md" {
		t.Fatalf("SaveNote = %q, %v", renamed, err)
	}
	if content, found := s.Doc("lifting.md"); !found || string(content) != "squats" {
		t.Errorf("renamed note holds %q (found %v)", content, found)
	}
	if s.IsNote(name) {
		t.Error("the old name should be gone")
	}
	plan, _ := s.LoadPlan()
	if !slices.Equal(plan.DocOrder, []string{"gym-ideas-2.md", "lifting.md"}) {
		t.Errorf("order after renaming: %v", plan.DocOrder)
	}
	if _, err := s.SaveNote("lifting.md", "gym-ideas-2.md", nil); err == nil {
		t.Error("renaming onto another note should fail")
	}

	// Guides can't be changed, deleted or shadowed.
	if _, err := s.SaveNote("dashboard.md", "", []byte("mine")); err == nil {
		t.Error("a guide shouldn't be editable")
	}
	if err := s.DeleteNote("dashboard.md"); err == nil {
		t.Error("a guide shouldn't be deletable")
	}
	if _, err := s.SaveNote("lifting.md", "dashboard.md", nil); err == nil {
		t.Error("a note shouldn't take a guide's name")
	}
	if _, err := s.SaveNote("../plan.json", "", nil); err == nil {
		t.Error("only notes can be saved")
	}

	// Deleting keeps a copy in .deleted and drops it from the order.
	if err := s.DeleteNote("lifting.md"); err != nil {
		t.Fatal(err)
	}
	if s.IsNote("lifting.md") {
		t.Error("deleted note still listed")
	}
	if kept, _ := filepath.Glob(filepath.Join(s.paths.Docs, ".deleted", "lifting-*.md")); len(kept) != 1 {
		t.Errorf("deleted note not kept: %v", kept)
	}
	plan, _ = s.LoadPlan()
	if !slices.Equal(plan.DocOrder, []string{"gym-ideas-2.md"}) {
		t.Errorf("order after deleting: %v", plan.DocOrder)
	}
}

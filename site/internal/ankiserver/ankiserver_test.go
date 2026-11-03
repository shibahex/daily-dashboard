package ankiserver

import (
	"os"
	"path/filepath"
	"testing"

	"dashboard/internal/egress"
	"dashboard/internal/sandbox"
)

// TestAnkiRunsInItsSandbox runs a real Anki (DASHBOARD_ANKI, which `nix develop` sets): it starts
// when asked, studies, stops whole, and keeps its cards for the next start.
func TestAnkiRunsInItsSandbox(t *testing.T) {
	program := os.Getenv("DASHBOARD_ANKI")
	if program == "" {
		t.Skip("set DASHBOARD_ANKI (nix develop does) to run Anki")
	}
	tools, err := sandbox.Find()
	if err != nil {
		t.Skip(err)
	}
	runner := New(program, tools, egress.New())
	user, dir := "abc123", t.TempDir()
	client := runner.Client(user, dir)
	if runner.Running(user) {
		t.Fatal("started before anything asked")
	}
	if err := client.AddNote("Default", "Basic", map[string]string{"Front": "Capital of France?", "Back": "Paris"}, nil); err != nil {
		t.Fatal(err)
	}
	if !runner.Running(user) {
		t.Fatal("not running after use")
	}
	if status, err := client.ServerStatus(); err != nil || status.SignedIn {
		t.Fatalf("status: %+v %v", status, err)
	}

	study, err := client.StudyNext("Default")
	if err != nil || study.Done || study.Card == nil || study.Counts[0] != 1 || len(study.Card.Buttons) != 4 {
		t.Fatalf("study: %+v %v", study, err)
	}
	if study.Card.Question != "Capital of France?" || study.Card.Buttons[0] != "<1m" {
		t.Errorf("card: %+v", study.Card)
	}
	if err := client.Answer(study.Card.ID, study.Card.Reps, 3); err != nil {
		t.Fatal(err)
	}
	if err := client.Answer(study.Card.ID, study.Card.Reps, 3); err == nil {
		t.Error("the same answer counted twice")
	}

	// Media the card frame inlines.
	if _, err := client.Call("storeMediaFile", map[string]string{"filename": "dot.png", "data": "UE5H"}); err != nil {
		t.Fatal(err)
	}
	if data, err := client.MediaFile("dot.png"); err != nil || string(data) != "PNG" {
		t.Errorf("media: %q %v", data, err)
	}
	if data, err := client.MediaFile("missing.png"); err != nil || data != nil {
		t.Errorf("missing media: %q %v", data, err)
	}

	runner.Stop(user)
	if runner.Running(user) {
		t.Fatal("still running after Stop")
	}
	if _, err := os.Stat(filepath.Join(dir, Folder, "base", "User 1", "collection.anki2")); err != nil {
		t.Fatalf("no collection in the user's folder: %v", err)
	}

	// Started again on the next use, with the review kept.
	reviewed, err := client.Call("getNumCardsReviewedToday", nil)
	if err != nil || string(reviewed) != "1" {
		t.Errorf("reviews after a restart: %s %v", reviewed, err)
	}
	runner.StopAll()
}

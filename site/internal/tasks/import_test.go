package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

func TestImport(t *testing.T) {
	if _, err := exec.LookPath("task"); err != nil {
		t.Skip("task isn't installed")
	}
	dir := t.TempDir()
	rc := filepath.Join(dir, "taskrc")
	os.WriteFile(rc, []byte("data.location="+filepath.Join(dir, "task")+"\nhooks=off\n"), 0o600)
	s := NewService(taskwarrior.NewClientWithEnv([]string{"TASKRC=" + rc, "TASKDATA=" + filepath.Join(dir, "task")}), timewarrior.NewClient())
	export := []byte(`[{"uuid":"3f2b0c1e-6a1d-4c55-9a52-0d1c1a1b2c3d","description":"Walk 30 min","status":"pending","entry":"20261001T120000Z"},
{"uuid":"9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a","description":"Read","status":"completed","entry":"20261001T120000Z","end":"20261002T120000Z"}]`)
	for range 2 { // importing twice changes nothing
		if n, err := s.Import(export); err != nil || n != 2 {
			t.Fatalf("Import = %d, %v", n, err)
		}
	}
	all, err := s.Export()
	if err != nil || strings.Count(string(all), `"uuid"`) != 2 || !strings.Contains(string(all), "Walk 30 min") {
		t.Errorf("after import: %s, %v", all, err)
	}
	if _, err := s.Import([]byte(`{"not": "a list"}`)); err == nil {
		t.Error("imported something that isn't a task export")
	}
}

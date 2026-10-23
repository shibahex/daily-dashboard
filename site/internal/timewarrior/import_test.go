package timewarrior

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImport(t *testing.T) {
	if _, err := exec.LookPath("timew"); err != nil {
		t.Skip("timew isn't installed")
	}
	db := t.TempDir()
	os.MkdirAll(filepath.Join(db, "data"), 0o700)
	os.WriteFile(filepath.Join(db, "timewarrior.cfg"), nil, 0o600)
	c := NewClientWithEnv([]string{"TIMEWARRIORDB=" + db})
	export := []byte(`[
{"id":3,"start":"20261020T140000Z","end":"20261020T150000Z","tags":["Read or study 1 h"],"annotation":"chapter 2"},
{"id":2,"start":"20261020T143000Z","end":"20261020T153000Z","tags":["overlaps"]},
{"id":1,"start":"20261021T090000Z","tags":["still running"]}
]`)
	added, skipped, err := c.Import(export)
	if err != nil || added != 1 || skipped != 2 {
		t.Fatalf("Import = %d added, %d skipped, %v", added, skipped, err)
	}
	raw, _ := c.Export()
	var got []struct {
		Start, End, Annotation string
		Tags                   []string
	}
	json.Unmarshal(raw, &got)
	if len(got) != 1 || got[0].Annotation != "chapter 2" || got[0].Tags[0] != "Read or study 1 h" {
		t.Errorf("after import: %s", raw)
	}
	if added, _, _ := c.Import(export); added != 0 {
		t.Errorf("a second import added %d", added)
	}
}

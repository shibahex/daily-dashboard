package web

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The guide lists every script's hash, for anyone to check what their browser runs against the
// published code. This keeps that list true: change a script, then copy its new hash there.
func TestGuideListsTheScriptHashes(t *testing.T) {
	guide, err := os.ReadFile("../../../docs/dashboard.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("\\| `([\\w-]+\\.js)` \\| `(sha384-[A-Za-z0-9+/=]+)` \\|").FindAllStringSubmatch(string(guide), -1)
	listed := map[string]string{}
	for _, row := range rows {
		listed[row[1]] = row[2]
	}
	scripts, _ := filepath.Glob("../../web/js/*.js")
	if len(scripts) == 0 {
		t.Fatal("no scripts found")
	}
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name, want := filepath.Base(path), scriptIntegrity(data)
		if listed[name] != want {
			t.Errorf("docs/dashboard.md lists %s as %q; it's %q", name, listed[name], want)
		}
	}
}

// Command scripthashes rewrites the script hash table in docs/dashboard.md ("Checking the page's
// code") from the files in web/js, so the published list always matches the code. Run it from
// the site/ folder: go run ./cmd/scripthashes. The GitHub workflows run it on every push.
package main

import (
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	guide   = "../docs/dashboard.md"
	scripts = "web/js"
)

var row = regexp.MustCompile("^\\| `([\\w-]+\\.js)` \\| `sha384-[A-Za-z0-9+/=]+` \\|$")

func main() {
	text, err := os.ReadFile(guide)
	if err != nil {
		log.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(scripts, "*.js"))
	if err != nil || len(files) == 0 {
		log.Fatalf("no scripts in %s", scripts)
	}
	hashes := map[string]string{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		sum := sha512.Sum384(data)
		hashes[filepath.Base(path)] = "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	}

	// The table keeps its order: listed scripts that still exist, then new ones by name.
	lines := strings.Split(string(text), "\n")
	first, last := -1, -1
	var order []string
	for i, line := range lines {
		if m := row.FindStringSubmatch(line); m != nil {
			if first < 0 {
				first = i
			}
			last = i
			if _, ok := hashes[m[1]]; ok {
				order = append(order, m[1])
			}
		}
	}
	if first < 0 {
		log.Fatalf("no script hash table in %s", guide)
	}
	var added []string
	for name := range hashes {
		if !slices.Contains(order, name) {
			added = append(added, name)
		}
	}
	slices.Sort(added)

	var rows []string
	for _, name := range append(order, added...) {
		rows = append(rows, fmt.Sprintf("| `%s` | `%s` |", name, hashes[name]))
	}
	out := slices.Concat(lines[:first], rows, lines[last+1:])
	if err := os.WriteFile(guide, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		log.Fatal(err)
	}
}

package workspace

import (
	"os"
	"path/filepath"
	"slices"

	"dashboard/internal/anki"
	"dashboard/internal/config"
	"dashboard/internal/jsonfile"
	"dashboard/internal/store"
)

// legacyDataFiles are the per-person files the single-user dashboard kept in site/data.
var legacyDataFiles = []string{"checklist.json", "weights.json", "mfp.json", "chat.json", "anki_history.json"}

// migrateHostData copies the single-user dashboard's files (site/plan.json, site/data/*.json and
// the notes in docs/) into the host account's folder, once: when that folder has no plan yet.
// The originals stay where they were, as a backup.
func migrateHostData(server, user config.Paths) {
	if exists(user.Plan()) || !exists(server.Plan()) {
		return
	}
	copyFile(server.Plan(), user.Plan())
	for _, name := range legacyDataFiles {
		copyFile(filepath.Join(server.ServerData, name), user.DataFile(name))
	}
	notes, _ := filepath.Glob(filepath.Join(server.Guides, "*.md"))
	os.MkdirAll(user.Docs, 0o700)
	for _, note := range notes {
		if !slices.Contains(store.GuideNames, filepath.Base(note)) {
			copyFile(note, filepath.Join(user.Docs, filepath.Base(note)))
		}
	}
}

// copyFile copies src to dst (owner-only) unless dst exists or src doesn't.
func copyFile(src, dst string) {
	if exists(dst) {
		return
	}
	if data, err := os.ReadFile(src); err == nil {
		os.WriteFile(dst, data, 0o600)
	}
}

// ankiSettings is anki.json: the user's own AnkiConnect.
type ankiSettings struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

// loadAnki is the user's AnkiConnect from anki.json (not set up: an empty URL).
func loadAnki(dir string) anki.Client {
	var settings ankiSettings
	jsonfile.Read(filepath.Join(dir, "anki.json"), &settings)
	return anki.NewClient(settings.URL, settings.APIKey)
}

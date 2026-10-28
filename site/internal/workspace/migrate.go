package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/config"
	"dashboard/internal/jsonfile"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/store"
)

// legacyDataFiles are the per-person files the single-user dashboard kept in site/data.
var legacyDataFiles = []string{"checklist.json", "weights.json", "mfp.json", "chat.json", "anki_history.json"}

// migrateHostData copies the single-user dashboard's files (site/plan.json, site/data/*.json and
// the notes in docs/) into the host account's folder, once (a .migrated file marks it done).
// The originals stay where they were, as a backup.
func migrateHostData(server, user config.Paths) {
	marker := user.DataFile(".migrated")
	if exists(marker) {
		return
	}
	defer os.WriteFile(marker, []byte("Copied from the single-user dashboard's files.\n"), 0o600)
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

// migrateHostServices copies the server computer's own Taskwarrior, Timewarrior and MyFitnessPal
// login into the host account's folder, once (a .migrated-services file marks it done). The
// host used those before it had its own, like every other account. The originals stay where
// they were, as a backup.
func migrateHostServices(dir string, mfp myfitnesspal.Client) {
	marker := filepath.Join(dir, ".migrated-services")
	if exists(marker) {
		return
	}
	defer os.WriteFile(marker, []byte("Copied from the server computer's own Taskwarrior, Timewarrior and logins.\n"), 0o600)

	if taskData := serverTaskData(); !exists(filepath.Join(dir, "task")) {
		os.MkdirAll(filepath.Join(dir, "task"), 0o700)
		entries, _ := os.ReadDir(taskData)
		for _, entry := range entries {
			if entry.Type().IsRegular() { // the database, not the hooks folder
				copyFile(filepath.Join(taskData, entry.Name()), filepath.Join(dir, "task", entry.Name()))
			}
		}
	}

	if timewData, timewConfig := serverTimewarrior(); !exists(filepath.Join(dir, "timewarrior")) && exists(timewData) {
		copyTree(timewData, filepath.Join(dir, "timewarrior", "data"))
		copyFile(timewConfig, filepath.Join(dir, "timewarrior", "timewarrior.cfg"))
	}

	server := mfp
	server.ConfigHome, server.DataHome = "", ""
	if login := filepath.Dir(server.CookiePath()); exists(login) && !exists(filepath.Dir(mfp.CookiePath())) {
		copyTree(login, filepath.Dir(mfp.CookiePath()))
		if profile := filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"), "myfitnesspal-mcp"); exists(profile) {
			copyTree(profile, filepath.Join(mfp.DataHome, "myfitnesspal-mcp"))
		}
	}
}

// serverTaskData is the folder the server computer's own Taskwarrior keeps its tasks in.
func serverTaskData() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "task", "rc.hooks=off", "_get", "rc.data.location").Output()
	if path := strings.TrimSpace(string(out)); err == nil && path != "" {
		return config.ExpandHome(path)
	}
	return config.ExpandHome("~/.task")
}

// serverTimewarrior is the server computer's own Timewarrior data folder and config file:
// TIMEWARRIORDB, else ~/.timewarrior, else the XDG folders.
func serverTimewarrior() (data, cfg string) {
	db := os.Getenv("TIMEWARRIORDB")
	if db == "" && exists(config.ExpandHome("~/.timewarrior")) {
		db = config.ExpandHome("~/.timewarrior")
	}
	if db != "" {
		return filepath.Join(db, "data"), filepath.Join(db, "timewarrior.cfg")
	}
	return filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"), "timewarrior", "data"),
		filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "timewarrior", "timewarrior.cfg")
}

// xdgDir is the XDG folder in the variable, or its default under the home folder.
func xdgDir(variable, fallback string) string {
	if dir := os.Getenv(variable); dir != "" {
		return dir
	}
	return config.ExpandHome("~/" + fallback)
}

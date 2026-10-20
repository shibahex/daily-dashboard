// Package config says where the dashboard's files live and loads local settings from site/.env.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Paths are the folders and files the dashboard uses, all derived from the site/ folder.
type Paths struct {
	Site         string // site/: the server's own folder
	Root         string // the repo root (parent of site/)
	Docs         string // docs/: the user's notes (git-ignored) and the guides
	DocTemplates string // docs/templates/: starter notes copied into docs/ on first run
	Web          string // site/web/: the page
	Data         string // site/data/: history, weigh-ins, caches, chat (git-ignored)
}

func NewPaths(siteDir string) Paths {
	root := filepath.Dir(siteDir)
	docs := filepath.Join(root, "docs")
	return Paths{
		Site:         siteDir,
		Root:         root,
		Docs:         docs,
		DocTemplates: filepath.Join(docs, "templates"),
		Web:          filepath.Join(siteDir, "web"),
		Data:         filepath.Join(siteDir, "data"),
	}
}

func (p Paths) Plan() string                { return filepath.Join(p.Site, "plan.json") }
func (p Paths) PlanExample() string         { return filepath.Join(p.Site, "plan.example.json") }
func (p Paths) DataFile(name string) string { return filepath.Join(p.Data, name) }

// MakePrivate makes the personal files readable by their owner only: files made before the
// dashboard had an account were readable by every user on the computer.
func (p Paths) MakePrivate() {
	os.MkdirAll(p.Data, 0o700)
	os.Chmod(p.Data, 0o700)
	notes, _ := filepath.Glob(filepath.Join(p.Docs, "*.md"))
	for _, file := range append(notes, p.Plan(), filepath.Join(p.Site, ".env")) {
		if info, err := os.Stat(file); err == nil && info.Mode().Perm()&0o077 != 0 {
			os.Chmod(file, 0o600)
		}
	}
}

// LoadEnvFile reads KEY=value lines (see site/.env.example) into the environment.
// Variables that are already set win, and a leading ~ in a value means the home folder.
func LoadEnvFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.HasPrefix(key, "#") {
			continue
		}
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		os.Setenv(key, ExpandHome(strings.Trim(strings.TrimSpace(value), `"'`)))
	}
}

// ExpandHome turns "~/x" into "/home/me/x".
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

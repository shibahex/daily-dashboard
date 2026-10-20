// Package config says where the dashboard's files live and loads local settings from site/.env.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Paths are the folders and files the dashboard uses. NewPaths gives the server's own; ForUser
// gives one user's, whose data, notes and plan live in their own folder.
type Paths struct {
	Site         string // site/: the server's own folder
	Root         string // the repo root (parent of site/)
	Guides       string // docs/: the app's guides, the same for everyone
	DocTemplates string // docs/templates/: starter notes copied into a new user's notes
	Web          string // site/web/: the page
	ServerData   string // site/data/: accounts, sessions and every user's folder
	Docs         string // the user's notes
	Data         string // the user's data: history, weigh-ins, caches, chat
	PlanFile     string // the user's plan.json
}

func NewPaths(siteDir string) Paths {
	root := filepath.Dir(siteDir)
	docs := filepath.Join(root, "docs")
	data := filepath.Join(siteDir, "data")
	return Paths{
		Site:         siteDir,
		Root:         root,
		Guides:       docs,
		DocTemplates: filepath.Join(docs, "templates"),
		Web:          filepath.Join(siteDir, "web"),
		ServerData:   data,
		Docs:         docs,
		Data:         data,
		PlanFile:     filepath.Join(siteDir, "plan.json"),
	}
}

// UsersDir holds one folder per user (site/data/users/<id>/).
func (p Paths) UsersDir() string { return filepath.Join(p.ServerData, "users") }

// ForUser is the paths for the user whose folder is dir: their data, notes (dir/docs) and plan.
func (p Paths) ForUser(dir string) Paths {
	p.Data, p.Docs, p.PlanFile = dir, filepath.Join(dir, "docs"), filepath.Join(dir, "plan.json")
	return p
}

func (p Paths) Plan() string                { return p.PlanFile }
func (p Paths) PlanExample() string         { return filepath.Join(p.Site, "plan.example.json") }
func (p Paths) DataFile(name string) string { return filepath.Join(p.Data, name) }

// MakePrivate makes the personal files readable by their owner only: files made before the
// dashboard had an account were readable by every user on the computer.
func (p Paths) MakePrivate() {
	os.MkdirAll(p.ServerData, 0o700)
	os.Chmod(p.ServerData, 0o700)
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

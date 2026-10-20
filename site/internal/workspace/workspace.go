// Package workspace is each user's own corner of the dashboard: their folder on disk and the
// services that read and change it. Pages get the signed-in user's workspace and nothing else,
// so one user's requests can never touch another user's plan, notes, tasks, food or chat.
//
// A user's folder is site/data/users/<id>/:
//
//	plan.json, checklist.json, weights.json, mfp.json, chat.json, anki_history.json
//	docs/          their notes
//	taskrc, task/  their Taskwarrior; timewarrior/ their Timewarrior
//	mfp/           their MyFitnessPal login (the MCP's config and browser profile)
//	assistant/     config files for the AI agent's runs
//
// The host account (the server's owner) keeps this computer's own Taskwarrior, Timewarrior,
// MyFitnessPal login and Anki, so the terminal tools keep showing the same tasks.
package workspace

import (
	"os"
	"path/filepath"
	"sync"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/store"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

// Workspace is one user's services, all working on that user's own files.
type Workspace struct {
	UserID        string
	Host          bool
	Paths         config.Paths
	Store         *store.Store
	Tasks         *tasks.Service
	Time          timewarrior.Client
	MyFitnessPal  *myfitnesspal.Syncer
	Anki          anki.Client
	Conversations *assistant.Conversations
	Chat          *assistant.Runner
	Live          *assistant.LiveAnswer

	stop chan struct{}
}

// AssistantDir is where the agent's config files for this user are written.
func (w *Workspace) AssistantDir() string { return w.Paths.DataFile("assistant") }

// Manager opens each user's workspace once and keeps it.
type Manager struct {
	paths     config.Paths        // the server's
	mfp       myfitnesspal.Client // where the MCP comes from; each user gets their own login folder
	hostAnki  anki.Client         // the Anki on this computer, for the host account
	gateway   *anki.Gateway
	providers assistant.Providers

	mu   sync.Mutex
	open map[string]*Workspace
}

func NewManager(paths config.Paths, mfp myfitnesspal.Client, hostAnki anki.Client, gateway *anki.Gateway, providers assistant.Providers) *Manager {
	os.MkdirAll(paths.UsersDir(), 0o700)
	return &Manager{paths: paths, mfp: mfp, hostAnki: hostAnki, gateway: gateway, providers: providers, open: map[string]*Workspace{}}
}

// For is the user's workspace, opened (and its MyFitnessPal sync started) on first use.
// It panics without a user: only pages behind auth.Guard ask, and handing out some shared
// workspace instead would mix people's data.
func (m *Manager) For(user auth.User) *Workspace {
	if !validID(user.ID) {
		panic("workspace: no signed-in user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ws, ok := m.open[user.ID]; ok {
		return ws
	}
	ws := m.openWorkspace(user)
	m.open[user.ID] = ws
	go ws.MyFitnessPal.RunUntil(ws.stop)
	return ws
}

// OpenAll opens every user's workspace, so each one's MyFitnessPal keeps syncing.
func (m *Manager) OpenAll(users []auth.User) {
	for _, user := range users {
		if !user.Disabled {
			m.For(user)
		}
	}
}

// Remove stops the user's background work and deletes their folder: everything they had.
func (m *Manager) Remove(userID string) error {
	m.mu.Lock()
	ws, ok := m.open[userID]
	delete(m.open, userID)
	m.mu.Unlock()
	if ok {
		close(ws.stop)
		ws.Chat.Stop()
	}
	m.gateway.Unregister(userID)
	if !validID(userID) {
		return nil
	}
	return os.RemoveAll(filepath.Join(m.paths.UsersDir(), userID))
}

// SetAnki points the user's Anki at their AnkiConnect.
func (m *Manager) SetAnki(user auth.User, client anki.Client) {
	ws := m.For(user)
	m.mu.Lock()
	defer m.mu.Unlock()
	client.Gateway, client.GatewayKey = m.gateway, m.gateway.Register(user.ID, client)
	ws.Anki = client
}

// validID guards RemoveAll: user ids are hex (auth.newUserID), never a path.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (m *Manager) openWorkspace(user auth.User) *Workspace {
	dir := filepath.Join(m.paths.UsersDir(), user.ID)
	os.MkdirAll(dir, 0o700)
	paths := m.paths.ForUser(dir)
	if user.Host {
		migrateHostData(m.paths, paths)
	}
	files := store.New(paths)
	files.SeedDocs()

	ws := &Workspace{UserID: user.ID, Host: user.Host, Paths: paths, Store: files, stop: make(chan struct{})}
	mfp := m.mfp
	var taskClient *taskwarrior.Client
	if user.Host {
		taskClient, ws.Time = taskwarrior.NewClient(), timewarrior.NewClient()
		ws.Anki = m.hostAnki
	} else {
		env := prepareTaskwarrior(dir, filepath.Join(m.paths.Site, ".timew-hook"))
		taskClient, ws.Time = taskwarrior.NewClientWithEnv(env), timewarrior.NewClientWithEnv(env)
		mfp = mfp.ForUser(filepath.Join(dir, "mfp"))
		ws.Anki = loadAnki(dir)
	}
	ws.Anki.Gateway, ws.Anki.GatewayKey = m.gateway, m.gateway.Register(user.ID, ws.Anki)
	ws.Tasks = tasks.NewService(taskClient, ws.Time)
	ws.MyFitnessPal = myfitnesspal.NewSyncer(mfp, files, paths.DataFile("mfp.json"))
	ws.Conversations = assistant.NewConversations(paths.DataFile("chat.json"), m.providers)
	ws.Chat = assistant.NewRunner()
	ws.Live = assistant.NewLiveAnswer()
	return ws
}

// prepareTaskwarrior makes the user's own taskrc, task data and Timewarrior database, with the
// dashboard's Taskwarrior→Timewarrior hook, and returns the environment that points at them.
func prepareTaskwarrior(dir, hook string) []string {
	taskData := filepath.Join(dir, "task")
	hooks := filepath.Join(dir, "task-hooks")
	timewDB := filepath.Join(dir, "timewarrior")
	taskrc := filepath.Join(dir, "taskrc")
	for _, d := range []string{taskData, hooks, filepath.Join(timewDB, "data")} {
		os.MkdirAll(d, 0o700)
	}
	if _, err := os.Stat(taskrc); os.IsNotExist(err) {
		os.WriteFile(taskrc, []byte("# Made by the dashboard for this account.\n"+
			"data.location="+taskData+"\nhooks.location="+hooks+"\nhooks=on\n"), 0o600)
	}
	if cfg := filepath.Join(timewDB, "timewarrior.cfg"); !exists(cfg) {
		os.WriteFile(cfg, nil, 0o600)
	}
	if link := filepath.Join(hooks, "on-modify.timewarrior"); exists(hook) && !exists(link) {
		os.Symlink(hook, link)
	}
	return []string{"TASKRC=" + taskrc, "TASKDATA=" + taskData, "TIMEWARRIORDB=" + timewDB}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Package workspace is each user's own corner of the dashboard: their folder on disk and the
// services that read and change it. Pages get the signed-in user's workspace and nothing else,
// so one user's requests can never touch another user's plan, notes, tasks, food or chat.
//
// A user's folder holds:
//
//	plan.json, checklist.json, weights.json, lifting.json, mfp.json, chat.json, anki_history.json
//	docs/          their notes
//	taskrc, task/  their Taskwarrior; timewarrior/ their Timewarrior
//	mfp/           their MyFitnessPal login (the MCP's config and browser profile)
//	agent-home/    the home of the sandbox the AI's MCP tools run in
//
// It's encrypted on disk (site/data/vaults/<id>/, see vault.go): while the user is signed in it's
// decrypted into memory and used from there. The host account (the server's owner) keeps this
// computer's own Taskwarrior, Timewarrior, MyFitnessPal login and Anki, so the terminal tools
// keep showing the same tasks: those stay where they are, outside the encrypted folder.
package workspace

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/jsonfile"
	"dashboard/internal/lifting"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
	"dashboard/internal/secrets"
	"dashboard/internal/store"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
	"dashboard/internal/vault"
)

// Workspace is one user's services, all working on that user's own files.
type Workspace struct {
	UserID        string
	Host          bool
	Paths         config.Paths
	Store         *store.Store
	Lifting       *lifting.Store // routines and workouts
	Tasks         *tasks.Service
	Time          timewarrior.Client
	MyFitnessPal  *myfitnesspal.Syncer
	Secrets       *secrets.Store           // the user's AI API keys, sealed
	Conversations *assistant.Conversations // the AI's settings (it runs in the browser)

	mu   sync.Mutex
	anki anki.Client   // changes when the user connects their Anki: read it with Anki()
	stop chan struct{} // closed to stop the background sync
	done chan struct{} // closed once it has stopped
}

// Anki is the user's Anki: this computer's for the host, otherwise their own AnkiConnect.
func (w *Workspace) Anki() anki.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.anki
}

func (w *Workspace) setAnki(client anki.Client) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.anki = client
}

// close ends the workspace's background work: its sync.
func (w *Workspace) close() {
	close(w.stop)
	<-w.done // a sync still writing would leave files behind
}

// Manager opens each user's workspace once and keeps it.
type Manager struct {
	paths    config.Paths        // the server's
	mfp      myfitnesspal.Client // where the MCP comes from; each user gets their own login folder
	hostAnki anki.Client         // the Anki on this computer, for the host account
	gateway  *anki.Gateway
	box      *secrets.Box

	mu       sync.Mutex
	open     map[string]*Workspace
	unlocked map[string]*unlocked // encrypted users whose data is open, by id
	memory   string               // where unlocked folders live (in RAM); "" when there's nowhere safe
	memErr   error

	// Credentials names a user's live sessions and token (auth.Auth.Credentials), so key copies
	// for ended ones are dropped and a user with none left is locked.
	Credentials func(userID string) []string
	// LockAfter locks an encrypted user's data after this long without a request.
	LockAfter time.Duration
	// OnLock is told when a user's data locks (their tools, which use it, stop).
	OnLock func(userID string)
}

func NewManager(paths config.Paths, mfp myfitnesspal.Client, hostAnki anki.Client, gateway *anki.Gateway,
	box *secrets.Box) *Manager {
	os.MkdirAll(paths.UsersDir(), 0o700)
	memory, memErr := vault.MemoryDir()
	return &Manager{paths: paths, mfp: mfp, hostAnki: hostAnki, gateway: gateway, box: box,
		open: map[string]*Workspace{}, unlocked: map[string]*unlocked{}, memory: memory, memErr: memErr,
		Credentials: func(string) []string { return nil }, LockAfter: 30 * time.Minute}
}

// For is the user's workspace, opened (and its MyFitnessPal sync started) on first use.
// It panics without a user: only pages behind auth.Guard ask, and handing out some shared
// workspace instead would mix people's data. An encrypted user's data must have been unlocked
// first (Unlock, which every request goes through); asking for a locked one panics too.
func (m *Manager) For(user auth.User) *Workspace {
	if !validID(user.ID) {
		panic("workspace: no signed-in user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ws, ok := m.open[user.ID]; ok {
		if u := m.unlocked[user.ID]; u != nil {
			u.used = time.Now()
		}
		return ws
	}
	if m.encrypted(user) {
		panic("workspace: " + user.ID + "'s data is locked")
	}
	return m.start(user, filepath.Join(m.paths.UsersDir(), user.ID))
}

// start opens the user's workspace on their folder dir and starts its sync. It needs m.mu held.
func (m *Manager) start(user auth.User, dir string) *Workspace {
	ws := m.openWorkspace(user, dir)
	m.open[user.ID] = ws
	go func() {
		defer close(ws.done)
		ws.MyFitnessPal.RunUntil(ws.stop)
	}()
	return ws
}

// Gateway is the Anki gateway every user's agent reaches their Anki through.
func (m *Manager) Gateway() *anki.Gateway { return m.gateway }

// OpenAll opens the workspaces that aren't encrypted, so each one's MyFitnessPal keeps syncing.
// Encrypted ones open when their user signs in or comes back.
func (m *Manager) OpenAll(users []auth.User) {
	for _, user := range users {
		if !user.Disabled && !m.encrypted(user) {
			m.For(user)
		}
	}
}

// Remove stops the user's background work and deletes their data: their folder, their
// encrypted copy and its keys, and the unlocked copy in memory. Everything they had.
func (m *Manager) Remove(userID string) error {
	if !validID(userID) {
		return nil
	}
	m.mu.Lock()
	ws, ok := m.open[userID]
	delete(m.open, userID)
	delete(m.unlocked, userID)
	m.mu.Unlock()
	if ok {
		ws.close()
	}
	m.gateway.Unregister(userID)
	if m.memory != "" {
		os.Remove(m.unlockedMarker(userID))
		vault.Wipe(m.memoryDir(userID))
	}
	if err := os.RemoveAll(m.vaultDir(userID)); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.paths.UsersDir(), userID))
}

// ConnectAnki checks the user's AnkiConnect (see anki.CheckConnection), saves it and points
// their Anki and their agent's Anki MCP at it. The host keeps this computer's Anki.
func (m *Manager) ConnectAnki(user auth.User, url, apiKey string) error {
	cleaned, err := anki.CheckConnection(url, apiKey, false)
	if err != nil {
		return err
	}
	ws := m.For(user)
	if err := jsonfile.Write(filepath.Join(filepath.Dir(ws.Paths.Plan()), "anki.json"), ankiSettings{URL: cleaned, APIKey: apiKey}); err != nil {
		return err
	}
	m.useAnki(ws, anki.NewClient(cleaned, apiKey))
	return nil
}

// DisconnectAnki forgets the user's AnkiConnect.
func (m *Manager) DisconnectAnki(user auth.User) error {
	ws := m.For(user)
	if err := os.Remove(filepath.Join(filepath.Dir(ws.Paths.Plan()), "anki.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.useAnki(ws, anki.NewClient("", ""))
	return nil
}

func (m *Manager) useAnki(ws *Workspace, client anki.Client) {
	client.Gateway, client.GatewayKey = m.gateway, m.gateway.Register(ws.UserID, client)
	ws.setAnki(client)
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

// openWorkspace makes the user's services, working on their folder dir.
func (m *Manager) openWorkspace(user auth.User, dir string) *Workspace {
	os.MkdirAll(dir, 0o700)
	paths := m.paths.ForUser(dir)
	if user.Host {
		migrateHostData(m.paths, paths)
	}
	files := store.New(paths)
	files.SeedDocs()

	ws := &Workspace{UserID: user.ID, Host: user.Host, Paths: paths, Store: files,
		stop: make(chan struct{}), done: make(chan struct{})}
	mfp := m.mfp
	var taskClient *taskwarrior.Client
	ankiClient := m.hostAnki
	if user.Host {
		taskClient, ws.Time = taskwarrior.NewClient(), timewarrior.NewClient()
		if saved := loadAnki(dir); saved.URL != "" {
			ankiClient = saved
		}
	} else {
		env := prepareTaskwarrior(dir, filepath.Join(m.paths.Site, ".timew-hook"))
		taskClient, ws.Time = taskwarrior.NewClientWithEnv(env), timewarrior.NewClientWithEnv(env)
		mfp = mfp.ForUser(filepath.Join(dir, "mfp"))
		ankiClient = loadAnki(dir)
	}
	m.useAnki(ws, ankiClient)
	ws.Secrets = secrets.NewStore(m.box, dir, user.ID)
	ws.Tasks = tasks.NewService(taskClient, ws.Time)
	ws.Lifting = lifting.New(paths.DataFile("lifting.json"))
	ws.MyFitnessPal = myfitnesspal.NewSyncer(mfp, files, paths.DataFile("mfp.json"))
	ws.Conversations = assistant.NewConversations(paths.DataFile("chat.json"))
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
	// Written every time: its paths follow the folder, which is in memory while unlocked.
	os.WriteFile(taskrc, []byte("# Made by the dashboard for this account.\n"+
		"data.location="+taskData+"\nhooks.location="+hooks+"\nhooks=on\n"), 0o600)
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

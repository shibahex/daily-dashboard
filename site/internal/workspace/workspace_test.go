package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"dashboard/internal/anki"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/secrets"
	"dashboard/internal/tasks"
)

// newManager is a Manager over a throwaway repo: site/ with a plan example, docs/ with a guide
// and a template note.
func newManager(t *testing.T) (*Manager, config.Paths) {
	t.Helper()
	root := t.TempDir()
	site := filepath.Join(root, "site")
	os.MkdirAll(filepath.Join(site, "data"), 0o700)
	os.MkdirAll(filepath.Join(root, "docs", "templates"), 0o700)
	os.WriteFile(filepath.Join(site, "plan.example.json"), []byte(`{"checklist": ["Weigh in"]}`), 0o600)
	os.WriteFile(filepath.Join(root, "docs", "README.md"), []byte("guide"), 0o600)
	os.WriteFile(filepath.Join(root, "docs", "templates", "overview.md"), []byte("template"), 0o600)
	paths := config.NewPaths(site)
	// The host account copies the server computer's own Taskwarrior and logins from here.
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0o700)
	t.Setenv("HOME", home)
	t.Setenv("TASKRC", filepath.Join(home, ".taskrc"))
	os.WriteFile(filepath.Join(home, ".taskrc"), []byte("data.location="+filepath.Join(home, ".task")+"\n"), 0o600)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "TIMEWARRIORDB", "TASKDATA"} {
		t.Setenv(name, "") // restored after the test
		os.Unsetenv(name)  // an empty TASKDATA is the current folder to Taskwarrior
	}
	gateway, err := anki.StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadBox(filepath.Join(paths.ServerData, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(paths, myfitnesspal.Client{}, gateway, box)
	t.Cleanup(func() {
		for _, id := range []string{"aaaa", "bbbb", "cccc"} {
			m.Remove(id)
		}
	})
	return m, paths
}

func user(id string) auth.User { return auth.User{ID: id, Username: "user-" + id} }

func TestUsersDontShareData(t *testing.T) {
	m, _ := newManager(t)
	a, b := m.For(user("aaaa")), m.For(user("bbbb"))
	if m.For(user("aaaa")) != a {
		t.Fatal("a user should keep one workspace")
	}

	lb := 180.0
	if err := a.Store.SetWeight("2026-10-20", &lb); err != nil {
		t.Fatal(err)
	}
	if len(b.Store.Weights()) != 0 || len(a.Store.Weights()) != 1 {
		t.Fatal("weigh-ins leaked between users")
	}

	if err := a.Store.SaveChecklistDay("2026-10-20", map[string]any{"Weigh in": "done"}); err != nil {
		t.Fatal(err)
	}
	if len(b.Store.Checklist()) != 0 {
		t.Fatal("checklist history leaked between users")
	}

	os.WriteFile(filepath.Join(a.Paths.Docs, "secret-plan.md"), []byte("a's note"), 0o600)
	if slices.Contains(b.Store.DocNames(), "secret-plan.md") {
		t.Fatal("notes leaked between users")
	}
	if _, found := b.Store.Doc("secret-plan.md"); found {
		t.Fatal("one user could open another's note")
	}
	for _, ws := range []*Workspace{a, b} {
		names := ws.Store.DocNames()
		if !slices.Contains(names, "README.md") || !slices.Contains(names, "overview.md") {
			t.Fatalf("everyone gets the guides and the starter notes: %v", names)
		}
	}

	a.Secrets.Set("anthropic", "a's key")
	if b.Secrets.Has("anthropic") {
		t.Fatal("API keys leaked between users")
	}
	if a.Paths.DataFile("chat.json") == b.Paths.DataFile("chat.json") {
		t.Fatal("chats and agent folders must differ")
	}
	if a.Anki().GatewayKey == "" || a.Anki().GatewayKey == b.Anki().GatewayKey {
		t.Fatal("each user needs their own Anki gateway key")
	}
	if info, err := os.Stat(filepath.Dir(a.Paths.Plan())); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("a user's folder should be owner-only")
	}
}

func TestUsersHaveTheirOwnTasks(t *testing.T) {
	if _, err := exec.LookPath("task"); err != nil {
		t.Skip("Taskwarrior isn't installed")
	}
	m, _ := newManager(t)
	a, b := m.For(user("aaaa")), m.For(user("bbbb"))
	if err := a.Tasks.Add(tasks.NewTask{Description: "a's task"}); err != nil {
		t.Fatal(err)
	}
	aTasks, err := a.Tasks.Pending()
	if err != nil || len(aTasks) != 1 {
		t.Fatalf("a should see their task: %v %v", aTasks, err)
	}
	if bTasks, _ := b.Tasks.Pending(); len(bTasks) != 0 {
		t.Fatalf("b sees a's tasks: %v", bTasks)
	}
	if _, err := os.Stat(filepath.Join(a.Paths.Data, "task")); err != nil {
		t.Fatal("tasks should live in the user's folder")
	}
}

func TestTheHostGetsTheOldDataOnce(t *testing.T) {
	m, paths := newManager(t)
	os.WriteFile(paths.Plan(), []byte(`{"checklist": ["Old item"]}`), 0o600)
	os.WriteFile(filepath.Join(paths.ServerData, "weights.json"), []byte(`[{"date": "2026-10-01", "lb": 230}]`), 0o600)
	os.WriteFile(filepath.Join(paths.Guides, "background.md"), []byte("my story"), 0o600)

	host := user("cccc")
	host.Host = true
	ws := m.For(host)
	if got := ws.Store.PlanChecklist(); !slices.Equal(got, []string{"Old item"}) {
		t.Fatalf("the plan should carry over: %v", got)
	}
	if len(ws.Store.Weights()) != 1 {
		t.Fatal("the weigh-ins should carry over")
	}
	if content, found := ws.Store.Doc("background.md"); !found || string(content) != "my story" {
		t.Fatal("the notes should carry over")
	}
	if _, err := os.Stat(paths.Plan()); err != nil {
		t.Fatal("the originals stay as a backup")
	}
	os.Remove(ws.Paths.DataFile("weights.json"))
	m.Remove("cccc")
	if again := m.For(host); len(again.Store.Weights()) != 1 {
		t.Fatal("a fresh folder (the old one deleted) should migrate again")
	}

	other := m.For(user("aaaa"))
	if slices.Contains(other.Store.DocNames(), "background.md") || len(other.Store.Weights()) != 0 {
		t.Fatal("other users must not get the host's old data")
	}
}

func TestTheHostMigratesNotesWithoutAPlan(t *testing.T) {
	m, paths := newManager(t)
	os.WriteFile(filepath.Join(paths.Guides, "background.md"), []byte("my story"), 0o600)
	host := user("cccc")
	host.Host = true
	if _, found := m.For(host).Store.Doc("background.md"); !found {
		t.Fatal("notes should carry over even when there was no plan.json")
	}
}

func TestTheHostGetsTheServersServicesOnce(t *testing.T) {
	m, _ := newManager(t)
	home := os.Getenv("HOME")
	write := func(path, content string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(content), 0o600)
	}
	write(filepath.Join(home, ".task", "taskchampion.sqlite3"), "tasks")
	write(filepath.Join(home, ".task", "hooks", "on-modify.other"), "hook")
	write(filepath.Join(home, ".local", "share", "timewarrior", "data", "2026-10.data"), "inc 20261001T090000Z")
	write(filepath.Join(home, ".config", "myfitnesspal-mcp", "cookies.json"), "{}")

	host := user("cccc")
	host.Host = true
	ws := m.For(host)
	dir := filepath.Dir(ws.Paths.Plan())
	for _, path := range []string{"task/taskchampion.sqlite3", "timewarrior/data/2026-10.data", "mfp/config/myfitnesspal-mcp/cookies.json"} {
		if !exists(filepath.Join(dir, path)) {
			t.Errorf("%s should have been copied into the host's folder", path)
		}
	}
	if exists(filepath.Join(dir, "task", "hooks")) {
		t.Error("the server's Taskwarrior hooks shouldn't come along")
	}
	if !ws.MyFitnessPal.Client().Setup().SignedIn {
		t.Error("the host should keep its MyFitnessPal login")
	}

	other := m.For(user("aaaa"))
	otherDir := filepath.Dir(other.Paths.Plan())
	if exists(filepath.Join(otherDir, "task", "taskchampion.sqlite3")) ||
		other.MyFitnessPal.Client().Setup().SignedIn {
		t.Fatal("other users must not get the server's tasks or logins")
	}
}

func TestRemoveDeletesEverything(t *testing.T) {
	m, _ := newManager(t)
	ws := m.For(user("aaaa"))
	key := ws.Anki().GatewayKey
	dir := filepath.Dir(ws.Paths.Plan())
	if err := m.Remove("aaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the user's folder should be gone")
	}
	if m.gateway.Knows(key) {
		t.Fatal("the user's Anki key should stop working")
	}
	if err := m.Remove("../../etc"); err != nil {
		t.Fatal(err)
	}
}

func TestNoUserNoWorkspace(t *testing.T) {
	m, _ := newManager(t)
	defer func() {
		if recover() == nil {
			t.Fatal("asking for a workspace without a user should fail loudly")
		}
	}()
	m.For(auth.User{})
}

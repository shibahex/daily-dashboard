package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dashboard/internal/auth"
)

// encryptedManager is newManager with its in-memory folder in a temp dir and the live
// credentials under the test's control.
func encryptedManager(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	m, _ := newManager(t)
	m.memory = t.TempDir()
	live := []string{}
	m.Credentials = func(string) []string { return live }
	return m, &live
}

// noLeak fails when any file under dir holds text.
func noLeak(t *testing.T, dir, text string) {
	t.Helper()
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); bytes.Contains(data, []byte(text)) {
				t.Errorf("%q readable in %s", text, path)
			}
		}
		return nil
	})
}

func locked(m *Manager, u auth.User) (isLocked bool) {
	defer func() { isLocked = recover() != nil }()
	m.For(u)
	return false
}

func TestEncryptionLifecycle(t *testing.T) {
	m, live := encryptedManager(t)
	u := user("aaaa")

	// A plain folder from before: usable through a token, but a session must sign in again.
	ws := m.For(u)
	if _, err := ws.Store.CreateNote("Secret diary"); err != nil {
		t.Fatal(err)
	}
	if err := m.Unlock(u, auth.BySession, "old-session"); !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("a plain folder's session: %v", err)
	}

	// Signing in with the password encrypts it, and the plain folder is gone.
	created, err := m.SignIn(u, "pw", "rc-1", "session-1")
	if err != nil || !created {
		t.Fatalf("SignIn = %v, %v", created, err)
	}
	if exists(m.plainDir(u.ID)) {
		t.Error("the plain folder is still there")
	}
	ws = m.For(u)
	if !ws.Store.IsNote("secret-diary.md") || filepath.Dir(ws.Paths.Plan()) != m.memoryDir(u.ID) {
		t.Fatalf("not open from memory with its data: %s", ws.Paths.Plan())
	}
	noLeak(t, m.vaultDir(u.ID), "Secret diary")

	// A change, then locking: saved, and gone from memory.
	if _, err := ws.Store.CreateNote("Gym log"); err != nil {
		t.Fatal(err)
	}
	if err := m.Lock(u.ID); err != nil {
		t.Fatal(err)
	}
	if exists(m.memoryDir(u.ID)) || !locked(m, u) {
		t.Fatal("still open after locking")
	}
	noLeak(t, m.vaultDir(u.ID), "Gym log")

	// The session's own key copy opens it again (as after a restart); another doesn't.
	if err := m.Unlock(u, auth.BySession, "someone-else"); !errors.Is(err, ErrSignInAgain) {
		t.Errorf("an unknown session: %v", err)
	}
	if err := m.Unlock(u, auth.BySession, "session-1"); err != nil {
		t.Fatal(err)
	}
	if !m.For(u).Store.IsNote("gym-log.md") {
		t.Error("the change was lost")
	}

	// Pruning drops ended sessions' copies and, with nothing left, locks.
	*live = nil
	m.Prune(u.ID)
	if !locked(m, u) {
		t.Error("not locked with no session left")
	}
	if err := m.Unlock(u, auth.BySession, "session-1"); !errors.Is(err, ErrSignInAgain) {
		t.Error("an ended session still opens the data")
	}

	// A reset with the recovery code keeps the data under the new password.
	if _, err := m.Reset(u, "pw2", "session-2", "rc-1", "unused"); err != nil {
		t.Fatal(err)
	}
	if !m.For(u).Store.IsNote("gym-log.md") {
		t.Error("the reset lost the data")
	}
	m.Lock(u.ID)
	if _, err := m.SignIn(u, "pw", "", "session-3"); err == nil {
		t.Error("the old password still opens the data")
	}
	if _, err := m.SignIn(u, "pw2", "", "session-3"); err != nil {
		t.Fatal(err)
	}

	// A reset without it starts over: the old data can't be opened, so it's gone.
	startedOver, err := m.Reset(u, "pw3", "session-4", "", "rc-2")
	if err != nil || !startedOver {
		t.Fatalf("reset without a code = %v, %v", startedOver, err)
	}
	if m.For(u).Store.IsNote("gym-log.md") {
		t.Error("data survived a reset without the recovery code")
	}
	if err := m.CheckRecovery(u, "rc-1"); err == nil {
		t.Error("the old recovery code still matches")
	}
}

// The host's folder is encrypted like everyone's.
func TestHostIsEncrypted(t *testing.T) {
	m, _ := encryptedManager(t)
	host := auth.User{ID: "cccc", Username: "host", Host: true}
	m.For(host).Store.CreateNote("Host diary")
	if err := m.Unlock(host, auth.BySession, "old-session"); !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("a plain host folder's session should sign in again: %v", err)
	}
	if created, err := m.SignIn(host, "pw", "rc", "s"); err != nil || !created {
		t.Fatalf("SignIn = %v, %v", created, err)
	}
	if !m.Encrypted(host) || exists(m.plainDir(host.ID)) {
		t.Error("the host's folder should be encrypted, and the plain one gone")
	}
	if ws := m.For(host); filepath.Dir(ws.Paths.Plan()) != m.memoryDir(host.ID) || !ws.Store.IsNote("host-diary.md") {
		t.Error("the host should work from memory, with its notes")
	}
	noLeak(t, m.vaultDir(host.ID), "Host diary")
}

// The plugin's token keeps its own key copy, so the bar plugin opens the data after a restart.
func TestTokenUnlocks(t *testing.T) {
	m, _ := encryptedManager(t)
	u := user("bbbb")
	if _, err := m.SignIn(u, "pw", "rc", "session"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetToken(u, "dd_token-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetToken(u, "dd_token-2"); err != nil { // a new token replaces the old one
		t.Fatal(err)
	}
	m.Lock(u.ID)
	if err := m.Unlock(u, auth.ByToken, "dd_token-1"); err == nil {
		t.Error("the replaced token still opens the data")
	}
	if err := m.Unlock(u, auth.ByToken, "dd_token-2"); err != nil {
		t.Fatal(err)
	}
	if locked(m, u) {
		t.Error("not open after the token unlocked it")
	}
}

package workspace

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dashboard/internal/auth"
	"dashboard/internal/problem"
	"dashboard/internal/vault"
)

// Every user's data is encrypted on disk, the host's too, in site/data/vaults/<id>/: their
// keyring (keys.json) and the encrypted copy of their folder (data/). It's unlocked by their
// password at sign-in, and after that by the key copy kept for that browser's session (or the
// plugin's token), so a restarted server unlocks it again at their next request without asking.
// Unlocked, the folder is decrypted into memory (vault.MemoryDir) and saved back every few
// seconds; it's locked again (saved, wiped from memory) when they log out, after LockAfter
// without a request, and when the server stops.
//
// A user who's still in a plain folder from before is encrypted at their next sign-in with their
// password. Until then their requests ask them to sign in again.

// ErrSignInAgain is Unlock's answer when the credential can't open the data: a session from
// before the data was encrypted, or a plain folder waiting for its password.
var ErrSignInAgain error = problem.Error{Kind: problem.Forbidden, Message: "Sign in again to unlock your data"}

// saveEvery is how often unlocked folders are saved back to disk.
const saveEvery = 5 * time.Second

// unlocked is an encrypted user's open data.
type unlocked struct {
	key    *vault.Key
	mirror *vault.Mirror
	used   time.Time // the last request
}

func (m *Manager) vaultDir(id string) string       { return filepath.Join(m.paths.ServerData, "vaults", id) }
func (m *Manager) keys(id string) *vault.Keys      { return vault.NewKeys(m.vaultDir(id), id) }
func (m *Manager) memoryDir(id string) string      { return filepath.Join(m.memory, id) }
func (m *Manager) unlockedMarker(id string) string { return filepath.Join(m.memory, id+".unlocked") }
func (m *Manager) plainDir(id string) string       { return filepath.Join(m.paths.UsersDir(), id) }
func (m *Manager) encrypted(user auth.User) bool   { return m.keys(user.ID).Exists() }
func (m *Manager) mirror(id string, key *vault.Key) *vault.Mirror {
	return vault.NewMirror(filepath.Join(m.vaultDir(id), "data"), m.memoryDir(id), key)
}

// slotName is where a session's or token's key copy is kept: under its fingerprint.
func slotName(kind, secret string) string { return kind + ":" + auth.Fingerprint(secret) }

// Encrypted reports whether the user's data is encrypted on disk, for the admin page.
func (m *Manager) Encrypted(user auth.User) bool { return m.encrypted(user) }

// Unlock makes sure the request's user can use their data: anyone still in a plain folder (only
// through the plugin's token) needs nothing; an encrypted user's data is decrypted with the key
// copy for this session or token. kind and secret are auth.CredentialFrom's.
func (m *Manager) Unlock(user auth.User, kind, secret string) error {
	if !m.encrypted(user) {
		if kind == auth.ByToken || user.PlainStorage {
			return nil // a plain folder: chosen, or encrypted when they next sign in with their password
		}
		return ErrSignInAgain
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.unlocked[user.ID]; u != nil {
		u.used = time.Now()
		return nil
	}
	key, err := m.keys(user.ID).Open(kind, slotName(kind, secret), secret)
	if err != nil {
		return ErrSignInAgain
	}
	return m.unlock(user, key)
}

// unlock decrypts the user's data into memory and opens their workspace on it. It needs m.mu.
func (m *Manager) unlock(user auth.User, key *vault.Key) error {
	if m.memory == "" {
		return problem.Unavailablef("The server has nowhere private in memory to open your data: " + m.memErr.Error())
	}
	mirror := m.mirror(user.ID, key)
	dir := m.memoryDir(user.ID)
	if exists(m.unlockedMarker(user.ID)) && exists(dir) {
		// Left open by a server that stopped without saving: it's newer than the saved copy.
		if err := mirror.Load(); err != nil {
			return err
		}
	} else {
		vault.Wipe(dir)
		if err := mirror.Restore(); err != nil {
			vault.Wipe(dir)
			return err
		}
	}
	if err := os.WriteFile(m.unlockedMarker(user.ID), nil, 0o600); err != nil {
		return err
	}
	m.unlocked[user.ID] = &unlocked{key: key, mirror: mirror, used: time.Now()}
	m.start(user, dir)
	return nil
}

// SignIn is called after a sign-in (or a new account from an invite) with the password's proof.
// It encrypts the user's data if it isn't yet, keeps a key copy for the new session, and unlocks.
// recovery is the proof of a recovery code the browser made in case this is the first time;
// created reports that it was used, so the browser shows that code.
func (m *Manager) SignIn(user auth.User, password, recovery, session string) (created bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.keys(user.ID)
	if user.PlainStorage && !keys.Exists() {
		return false, nil // they chose to keep it unencrypted on disk
	}
	var key *vault.Key
	if keys.Exists() {
		if key, err = keys.Open(vault.Password, "", password); err != nil {
			return false, problem.Invalidf("Your data couldn't be unlocked with this password. If an admin reset it, use your recovery code on the reset link.")
		}
	} else {
		if key, err = m.encrypt(user, keys, password, recovery); err != nil {
			return false, err
		}
		created = true
	}
	if err := keys.Add(key, auth.BySession, slotName(auth.BySession, session), session); err != nil {
		return false, err
	}
	if m.unlocked[user.ID] == nil {
		if err := m.unlock(user, key); err != nil {
			return false, err
		}
	}
	return created, nil
}

// encrypt makes the user's keyring and encrypts their plain folder (if they have one) into it,
// then deletes the plain one. It needs m.mu.
func (m *Manager) encrypt(user auth.User, keys *vault.Keys, password, recovery string) (*vault.Key, error) {
	if m.memory == "" {
		return nil, problem.Unavailablef("The server can't encrypt your data: " + m.memErr.Error())
	}
	if recovery == "" {
		return nil, problem.Invalidf("Your browser didn't send a recovery code: reload the page and sign in again")
	}
	plain := m.plainDir(user.ID)
	if ws, ok := m.open[user.ID]; ok {
		delete(m.open, user.ID)
		ws.close()
	}
	os.RemoveAll(filepath.Join(m.vaultDir(user.ID), "data")) // anything half-made before
	key, err := keys.Create(password, recovery)
	if err != nil {
		return nil, err
	}
	if exists(plain) {
		mirror := vault.NewMirror(filepath.Join(m.vaultDir(user.ID), "data"), plain, key)
		if err := mirror.Load(); err == nil {
			err = mirror.Save()
		}
		if err != nil {
			os.RemoveAll(m.vaultDir(user.ID)) // stay plain rather than half-encrypted
			return nil, err
		}
		vault.Wipe(plain)
	}
	return key, nil
}

// CheckRecovery reports whether recovery is the proof of the user's recovery code (before a reset
// with it).
func (m *Manager) CheckRecovery(user auth.User, recovery string) error {
	if _, err := m.keys(user.ID).Open(vault.Recovery, "", recovery); err != nil {
		return problem.Invalidf("That recovery code doesn't match")
	}
	return nil
}

// HasEncryptedData reports whether a reset needs the recovery code to keep the user's data.
func (m *Manager) HasEncryptedData(user auth.User) bool { return m.encrypted(user) }

// Reset follows an admin's password reset, after the new password is set. With oldRecovery (the
// proof of their recovery code) it keeps the data under the new password; without it the data
// can't be opened any more, so it's deleted and the account starts empty with newRecovery as
// its recovery code. Every old session and token copy is dropped. startedOver reports which.
func (m *Manager) Reset(user auth.User, password, session, oldRecovery, newRecovery string) (startedOver bool, err error) {
	m.Lock(user.ID)
	if !m.keys(user.ID).Exists() {
		return m.SignIn(user, password, newRecovery, session)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.keys(user.ID)
	if oldRecovery == "" {
		os.RemoveAll(m.vaultDir(user.ID))
		vault.Wipe(m.memoryDir(user.ID))
		os.Remove(m.unlockedMarker(user.ID))
		key, err := m.encrypt(user, keys, password, newRecovery)
		if err != nil {
			return false, err
		}
		if err := keys.Add(key, auth.BySession, slotName(auth.BySession, session), session); err != nil {
			return false, err
		}
		return true, m.unlock(user, key)
	}
	key, err := keys.Open(vault.Recovery, "", oldRecovery)
	if err != nil {
		return false, problem.Invalidf("That recovery code doesn't match")
	}
	if err := keys.Remove(func(name, kind string) bool { return kind == auth.BySession || kind == auth.ByToken }); err != nil {
		return false, err
	}
	for _, add := range []struct{ kind, name, secret string }{
		{vault.Password, "", password},
		{auth.BySession, slotName(auth.BySession, session), session},
	} {
		if err := keys.Add(key, add.kind, add.name, add.secret); err != nil {
			return false, err
		}
	}
	return false, m.unlock(user, key)
}

// openKey is an unlocked user's data key (to wrap it again), or an error asking to sign in.
func (m *Manager) openKey(user auth.User) (*vault.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.unlocked[user.ID]; u != nil {
		return u.key, nil
	}
	return nil, ErrSignInAgain
}

// ChangePassword wraps the user's data key with their new password (after auth changed it).
func (m *Manager) ChangePassword(user auth.User, password string) error {
	if !m.encrypted(user) {
		return nil
	}
	key, err := m.openKey(user)
	if err != nil {
		return err
	}
	return m.keys(user.ID).Add(key, vault.Password, "", password)
}

// SetRecovery replaces the user's recovery code with a new one (its proof, from the browser).
func (m *Manager) SetRecovery(user auth.User, recovery string) error {
	if !m.encrypted(user) {
		return problem.Invalidf("This account's data isn't encrypted, so it has no recovery code")
	}
	if recovery == "" {
		return problem.Invalidf("no recovery code")
	}
	key, err := m.openKey(user)
	if err != nil {
		return err
	}
	return m.keys(user.ID).Add(key, vault.Recovery, "", recovery)
}

// SetToken keeps a key copy for the user's new plugin token (replacing the old one's).
func (m *Manager) SetToken(user auth.User, token string) error {
	if !m.encrypted(user) {
		return nil
	}
	key, err := m.openKey(user)
	if err != nil {
		return err
	}
	keys := m.keys(user.ID)
	if err := keys.Remove(func(name, kind string) bool { return kind == auth.ByToken }); err != nil {
		return err
	}
	return keys.Add(key, auth.ByToken, slotName(auth.ByToken, token), token)
}

// Prune drops the key copies of the user's ended sessions and old tokens, and locks their data
// when nothing they could come back with is left (they logged out everywhere).
func (m *Manager) Prune(userID string) {
	keys := m.keys(userID)
	if !keys.Exists() {
		return
	}
	live := m.Credentials(userID)
	keys.Remove(func(name, kind string) bool {
		return (kind == auth.BySession || kind == auth.ByToken) && !slices.Contains(live, name)
	})
	if len(live) == 0 {
		m.Lock(userID)
	}
}

// Lock saves the user's data, stops their workspace and wipes the decrypted copy from memory.
func (m *Manager) Lock(userID string) error {
	m.mu.Lock()
	u := m.unlocked[userID]
	ws := m.open[userID]
	if u == nil {
		m.mu.Unlock()
		return nil
	}
	delete(m.unlocked, userID)
	delete(m.open, userID)
	m.mu.Unlock()
	if m.OnLock != nil {
		m.OnLock(userID)
	}
	if ws != nil {
		ws.close()
	}
	m.gateway.Unregister(userID)
	if err := u.mirror.Close(); err != nil {
		// Keep the copy in memory (and its marker), so the next unlock picks it up.
		log.Printf("locking %s: saving failed, keeping the open copy: %v", userID, err)
		return err
	}
	os.Remove(m.unlockedMarker(userID))
	return vault.Wipe(m.memoryDir(userID))
}

// LockAll locks everyone, for a server that's stopping.
func (m *Manager) LockAll() {
	m.mu.Lock()
	var ids []string
	for id := range m.unlocked {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Lock(id)
	}
}

// RunUntil saves unlocked data every few seconds and locks what's been idle for LockAfter (or
// whose user has no session or token left), until stop is closed.
func (m *Manager) RunUntil(stop <-chan struct{}) {
	ticker := time.NewTicker(saveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		var idle []string
		var open []*unlocked
		for id, u := range m.unlocked {
			if time.Since(u.used) > m.LockAfter {
				idle = append(idle, id)
			} else {
				open = append(open, u)
			}
		}
		m.mu.Unlock()
		for _, u := range open {
			if err := u.mirror.Save(); err != nil {
				log.Printf("saving encrypted data: %v", err)
			}
		}
		for _, id := range idle {
			m.Lock(id)
		}
	}
}

// ---------- choosing ----------

// Decrypt moves the user's data out of the encrypted copy into a plain folder on disk, for a
// user who chose not to keep it encrypted there (auth.User.PlainStorage). They must be signed in.
func (m *Manager) Decrypt(user auth.User) error {
	m.mu.Lock()
	u, ws := m.unlocked[user.ID], m.open[user.ID]
	if u == nil {
		m.mu.Unlock()
		return ErrSignInAgain
	}
	delete(m.unlocked, user.ID)
	delete(m.open, user.ID)
	m.mu.Unlock()
	if m.OnLock != nil {
		m.OnLock(user.ID)
	}
	if ws != nil {
		ws.close()
	}
	if err := u.mirror.Close(); err != nil {
		return err
	}
	plain := m.plainDir(user.ID)
	os.RemoveAll(plain)
	if err := copyTree(m.memoryDir(user.ID), plain); err != nil {
		os.RemoveAll(plain) // the encrypted copy is still whole: nothing lost
		return err
	}
	os.RemoveAll(m.vaultDir(user.ID))
	os.Remove(m.unlockedMarker(user.ID))
	return vault.Wipe(m.memoryDir(user.ID))
}

// EncryptNow encrypts the user's plain folder again (they chose to), with their password's
// proof, a new recovery code's proof, and a key copy for their session, and opens it.
func (m *Manager) EncryptNow(user auth.User, password, recovery, session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.keys(user.ID)
	if keys.Exists() {
		return nil
	}
	key, err := m.encrypt(user, keys, password, recovery)
	if err != nil {
		return err
	}
	if err := keys.Add(key, auth.BySession, slotName(auth.BySession, session), session); err != nil {
		return err
	}
	return m.unlock(user, key)
}

// copyTree copies a folder (files, folders, symlinks), keeping modes and times.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		}
		return nil // sockets, pipes: not data
	})
}

// Package auth is the dashboard's one account: its password, the browsers signed in to it and
// the token the Noctalia plugin uses. The server only listens on 127.0.0.1, but every program
// and every user on the computer can reach that, so everything except the login and setup
// pages needs a session or the token.
//
// The account is created on first run, with a setup code printed in the terminal (and saved
// in data/setup-code), so nobody else on the computer can create it first.
package auth

import (
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

const (
	SessionLength = 30 * 24 * time.Hour

	// After freeLoginTries wrong passwords in a row, each try waits twice as long as the last,
	// up to maxLoginWait.
	freeLoginTries = 5
	maxLoginWait   = 15 * time.Minute
)

type account struct {
	Username string       `json:"username"`
	Password passwordHash `json:"password"`
	Token    string       `json:"token,omitempty"` // the plugin token's fingerprint
	Created  time.Time    `json:"created"`
}

type session struct {
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

type Auth struct {
	accountFile, sessionsFile, setupCodeFile string

	mu        sync.Mutex
	account   *account           // nil until setup
	sessions  map[string]session // by fingerprint
	setupCode string

	checking sync.Mutex // one password check at a time, so guessing can't hog the CPU
	failures int
	retryAt  time.Time

	now func() time.Time
}

// New loads the account from dataDir. Before setup it makes a setup code; SetupCode has it.
func New(dataDir string) (*Auth, error) {
	a := &Auth{
		accountFile:   filepath.Join(dataDir, "account.json"),
		sessionsFile:  filepath.Join(dataDir, "sessions.json"),
		setupCodeFile: filepath.Join(dataDir, "setup-code"),
		sessions:      map[string]session{},
		now:           time.Now,
	}
	var saved account
	if jsonfile.Read(a.accountFile, &saved) {
		if saved.Username == "" || saved.Password.Hash == "" {
			return nil, fmt.Errorf("%s is damaged: remove it to create the account again", a.accountFile)
		}
		a.account = &saved
		jsonfile.Read(a.sessionsFile, &a.sessions)
		a.dropExpired()
		return a, nil
	}
	if _, err := os.Stat(a.accountFile); err == nil {
		return nil, fmt.Errorf("%s isn't readable: fix or remove it", a.accountFile)
	}
	a.setupCode = newSecret()
	if err := os.WriteFile(a.setupCodeFile, []byte(a.setupCode+"\n"), 0o600); err != nil {
		return nil, err
	}
	return a, nil
}

// NeedsSetup is true until the account exists.
func (a *Auth) NeedsSetup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.account == nil
}

// SetupCode is the code the setup page asks for ("" once the account exists).
func (a *Auth) SetupCode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupCode
}

func (a *Auth) Username() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account == nil {
		return ""
	}
	return a.account.Username
}

// Setup creates the account and returns a session for the browser that made it.
func (a *Auth) Setup(code, username, password, confirm string) (string, error) {
	if err := checkUsername(username); err != nil {
		return "", err
	}
	if err := checkNewPassword(username, password, confirm); err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account != nil {
		return "", problem.Conflictf("The account already exists: log in instead")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(a.setupCode)) != 1 {
		return "", problem.Invalidf("That setup code is wrong. Use the link printed in the terminal, or the code in site/data/setup-code")
	}
	created := &account{Username: username, Password: hash, Created: a.now()}
	if err := jsonfile.Write(a.accountFile, created); err != nil {
		return "", err
	}
	a.account, a.setupCode = created, ""
	os.Remove(a.setupCodeFile)
	return a.startSession()
}

// TooManyTries is the error Login returns while it's making you wait.
type TooManyTries struct{ Wait time.Duration }

func (e TooManyTries) Error() string {
	return fmt.Sprintf("Too many wrong passwords. Try again in %s", e.Wait.Round(time.Second))
}

var errWrongLogin = problem.Invalidf("Wrong username or password")

// Login checks the username and password and returns a new session.
func (a *Auth) Login(username, password string) (string, error) {
	a.checking.Lock()
	defer a.checking.Unlock()
	if wait := a.retryAt.Sub(a.now()); wait > 0 {
		return "", TooManyTries{Wait: wait}
	}

	a.mu.Lock()
	current := a.account
	a.mu.Unlock()
	if current == nil {
		return "", problem.Conflictf("Create the account first")
	}

	hash := current.Password
	nameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(current.Username)) == 1
	if !nameMatches {
		hash = dummyHash
	}
	if !hash.matches(password) || !nameMatches {
		a.failures++
		if a.failures >= freeLoginTries {
			a.retryAt = a.now().Add(loginWait(a.failures))
		}
		return "", errWrongLogin
	}
	a.failures, a.retryAt = 0, time.Time{}

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startSession()
}

// loginWait is 1s after the 5th wrong password in a row, then 2s, 4s… up to 15 minutes.
func loginWait(failures int) time.Duration {
	wait := time.Second
	for i := freeLoginTries; i < failures && wait < maxLoginWait; i++ {
		wait *= 2
	}
	return min(wait, maxLoginWait)
}

// ChangePassword needs the current password. It ends every other session (all but keep).
func (a *Auth) ChangePassword(keep, current, password, confirm string) error {
	a.checking.Lock()
	defer a.checking.Unlock()
	a.mu.Lock()
	acc := a.account
	a.mu.Unlock()
	if acc == nil {
		return problem.Conflictf("Create the account first")
	}
	if !acc.Password.matches(current) {
		return problem.Invalidf("Your current password is wrong")
	}
	if err := checkNewPassword(acc.Username, password, confirm); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	changed := *a.account
	changed.Password = hash
	if err := jsonfile.Write(a.accountFile, changed); err != nil {
		return err
	}
	a.account = &changed
	return a.endSessionsExcept(keep)
}

// ---------- sessions ----------

// startSession needs a.mu held.
func (a *Auth) startSession() (string, error) {
	a.dropExpired()
	secret := newSecret()
	now := a.now()
	a.sessions[fingerprint(secret)] = session{Created: now, Expires: now.Add(SessionLength)}
	return secret, jsonfile.Write(a.sessionsFile, a.sessions)
}

// ValidSession is true for a session that exists and hasn't expired.
func (a *Auth) ValidSession(secret string) bool {
	if secret == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, found := a.sessions[fingerprint(secret)]
	return found && a.account != nil && a.now().Before(saved.Expires)
}

// Logout ends one session.
func (a *Auth) Logout(secret string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, fingerprint(secret))
	return jsonfile.Write(a.sessionsFile, a.sessions)
}

// LogoutOthers ends every session but keep.
func (a *Auth) LogoutOthers(keep string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.endSessionsExcept(keep)
}

// SessionCount is how many browsers are signed in.
func (a *Auth) SessionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropExpired()
	return len(a.sessions)
}

func (a *Auth) endSessionsExcept(keep string) error {
	kept := map[string]session{}
	if saved, found := a.sessions[fingerprint(keep)]; found {
		kept[fingerprint(keep)] = saved
	}
	a.sessions = kept
	return jsonfile.Write(a.sessionsFile, a.sessions)
}

func (a *Auth) dropExpired() {
	now := a.now()
	for id, saved := range a.sessions {
		if !now.Before(saved.Expires) {
			delete(a.sessions, id)
		}
	}
}

// ---------- the plugin's token ----------

// NewToken replaces the plugin token and returns it. Only its fingerprint is kept, so it can
// only be shown now.
func (a *Auth) NewToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account == nil {
		return "", problem.Conflictf("Create the account first")
	}
	token := "dd_" + newSecret()
	changed := *a.account
	changed.Token = fingerprint(token)
	if err := jsonfile.Write(a.accountFile, changed); err != nil {
		return "", err
	}
	a.account = &changed
	return token, nil
}

// RemoveToken stops the current token from working.
func (a *Auth) RemoveToken() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account == nil {
		return nil
	}
	changed := *a.account
	changed.Token = ""
	if err := jsonfile.Write(a.accountFile, changed); err != nil {
		return err
	}
	a.account = &changed
	return nil
}

func (a *Auth) HasToken() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.account != nil && a.account.Token != ""
}

func (a *Auth) ValidToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if token == "" || a.account == nil || a.account.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(fingerprint(token)), []byte(a.account.Token)) == 1
}

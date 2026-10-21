// Package auth is the dashboard's accounts: users and their passwords, roles and agent policy,
// the browsers signed in to each, the bar plugin's tokens and the invites admins send.
//
// The dashboard can be reached over a network, so everything except the login, setup and
// invite pages needs a session or a token, and every request is tied to one user: pages and
// the API only ever see that user's data.
//
// The first account is created with a setup code printed in the terminal (and saved in
// data/setup-code), so nobody else can claim the server first. It's an admin; admins invite
// everyone else.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

const (
	SessionLength = 30 * 24 * time.Hour
	InviteLength  = 7 * 24 * time.Hour

	// After freeLoginTries wrong passwords in a row (for one username, or from one address),
	// each try waits twice as long as the last, up to maxLoginWait.
	freeLoginTries = 5
	maxLoginWait   = 15 * time.Minute
)

type Role string

const (
	Admin  Role = "admin"
	Member Role = "user"
)

// User is one account. Methods on Auth return copies, so callers can't change the saved one.
type User struct {
	ID        string       `json:"id"`
	Username  string       `json:"username"`
	Password  passwordHash `json:"password"`
	Role      Role         `json:"role"`
	Disabled  bool         `json:"disabled,omitempty"`
	Host      bool         `json:"host,omitempty"` // uses this computer's own Taskwarrior, MyFitnessPal login and Anki
	Policy    Policy       `json:"policy"`
	Created   time.Time    `json:"created"`
	LastLogin time.Time    `json:"last_login,omitzero"`
}

func (u User) IsAdmin() bool { return u.Role == Admin }

// Policy is what a user's AI agent may use. Admins set it, and the server checks it again for
// every message, whatever the page sent.
type Policy struct {
	Providers []string `json:"providers"`  // "claude", "codex", "gemini", "local"
	MCPs      []string `json:"mcps"`       // "myfitnesspal", "anki"
	Web       bool     `json:"web"`        // web search and fetch
	ReadFiles bool     `json:"read_files"` // read the user's own notes and data in the sandbox
	EditFiles bool     `json:"edit_files"` // change the user's own notes and plan
}

var (
	KnownProviders = []string{"claude", "codex", "gemini", "local"}
	KnownMCPs      = []string{"myfitnesspal", "anki"}
	// FullPolicy is everything but the local model, which an admin turns on (it reaches a server the user names).
	FullPolicy = Policy{Providers: []string{"claude", "codex", "gemini"}, MCPs: KnownMCPs, Web: true, ReadFiles: true, EditFiles: true}
)

func (p Policy) AllowsProvider(id string) bool { return slices.Contains(p.Providers, id) }
func (p Policy) AllowsMCP(name string) bool    { return slices.Contains(p.MCPs, name) }

// Clean keeps only known providers and MCPs; editing files needs reading them.
func (p Policy) Clean() Policy {
	keep := func(values, known []string) []string {
		kept := []string{}
		for _, v := range known {
			if slices.Contains(values, v) {
				kept = append(kept, v)
			}
		}
		return kept
	}
	p.Providers, p.MCPs = keep(p.Providers, KnownProviders), keep(p.MCPs, KnownMCPs)
	p.EditFiles = p.EditFiles && p.ReadFiles
	return p
}

type session struct {
	User    string    `json:"user"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

// invite lets someone set a password: a new account, or a reset for an existing one.
type invite struct {
	Username string    `json:"username"`
	Role     Role      `json:"role"`
	ResetFor string    `json:"reset_for,omitempty"` // a user id: a password reset
	Expires  time.Time `json:"expires"`
}

// PendingInvite is an invite as the admin page lists it (the code itself is only shown once).
type PendingInvite struct {
	Username string
	Role     Role
	Reset    bool
	Expires  time.Time
}

// state is accounts.json. Tokens and invites are kept by fingerprint, so the file never holds
// one that works.
type state struct {
	Users         []*User           `json:"users"`
	Tokens        map[string]string `json:"tokens"`  // fingerprint → user id
	Invites       map[string]invite `json:"invites"` // fingerprint → invite
	DefaultPolicy Policy            `json:"default_policy"`
}

type failures struct {
	count   int
	retryAt time.Time
}

type Auth struct {
	accountsFile, sessionsFile, setupCodeFile string

	mu        sync.Mutex
	state     state
	sessions  map[string]session // by fingerprint
	setupCode string

	checking sync.Mutex // one password check at a time, so guessing can't hog the CPU
	failed   map[string]*failures

	now func() time.Time
}

// New loads the accounts from dataDir. Before setup it makes a setup code; SetupCode has it.
func New(dataDir string) (*Auth, error) {
	a := &Auth{
		accountsFile:  filepath.Join(dataDir, "accounts.json"),
		sessionsFile:  filepath.Join(dataDir, "sessions.json"),
		setupCodeFile: filepath.Join(dataDir, "setup-code"),
		sessions:      map[string]session{},
		failed:        map[string]*failures{},
		now:           time.Now,
	}
	if err := a.load(dataDir); err != nil {
		return nil, err
	}
	if len(a.state.Users) > 0 {
		jsonfile.Read(a.sessionsFile, &a.sessions)
		a.dropExpired()
		return a, nil
	}
	a.setupCode = newSecret()
	if err := os.WriteFile(a.setupCodeFile, []byte(a.setupCode+"\n"), 0o600); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Auth) load(dataDir string) error {
	if jsonfile.Read(a.accountsFile, &a.state) {
		for _, u := range a.state.Users {
			if u.ID == "" || u.Username == "" || u.Password.Hash == "" {
				return fmt.Errorf("%s is damaged: a user has no id, name or password", a.accountsFile)
			}
		}
	} else if _, err := os.Stat(a.accountsFile); err == nil {
		return fmt.Errorf("%s isn't readable: fix or remove it", a.accountsFile)
	} else if err := a.migrateSingleAccount(dataDir); err != nil {
		return err
	}
	if a.state.Tokens == nil {
		a.state.Tokens = map[string]string{}
	}
	if a.state.Invites == nil {
		a.state.Invites = map[string]invite{}
	}
	if a.state.DefaultPolicy.Providers == nil {
		a.state.DefaultPolicy = FullPolicy
	}
	return nil
}

// migrateSingleAccount turns the one-account account.json (from before there were several
// users) into the first admin, keeping its password, plugin token and signed-in browsers.
// That admin is the host: it keeps this computer's Taskwarrior, MyFitnessPal and Anki.
func (a *Auth) migrateSingleAccount(dataDir string) error {
	oldFile := filepath.Join(dataDir, "account.json")
	var old struct {
		Username string       `json:"username"`
		Password passwordHash `json:"password"`
		Token    string       `json:"token"`
		Created  time.Time    `json:"created"`
	}
	if !jsonfile.Read(oldFile, &old) {
		return nil
	}
	if old.Username == "" || old.Password.Hash == "" {
		return fmt.Errorf("%s is damaged: remove it to create the account again", oldFile)
	}
	admin := &User{ID: newUserID(), Username: old.Username, Password: old.Password, Role: Admin, Host: true,
		Policy: FullPolicy, Created: old.Created}
	a.state = state{Users: []*User{admin}, Tokens: map[string]string{}, Invites: map[string]invite{}, DefaultPolicy: FullPolicy}
	if old.Token != "" {
		a.state.Tokens[old.Token] = admin.ID
	}
	var oldSessions map[string]session
	if jsonfile.Read(a.sessionsFile, &oldSessions) {
		for id, s := range oldSessions {
			s.User = admin.ID
			oldSessions[id] = s
		}
		if err := jsonfile.Write(a.sessionsFile, oldSessions); err != nil {
			return err
		}
	}
	if err := jsonfile.Write(a.accountsFile, a.state); err != nil {
		return err
	}
	return os.Rename(oldFile, oldFile+".migrated")
}

// save writes accounts.json; it needs a.mu held.
func (a *Auth) save() error { return jsonfile.Write(a.accountsFile, a.state) }

// NeedsSetup is true until the first account exists.
func (a *Auth) NeedsSetup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.state.Users) == 0
}

// SetupCode is the code the setup page asks for ("" once the first account exists).
func (a *Auth) SetupCode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupCode
}

// Setup creates the first account, an admin and the host, and returns a session for it.
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
	if len(a.state.Users) > 0 {
		return "", problem.Conflictf("The first account already exists: log in instead")
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(a.setupCode)) != 1 {
		return "", problem.Invalidf("That setup code is wrong. Use the link printed in the terminal, or the code in site/data/setup-code")
	}
	admin := &User{ID: newUserID(), Username: username, Password: hash, Role: Admin, Host: true,
		Policy: FullPolicy, Created: a.now(), LastLogin: a.now()}
	a.state.Users = []*User{admin}
	if err := a.save(); err != nil {
		a.state.Users = nil
		return "", err
	}
	a.setupCode = ""
	os.Remove(a.setupCodeFile)
	return a.startSession(admin.ID)
}

// ---------- logging in ----------

// TooManyTries is the error Login returns while it's making you wait.
type TooManyTries struct{ Wait time.Duration }

func (e TooManyTries) Error() string {
	return fmt.Sprintf("Too many wrong passwords. Try again in %s", e.Wait.Round(time.Second))
}

var errWrongLogin = problem.Invalidf("Wrong username or password")

// Login checks the username and password and returns a new session. Wrong guesses slow down
// both that username and the address they come from (ip, "" when unknown).
func (a *Auth) Login(username, password, ip string) (string, error) {
	a.checking.Lock()
	defer a.checking.Unlock()
	keys := []string{"user:" + strings.ToLower(username)}
	if ip != "" {
		keys = append(keys, "ip:"+ip)
	}
	for _, key := range keys {
		if f := a.failed[key]; f != nil {
			if wait := f.retryAt.Sub(a.now()); wait > 0 {
				return "", TooManyTries{Wait: wait}
			}
		}
	}

	a.mu.Lock()
	user := a.userByName(username)
	hash, usable := dummyHash, false
	if user != nil {
		hash, usable = user.Password, !user.Disabled
	}
	a.mu.Unlock()
	ok := hash.matches(password) && usable
	if !ok {
		for _, key := range keys {
			f := a.failed[key]
			if f == nil {
				f = &failures{}
				a.failed[key] = f
			}
			f.count++
			if f.count >= freeLoginTries {
				f.retryAt = a.now().Add(loginWait(f.count))
			}
		}
		return "", errWrongLogin
	}
	for _, key := range keys {
		delete(a.failed, key)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	user.LastLogin = a.now()
	a.save()
	return a.startSession(user.ID)
}

// loginWait is 1s after the 5th wrong password in a row, then 2s, 4s… up to 15 minutes.
func loginWait(failures int) time.Duration {
	wait := time.Second
	for i := freeLoginTries; i < failures && wait < maxLoginWait; i++ {
		wait *= 2
	}
	return min(wait, maxLoginWait)
}

// userByName needs a.mu held. Usernames are matched without case.
func (a *Auth) userByName(name string) *User {
	for _, u := range a.state.Users {
		if strings.EqualFold(u.Username, name) {
			return u
		}
	}
	return nil
}

// userByID needs a.mu held.
func (a *Auth) userByID(id string) *User {
	for _, u := range a.state.Users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

// ChangePassword needs the current password. It ends the user's other sessions (all but keep).
func (a *Auth) ChangePassword(userID, keep, current, password, confirm string) error {
	a.checking.Lock()
	defer a.checking.Unlock()
	a.mu.Lock()
	user := a.userByID(userID)
	var saved User
	if user != nil {
		saved = *user
	}
	a.mu.Unlock()
	if user == nil {
		return problem.NotFoundf("No such user")
	}
	if !saved.Password.matches(current) {
		return problem.Invalidf("Your current password is wrong")
	}
	if err := checkNewPassword(saved.Username, password, confirm); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	user.Password = hash
	if err := a.save(); err != nil {
		return err
	}
	return a.endSessions(userID, keep)
}

// ---------- sessions ----------

// startSession needs a.mu held.
func (a *Auth) startSession(userID string) (string, error) {
	a.dropExpired()
	secret := newSecret()
	now := a.now()
	a.sessions[fingerprint(secret)] = session{User: userID, Created: now, Expires: now.Add(SessionLength)}
	return secret, jsonfile.Write(a.sessionsFile, a.sessions)
}

// Session is the user signed in with this session secret, if it's valid, unexpired and the
// user isn't disabled.
func (a *Auth) Session(secret string) (User, bool) {
	if secret == "" {
		return User{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, found := a.sessions[fingerprint(secret)]
	if !found || !a.now().Before(saved.Expires) {
		return User{}, false
	}
	user := a.userByID(saved.User)
	if user == nil || user.Disabled {
		return User{}, false
	}
	return *user, true
}

// Logout ends one session.
func (a *Auth) Logout(secret string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, fingerprint(secret))
	return jsonfile.Write(a.sessionsFile, a.sessions)
}

// LogoutOthers ends every session of the user but keep.
func (a *Auth) LogoutOthers(userID, keep string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.endSessions(userID, keep)
}

// SessionCount is how many browsers the user is signed in with.
func (a *Auth) SessionCount(userID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropExpired()
	count := 0
	for _, s := range a.sessions {
		if s.User == userID {
			count++
		}
	}
	return count
}

// endSessions ends the user's sessions except keep ("" ends them all). It needs a.mu held.
func (a *Auth) endSessions(userID, keep string) error {
	kept := fingerprint(keep)
	for id, s := range a.sessions {
		if s.User == userID && (keep == "" || id != kept) {
			delete(a.sessions, id)
		}
	}
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

// NewToken replaces the user's plugin token and returns it. Only its fingerprint is kept, so
// it can only be shown now.
func (a *Auth) NewToken(userID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.userByID(userID) == nil {
		return "", problem.NotFoundf("No such user")
	}
	a.dropTokens(userID)
	token := "dd_" + newSecret()
	a.state.Tokens[fingerprint(token)] = userID
	return token, a.save()
}

// RemoveToken stops the user's token from working.
func (a *Auth) RemoveToken(userID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropTokens(userID)
	return a.save()
}

func (a *Auth) dropTokens(userID string) {
	for fp, owner := range a.state.Tokens {
		if owner == userID {
			delete(a.state.Tokens, fp)
		}
	}
}

func (a *Auth) HasToken(userID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(mapValues(a.state.Tokens), userID)
}

// TokenUser is the user whose plugin token this is.
func (a *Auth) TokenUser(token string) (User, bool) {
	if token == "" {
		return User{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	want := fingerprint(token)
	for fp, owner := range a.state.Tokens {
		if subtle.ConstantTimeCompare([]byte(fp), []byte(want)) == 1 {
			if user := a.userByID(owner); user != nil && !user.Disabled {
				return *user, true
			}
		}
	}
	return User{}, false
}

// ---------- invites ----------

// Invite makes a one-time link code for a new account with this username and role. Only its
// fingerprint is kept: the code can only be shown now.
func (a *Auth) Invite(username string, role Role) (string, error) {
	if err := checkUsername(username); err != nil {
		return "", err
	}
	if role != Admin && role != Member {
		return "", problem.Invalidf("Pick a role")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.userByName(username) != nil || a.pendingInviteFor(username, "") != "" {
		return "", problem.Conflictf("That username is taken or already invited")
	}
	return a.addInvite(invite{Username: username, Role: role})
}

// ResetPassword makes a one-time link where the user sets a new password. Their sessions end
// when it's used.
func (a *Auth) ResetPassword(userID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	user := a.userByID(userID)
	if user == nil {
		return "", problem.NotFoundf("No such user")
	}
	if old := a.pendingInviteFor(user.Username, userID); old != "" {
		delete(a.state.Invites, old)
	}
	return a.addInvite(invite{Username: user.Username, Role: user.Role, ResetFor: userID})
}

// addInvite needs a.mu held.
func (a *Auth) addInvite(inv invite) (string, error) {
	a.dropExpiredInvites()
	code := newSecret()
	inv.Expires = a.now().Add(InviteLength)
	a.state.Invites[fingerprint(code)] = inv
	return code, a.save()
}

// pendingInviteFor is the fingerprint of the invite for that username (a reset when resetFor
// is set), or "". It needs a.mu held.
func (a *Auth) pendingInviteFor(username, resetFor string) string {
	for fp, inv := range a.state.Invites {
		if strings.EqualFold(inv.Username, username) && inv.ResetFor == resetFor && a.now().Before(inv.Expires) {
			return fp
		}
	}
	return ""
}

func (a *Auth) dropExpiredInvites() {
	for fp, inv := range a.state.Invites {
		if !a.now().Before(inv.Expires) {
			delete(a.state.Invites, fp)
		}
	}
}

// InviteFor is what the invite page shows for a code: the username and whether it's a reset.
func (a *Auth) InviteFor(code string) (username string, reset, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	inv, found := a.state.Invites[fingerprint(code)]
	if !found || !a.now().Before(inv.Expires) {
		return "", false, false
	}
	return inv.Username, inv.ResetFor != "", true
}

// AcceptInvite sets the password from an invite: it creates the account (with the default
// policy) or resets the password, and returns a session.
func (a *Auth) AcceptInvite(code, password, confirm string) (string, error) {
	a.mu.Lock()
	inv, found := a.state.Invites[fingerprint(code)]
	a.mu.Unlock()
	if !found || !a.now().Before(inv.Expires) {
		return "", problem.Invalidf("This invite link is wrong or has expired. Ask your admin for a new one.")
	}
	if err := checkNewPassword(inv.Username, password, confirm); err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, still := a.state.Invites[fingerprint(code)]; !still {
		return "", problem.Invalidf("This invite link was already used")
	}
	delete(a.state.Invites, fingerprint(code))
	var user *User
	if inv.ResetFor != "" {
		if user = a.userByID(inv.ResetFor); user == nil {
			return "", problem.NotFoundf("That account was deleted")
		}
		user.Password = hash
		if err := a.endSessions(user.ID, ""); err != nil {
			return "", err
		}
	} else {
		if a.userByName(inv.Username) != nil {
			return "", problem.Conflictf("That username was taken meanwhile. Ask your admin for a new invite.")
		}
		user = &User{ID: newUserID(), Username: inv.Username, Password: hash, Role: inv.Role,
			Policy: a.state.DefaultPolicy, Created: a.now()}
		a.state.Users = append(a.state.Users, user)
	}
	user.LastLogin = a.now()
	if err := a.save(); err != nil {
		return "", err
	}
	return a.startSession(user.ID)
}

// Invites lists the invites not yet used.
func (a *Auth) Invites() []PendingInvite {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropExpiredInvites()
	var pending []PendingInvite
	for _, inv := range a.state.Invites {
		pending = append(pending, PendingInvite{Username: inv.Username, Role: inv.Role, Reset: inv.ResetFor != "", Expires: inv.Expires})
	}
	slices.SortFunc(pending, func(x, y PendingInvite) int { return strings.Compare(x.Username, y.Username) })
	return pending
}

// CancelInvite removes the pending invites for that username.
func (a *Auth) CancelInvite(username string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for fp, inv := range a.state.Invites {
		if strings.EqualFold(inv.Username, username) {
			delete(a.state.Invites, fp)
		}
	}
	return a.save()
}

// ---------- admin ----------

// Users lists every account (copies), in the order they were made.
func (a *Auth) Users() []User {
	a.mu.Lock()
	defer a.mu.Unlock()
	users := make([]User, len(a.state.Users))
	for i, u := range a.state.Users {
		users[i] = *u
	}
	return users
}

// User is one account by id (a copy).
func (a *Auth) User(id string) (User, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.userByID(id); u != nil {
		return *u, true
	}
	return User{}, false
}

// changeOther runs an admin's change to another user, refusing changes to yourself (so an admin
// can't lock themselves out) and anything that would leave no active admin. It needs a.mu held.
func (a *Auth) changeOther(adminID, userID string, change func(*User)) error {
	if adminID == userID {
		return problem.Forbiddenf("You can't do that to your own account")
	}
	user := a.userByID(userID)
	if user == nil {
		return problem.NotFoundf("No such user")
	}
	before := *user
	change(user)
	if a.activeAdmins() == 0 {
		*user = before
		return problem.Conflictf("There has to be at least one active admin")
	}
	return a.save()
}

func (a *Auth) activeAdmins() int {
	count := 0
	for _, u := range a.state.Users {
		if u.Role == Admin && !u.Disabled {
			count++
		}
	}
	return count
}

// SetDisabled turns an account off or back on. A disabled user's sessions end.
func (a *Auth) SetDisabled(adminID, userID string, disabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.changeOther(adminID, userID, func(u *User) { u.Disabled = disabled }); err != nil {
		return err
	}
	if disabled {
		return a.endSessions(userID, "")
	}
	return nil
}

// SetRole makes a user an admin or a regular user.
func (a *Auth) SetRole(adminID, userID string, role Role) error {
	if role != Admin && role != Member {
		return problem.Invalidf("Pick a role")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.changeOther(adminID, userID, func(u *User) { u.Role = role })
}

// DeleteUser removes the account, its sessions, token and invites. The caller deletes the
// user's data.
func (a *Auth) DeleteUser(adminID, userID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	user := a.userByID(userID)
	if user == nil {
		return problem.NotFoundf("No such user")
	}
	if user.Host {
		return problem.Forbiddenf("The host account uses this computer's own Taskwarrior and logins: it can't be deleted")
	}
	if err := a.changeOther(adminID, userID, func(u *User) { u.Disabled = true }); err != nil {
		return err
	}
	a.state.Users = slices.DeleteFunc(a.state.Users, func(u *User) bool { return u.ID == userID })
	a.dropTokens(userID)
	for fp, inv := range a.state.Invites {
		if inv.ResetFor == userID {
			delete(a.state.Invites, fp)
		}
	}
	if err := a.save(); err != nil {
		return err
	}
	return a.endSessions(userID, "")
}

// SetPolicy sets what the user's agent may use.
func (a *Auth) SetPolicy(userID string, policy Policy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	user := a.userByID(userID)
	if user == nil {
		return problem.NotFoundf("No such user")
	}
	user.Policy = policy.Clean()
	return a.save()
}

func (a *Auth) DefaultPolicy() Policy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.DefaultPolicy
}

// SetDefaultPolicy sets the policy new users start with.
func (a *Auth) SetDefaultPolicy(policy Policy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.DefaultPolicy = policy.Clean()
	return a.save()
}

// ---------- helpers ----------

// newUserID is 12 random hex characters: a folder name that never changes, unlike the username.
func newUserID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mapValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, v := range m {
		values = append(values, v)
	}
	return values
}

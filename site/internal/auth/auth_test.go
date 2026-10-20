package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dashboard/internal/jsonfile"
)

const goodPassword = "correct horse battery"

func newTestAuth(t *testing.T) (*Auth, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, dir
}

// valid reports whether the session works (for any user).
func valid(a *Auth, session string) bool {
	_, ok := a.Session(session)
	return ok
}

// adminID is the first account's id.
func adminID(a *Auth) string { return a.Users()[0].ID }

func setUp(t *testing.T) (*Auth, string, string) {
	t.Helper()
	a, dir := newTestAuth(t)
	session, err := a.Setup(a.SetupCode(), "me", goodPassword, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	return a, dir, session
}

func TestSetupNeedsTheCode(t *testing.T) {
	a, dir := newTestAuth(t)
	if !a.NeedsSetup() || a.SetupCode() == "" {
		t.Fatal("a new install should need setup and have a code")
	}
	if saved, _ := os.ReadFile(filepath.Join(dir, "setup-code")); strings.TrimSpace(string(saved)) != a.SetupCode() {
		t.Fatal("the setup code should be saved for the owner")
	}
	if _, err := a.Setup("wrong", "me", goodPassword, goodPassword); err == nil {
		t.Fatal("setup with a wrong code should fail")
	}
	if _, err := a.Setup("", "me", goodPassword, goodPassword); err == nil {
		t.Fatal("setup without a code should fail")
	}
}

func TestSetupChecksTheNewAccount(t *testing.T) {
	a, _ := newTestAuth(t)
	code := a.SetupCode()
	bad := map[string][3]string{
		"short password":    {"me", "short", "short"},
		"not matching":      {"me", goodPassword, goodPassword + "!"},
		"password = name":   {"administrator1", "administrator1", "administrator1"},
		"one character":     {"me", "aaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaa"},
		"empty username":    {"", goodPassword, goodPassword},
		"username with </>": {"<me>", goodPassword, goodPassword},
	}
	for name, c := range bad {
		if _, err := a.Setup(code, c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: setup should fail", name)
		}
	}
}

func TestSetupHappensOnce(t *testing.T) {
	a, dir, session := setUp(t)
	if a.NeedsSetup() || a.SetupCode() != "" {
		t.Fatal("setup should be over")
	}
	if user, ok := a.Session(session); !ok || !user.IsAdmin() || !user.Host {
		t.Fatal("setup should sign the browser in as an admin and the host")
	}
	if _, err := os.Stat(filepath.Join(dir, "setup-code")); !os.IsNotExist(err) {
		t.Fatal("the setup code file should be removed")
	}
	if _, err := a.Setup("", "intruder", goodPassword, goodPassword); err == nil {
		t.Fatal("a second setup should fail")
	}
}

func TestFilesNeverHoldThePasswordOrSecrets(t *testing.T) {
	a, dir, session := setUp(t)
	token, _ := a.NewToken(adminID(a))
	code, _ := a.Invite("friend", Member)
	for _, name := range []string{"accounts.json", "sessions.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want owner-only", name, info.Mode().Perm())
		}
		data, _ := os.ReadFile(filepath.Join(dir, name))
		for _, secret := range []string{goodPassword, session, token, code} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds a secret", name)
			}
		}
	}
}

func TestLogin(t *testing.T) {
	a, dir, _ := setUp(t)
	if _, err := a.Login("me", "wrong password!", ""); err == nil {
		t.Fatal("a wrong password should fail")
	}
	if _, err := a.Login("you", goodPassword, ""); err == nil {
		t.Fatal("a wrong username should fail")
	}
	session, err := a.Login("ME", goodPassword, "")
	if err != nil || !valid(a, session) {
		t.Fatalf("login failed: %v", err)
	}

	reloaded, err := New(dir)
	if err != nil || !valid(reloaded, session) || reloaded.NeedsSetup() {
		t.Fatal("the account and sessions should survive a restart")
	}
	if _, err := reloaded.Login("me", goodPassword, ""); err != nil {
		t.Fatal("the password should still work after a restart")
	}
}

func TestLoginSlowsDownGuessing(t *testing.T) {
	a, _, _ := setUp(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	for range freeLoginTries {
		if _, err := a.Login("me", "guess guess guess", "10.0.0.9"); errors.As(err, new(TooManyTries)) {
			t.Fatal("the first tries shouldn't wait")
		}
	}
	if _, err := a.Login("me", goodPassword, "10.0.0.1"); !errors.As(err, new(TooManyTries)) {
		t.Fatal("even the right password should wait after too many wrong ones")
	}
	if _, err := a.Login("someone", goodPassword, "10.0.0.9"); !errors.As(err, new(TooManyTries)) {
		t.Fatal("the guessing address should wait for every username")
	}
	now = now.Add(2 * time.Second)
	if _, err := a.Login("me", goodPassword, "10.0.0.1"); err != nil {
		t.Fatalf("after waiting, the right password should work: %v", err)
	}
	if loginWait(100) != maxLoginWait {
		t.Fatal("the wait should stop growing")
	}
}

func TestSessionsExpireAndEnd(t *testing.T) {
	a, _, session := setUp(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	other, _ := a.Login("me", goodPassword, "")

	if err := a.LogoutOthers(adminID(a), session); err != nil || valid(a, other) || !valid(a, session) {
		t.Fatal("logging out others should keep only this session")
	}
	a.Logout(session)
	if valid(a, session) {
		t.Fatal("logout should end the session")
	}

	fresh, _ := a.Login("me", goodPassword, "")
	now = now.Add(SessionLength)
	if valid(a, fresh) {
		t.Fatal("sessions should expire")
	}
	if valid(a, "") || valid(a, "made-up") {
		t.Fatal("unknown sessions aren't valid")
	}
}

func TestChangePassword(t *testing.T) {
	a, _, session := setUp(t)
	other, _ := a.Login("me", goodPassword, "")
	const newPassword = "a different long one"
	if err := a.ChangePassword(adminID(a), session, "wrong current pw", newPassword, newPassword); err == nil {
		t.Fatal("changing needs the current password")
	}
	if err := a.ChangePassword(adminID(a), session, goodPassword, newPassword, newPassword); err != nil {
		t.Fatal(err)
	}
	if !valid(a, session) || valid(a, other) {
		t.Fatal("a new password should end every other session")
	}
	if _, err := a.Login("me", goodPassword, ""); err == nil {
		t.Fatal("the old password should stop working")
	}
	if _, err := a.Login("me", newPassword, ""); err != nil {
		t.Fatal("the new password should work")
	}
}

func TestToken(t *testing.T) {
	a, _, _ := setUp(t)
	id := adminID(a)
	if _, ok := a.TokenUser(""); ok || a.HasToken(id) {
		t.Fatal("there's no token until one is made")
	}
	first, _ := a.NewToken(id)
	second, _ := a.NewToken(id)
	if _, ok := a.TokenUser(first); ok {
		t.Fatal("a new token should replace the old one")
	}
	if user, ok := a.TokenUser(second); !ok || user.ID != id {
		t.Fatal("the token should belong to its user")
	}
	a.RemoveToken(id)
	if _, ok := a.TokenUser(second); ok {
		t.Fatal("a removed token shouldn't work")
	}
}

func TestGuard(t *testing.T) {
	a, _ := newTestAuth(t)
	handler := a.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	request := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if w := request("GET", "/", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Fatalf("before setup, pages should go to /setup: %d %s", w.Code, w.Header().Get("Location"))
	}
	session, _ := a.Setup(a.SetupCode(), "me", goodPassword, goodPassword)
	token, _ := a.NewToken(adminID(a))
	cookie := map[string]string{"Cookie": SessionCookie + "=" + session}

	cases := []struct {
		method, path string
		headers      map[string]string
		want         int
	}{
		{"GET", "/", nil, http.StatusSeeOther},
		{"GET", "/login", nil, http.StatusTeapot},
		{"GET", "/invite", nil, http.StatusTeapot},
		{"GET", "/static/css/base.css", nil, http.StatusTeapot},
		{"POST", "/tasks", nil, http.StatusUnauthorized},
		{"POST", "/tasks", map[string]string{"HX-Request": "true"}, http.StatusUnauthorized},
		{"GET", "/", cookie, http.StatusTeapot},
		{"POST", "/tasks", cookie, http.StatusTeapot},
		{"GET", "/", map[string]string{"Cookie": SessionCookie + "=forged"}, http.StatusSeeOther},
		{"GET", "/api/tasks", nil, http.StatusUnauthorized},
		{"GET", "/api/tasks", map[string]string{"Authorization": "Bearer " + token}, http.StatusTeapot},
		{"GET", "/api/tasks", map[string]string{"Authorization": "Bearer dd_wrong"}, http.StatusUnauthorized},
		{"GET", "/api/tasks", cookie, http.StatusTeapot},
		// The token is only for the API, not the pages.
		{"GET", "/", map[string]string{"Authorization": "Bearer " + token}, http.StatusSeeOther},
	}
	for _, c := range cases {
		if w := request(c.method, c.path, c.headers); w.Code != c.want {
			t.Errorf("%s %s %v: got %d, want %d", c.method, c.path, c.headers, w.Code, c.want)
		}
	}
	if w := request("POST", "/tasks", map[string]string{"HX-Request": "true"}); w.Header().Get("HX-Redirect") != "/login" {
		t.Error("HTMX requests without a session should be sent to the login")
	}
}

// inviteUser invites and joins a regular user, returning their id and session.
func inviteUser(t *testing.T, a *Auth, name string) (string, string) {
	t.Helper()
	code, err := a.Invite(name, Member)
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.AcceptInvite(code, goodPassword, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := a.Session(session)
	return user.ID, session
}

func TestInvites(t *testing.T) {
	a, _, _ := setUp(t)
	code, err := a.Invite("friend", Member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invite("FRIEND", Member); err == nil {
		t.Fatal("a username can only be invited once")
	}
	if _, err := a.Invite("me", Member); err == nil {
		t.Fatal("a taken username can't be invited")
	}
	if name, reset, ok := a.InviteFor(code); !ok || name != "friend" || reset {
		t.Fatalf("the invite page should know the username: %q %v %v", name, reset, ok)
	}
	if _, err := a.AcceptInvite(code, "short", "short"); err == nil {
		t.Fatal("the invite still checks the password")
	}
	session, err := a.AcceptInvite(code, goodPassword, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, ok := a.Session(session)
	if !ok || user.Username != "friend" || user.IsAdmin() || user.Host || !user.Policy.AllowsMCP("anki") {
		t.Fatalf("joined user wrong: %+v", user)
	}
	if _, err := a.AcceptInvite(code, goodPassword, goodPassword); err == nil {
		t.Fatal("an invite works once")
	}
	if _, err := a.Login("friend", goodPassword, ""); err != nil {
		t.Fatal("the new user should be able to log in")
	}

	late, _ := a.Invite("late", Member)
	now := time.Now().Add(InviteLength)
	a.now = func() time.Time { return now }
	if _, err := a.AcceptInvite(late, goodPassword, goodPassword); err == nil {
		t.Fatal("invites expire")
	}
}

func TestResetPassword(t *testing.T) {
	a, _, _ := setUp(t)
	id, session := inviteUser(t, a, "friend")
	code, err := a.ResetPassword(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, reset, _ := a.InviteFor(code); !reset {
		t.Fatal("a reset link should say it's a reset")
	}
	const newPassword = "a brand new password"
	if _, err := a.AcceptInvite(code, newPassword, newPassword); err != nil {
		t.Fatal(err)
	}
	if valid(a, session) {
		t.Fatal("a reset should end the old sessions")
	}
	if _, err := a.Login("friend", newPassword, ""); err != nil {
		t.Fatal("the new password should work")
	}
	if len(a.Users()) != 2 {
		t.Fatal("a reset shouldn't make another account")
	}
}

func TestAdminsCantLockEveryoneOut(t *testing.T) {
	a, _, _ := setUp(t)
	admin := adminID(a)
	friend, session := inviteUser(t, a, "friend")

	if err := a.SetRole(admin, admin, Member); err == nil {
		t.Fatal("an admin can't demote themselves")
	}
	if err := a.SetDisabled(admin, admin, true); err == nil {
		t.Fatal("an admin can't disable themselves")
	}
	if err := a.DeleteUser(friend, admin); err == nil {
		t.Fatal("the host account can't be deleted")
	}

	if err := a.SetDisabled(admin, friend, true); err != nil {
		t.Fatal(err)
	}
	if valid(a, session) {
		t.Fatal("disabling should end the user's sessions")
	}
	if _, err := a.Login("friend", goodPassword, ""); err == nil {
		t.Fatal("a disabled user can't log in")
	}
	a.SetDisabled(admin, friend, false)

	if err := a.SetRole(admin, friend, Admin); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRole(friend, admin, Member); err != nil {
		t.Fatal("with two admins, one can demote the other")
	}
	if err := a.SetDisabled(admin, friend, true); err == nil {
		t.Fatal("the last active admin can't be disabled")
	}
}

func TestDeleteUser(t *testing.T) {
	a, _, _ := setUp(t)
	friend, session := inviteUser(t, a, "friend")
	token, _ := a.NewToken(friend)
	if err := a.DeleteUser(adminID(a), friend); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.TokenUser(token); ok || valid(a, session) || len(a.Users()) != 1 {
		t.Fatal("deleting should remove the account, its token and its sessions")
	}
}

func TestUsersOnlyGetTheirOwnSessionsAndTokens(t *testing.T) {
	a, _, adminSession := setUp(t)
	friend, friendSession := inviteUser(t, a, "friend")
	token, _ := a.NewToken(friend)
	if user, _ := a.Session(friendSession); user.ID != friend {
		t.Fatal("a session belongs to its user")
	}
	if user, _ := a.TokenUser(token); user.ID != friend {
		t.Fatal("a token belongs to its user")
	}
	if a.HasToken(adminID(a)) {
		t.Fatal("one user's token isn't another's")
	}
	a.LogoutOthers(friend, "")
	if valid(a, friendSession) || !valid(a, adminSession) {
		t.Fatal("ending one user's sessions leaves the others alone")
	}
}

func TestPolicy(t *testing.T) {
	a, _, _ := setUp(t)
	friend, _ := inviteUser(t, a, "friend")
	a.SetPolicy(friend, Policy{Providers: []string{"claude", "made-up"}, MCPs: []string{"anki"}, EditFiles: true})
	user, _ := a.User(friend)
	if !user.Policy.AllowsProvider("claude") || user.Policy.AllowsProvider("made-up") || user.Policy.AllowsMCP("myfitnesspal") {
		t.Fatalf("policy not cleaned: %+v", user.Policy)
	}
	if user.Policy.EditFiles {
		t.Fatal("editing files needs reading them")
	}

	a.SetDefaultPolicy(Policy{MCPs: []string{"myfitnesspal"}})
	_, session := inviteUser(t, a, "newcomer")
	if newcomer, _ := a.Session(session); newcomer.Policy.AllowsMCP("anki") || !newcomer.Policy.AllowsMCP("myfitnesspal") {
		t.Fatalf("new users should start with the default policy: %+v", newcomer.Policy)
	}
}

func TestMigrateTheSingleAccount(t *testing.T) {
	dir := t.TempDir()
	hash, _ := hashPassword(goodPassword)
	oldToken := "dd_old-token"
	jsonfile.Write(filepath.Join(dir, "account.json"), map[string]any{
		"username": "me", "password": hash, "token": fingerprint(oldToken), "created": time.Now(),
	})
	oldSession := "old-session"
	jsonfile.Write(filepath.Join(dir, "sessions.json"), map[string]any{
		fingerprint(oldSession): map[string]any{"created": time.Now(), "expires": time.Now().Add(time.Hour)},
	})

	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.NeedsSetup() {
		t.Fatal("the old account should carry over")
	}
	user, ok := a.Session(oldSession)
	if !ok || user.Username != "me" || !user.IsAdmin() || !user.Host {
		t.Fatalf("the old session should belong to the migrated admin: %+v %v", user, ok)
	}
	if tokenUser, ok := a.TokenUser(oldToken); !ok || tokenUser.ID != user.ID {
		t.Fatal("the old plugin token should keep working")
	}
	if _, err := a.Login("me", goodPassword, ""); err != nil {
		t.Fatal("the old password should keep working")
	}
	if _, err := os.Stat(filepath.Join(dir, "account.json")); !os.IsNotExist(err) {
		t.Fatal("the old file should be set aside")
	}
}

func TestGuardTellsHandlersTheUser(t *testing.T) {
	a, _, _ := setUp(t)
	_, session := inviteUser(t, a, "friend")
	var seen User
	handler := a.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen, _ = UserFrom(r) }))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Cookie", SessionCookie+"="+session)
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if seen.Username != "friend" {
		t.Fatalf("handler saw %+v", seen)
	}

	admin := RequireAdmin(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	w := httptest.NewRecorder()
	a.Guard(admin).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a regular user got %d from an admin page", w.Code)
	}
}

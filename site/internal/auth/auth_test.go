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
	if !a.ValidSession(session) {
		t.Fatal("setup should sign the browser in")
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
	token, _ := a.NewToken()
	for _, name := range []string{"account.json", "sessions.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want owner-only", name, info.Mode().Perm())
		}
		data, _ := os.ReadFile(filepath.Join(dir, name))
		for _, secret := range []string{goodPassword, session, token} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds a secret", name)
			}
		}
	}
}

func TestLogin(t *testing.T) {
	a, dir, _ := setUp(t)
	if _, err := a.Login("me", "wrong password!"); err == nil {
		t.Fatal("a wrong password should fail")
	}
	if _, err := a.Login("you", goodPassword); err == nil {
		t.Fatal("a wrong username should fail")
	}
	session, err := a.Login("me", goodPassword)
	if err != nil || !a.ValidSession(session) {
		t.Fatalf("login failed: %v", err)
	}

	reloaded, err := New(dir)
	if err != nil || !reloaded.ValidSession(session) || reloaded.NeedsSetup() {
		t.Fatal("the account and sessions should survive a restart")
	}
	if _, err := reloaded.Login("me", goodPassword); err != nil {
		t.Fatal("the password should still work after a restart")
	}
}

func TestLoginSlowsDownGuessing(t *testing.T) {
	a, _, _ := setUp(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	for range freeLoginTries {
		if _, err := a.Login("me", "guess guess guess"); errors.As(err, new(TooManyTries)) {
			t.Fatal("the first tries shouldn't wait")
		}
	}
	if _, err := a.Login("me", goodPassword); !errors.As(err, new(TooManyTries)) {
		t.Fatal("even the right password should wait after too many wrong ones")
	}
	now = now.Add(2 * time.Second)
	if _, err := a.Login("me", goodPassword); err != nil {
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
	other, _ := a.Login("me", goodPassword)

	if err := a.LogoutOthers(session); err != nil || a.ValidSession(other) || !a.ValidSession(session) {
		t.Fatal("logging out others should keep only this session")
	}
	a.Logout(session)
	if a.ValidSession(session) {
		t.Fatal("logout should end the session")
	}

	fresh, _ := a.Login("me", goodPassword)
	now = now.Add(SessionLength)
	if a.ValidSession(fresh) {
		t.Fatal("sessions should expire")
	}
	if a.ValidSession("") || a.ValidSession("made-up") {
		t.Fatal("unknown sessions aren't valid")
	}
}

func TestChangePassword(t *testing.T) {
	a, _, session := setUp(t)
	other, _ := a.Login("me", goodPassword)
	const newPassword = "a different long one"
	if err := a.ChangePassword(session, "wrong current pw", newPassword, newPassword); err == nil {
		t.Fatal("changing needs the current password")
	}
	if err := a.ChangePassword(session, goodPassword, newPassword, newPassword); err != nil {
		t.Fatal(err)
	}
	if !a.ValidSession(session) || a.ValidSession(other) {
		t.Fatal("a new password should end every other session")
	}
	if _, err := a.Login("me", goodPassword); err == nil {
		t.Fatal("the old password should stop working")
	}
	if _, err := a.Login("me", newPassword); err != nil {
		t.Fatal("the new password should work")
	}
}

func TestToken(t *testing.T) {
	a, _, _ := setUp(t)
	if a.ValidToken("") || a.HasToken() {
		t.Fatal("there's no token until one is made")
	}
	first, _ := a.NewToken()
	second, _ := a.NewToken()
	if a.ValidToken(first) || !a.ValidToken(second) {
		t.Fatal("a new token should replace the old one")
	}
	a.RemoveToken()
	if a.ValidToken(second) {
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
	token, _ := a.NewToken()
	cookie := map[string]string{"Cookie": SessionCookie + "=" + session}

	cases := []struct {
		method, path string
		headers      map[string]string
		want         int
	}{
		{"GET", "/", nil, http.StatusSeeOther},
		{"GET", "/login", nil, http.StatusTeapot},
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

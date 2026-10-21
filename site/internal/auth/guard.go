package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"dashboard/internal/httpx"
)

type userKey struct{}

// credential is what let a request in: its session cookie or plugin token. The workspace
// unlocks the user's encrypted data with it (see workspace.Manager.Unlock).
type credential struct{ kind, secret string }

type credentialKey struct{}

// Kinds of credential.
const (
	BySession = "session"
	ByToken   = "token"
)

// CredentialFrom is how the request's user signed in ("session" or "token") and the secret.
func CredentialFrom(r *http.Request) (kind, secret string) {
	c, _ := r.Context().Value(credentialKey{}).(credential)
	return c.kind, c.secret
}

// UserFrom is the signed-in user of a request that went through Guard. Handlers behind Guard
// always have one; ok is false only on the public pages.
func UserFrom(r *http.Request) (User, bool) {
	user, ok := r.Context().Value(userKey{}).(User)
	return user, ok
}

func withUser(r *http.Request, user User, kind, secret string) *http.Request {
	ctx := context.WithValue(r.Context(), userKey{}, user)
	return r.WithContext(context.WithValue(ctx, credentialKey{}, credential{kind, secret}))
}

// RequireAdmin refuses the request unless the signed-in user is an admin.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, ok := UserFrom(r); !ok || !user.IsAdmin() {
			http.Error(w, "only admins can do that", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// SessionCookie is the cookie's name: "__Host-session" when people reach the dashboard over
// HTTPS (UseSecureCookies), which browsers only accept from a secure page for exactly this host.
var (
	SessionCookie = "session"
	secureCookies bool
)

// UseSecureCookies marks the session cookie Secure, for a dashboard reached over HTTPS.
func UseSecureCookies() {
	SessionCookie, secureCookies = "__Host-session", true
}

// SessionFrom is the request's session cookie ("" when there's none).
func SessionFrom(r *http.Request) string {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// SetSessionCookie gives the browser its session. HttpOnly keeps it out of page scripts and
// SameSite=Strict out of requests other sites start.
func SetSessionCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: secret, Path: "/", Secure: secureCookies,
		MaxAge: int(SessionLength / time.Second), HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", Secure: secureCookies, MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// tokenFrom is the token in "Authorization: Bearer <token>".
func tokenFrom(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// isPublic is what can be seen signed out: the login, setup and invite pages and the styles
// they use.
func isPublic(path string) bool {
	return path == "/login" || path == "/setup" || path == "/invite" || strings.HasPrefix(path, "/static/")
}

// Guard lets a request through when it's public, has a session, or (for /api/) has a plugin
// token, and tells the handlers whose request it is (UserFrom). Otherwise pages go to the
// login (or setup) page and the API answers 401.
func (a *Auth) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if user, ok := a.Session(SessionFrom(r)); ok {
			next.ServeHTTP(w, withUser(r, user, BySession, SessionFrom(r)))
			return
		}
		switch {
		case isPublic(path):
			next.ServeHTTP(w, r)
			return
		case strings.HasPrefix(path, "/api/"):
			if user, ok := a.TokenUser(tokenFrom(r)); ok {
				next.ServeHTTP(w, withUser(r, user, ByToken, tokenFrom(r)))
				return
			}
			httpx.WriteError(w, http.StatusUnauthorized, "log in to the dashboard, or set the plugin's token")
			return
		}

		target := "/login"
		if a.NeedsSetup() {
			target = "/setup"
		}
		if r.Header.Get("HX-Request") == "true" {
			// HTMX swaps nothing on a 401 but follows HX-Redirect, so an expired session
			// takes the whole page to the login.
			w.Header().Set("HX-Redirect", target)
			http.Error(w, "Your session ended: log in again", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		http.Error(w, "log in first", http.StatusUnauthorized)
	})
}

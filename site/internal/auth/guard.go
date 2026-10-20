package auth

import (
	"net/http"
	"strings"
	"time"

	"dashboard/internal/httpx"
)

const SessionCookie = "session"

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
		Name: SessionCookie, Value: secret, Path: "/",
		MaxAge: int(SessionLength / time.Second), HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// tokenFrom is the token in "Authorization: Bearer <token>".
func tokenFrom(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// isPublic is what can be seen signed out: the login and setup pages and the styles they use.
func isPublic(path string) bool {
	return path == "/login" || path == "/setup" || strings.HasPrefix(path, "/static/")
}

// Guard lets a request through when it's public, has a session, or (for /api/) has the
// plugin's token. Otherwise pages go to the login (or setup) page and the API answers 401.
func (a *Auth) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case isPublic(path), a.ValidSession(SessionFrom(r)):
			next.ServeHTTP(w, r)
			return
		case strings.HasPrefix(path, "/api/"):
			if a.ValidToken(tokenFrom(r)) {
				next.ServeHTTP(w, r)
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

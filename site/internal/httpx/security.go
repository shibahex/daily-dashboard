package httpx

import (
	"net/http"
	"strings"
)

// contentPolicy lets the page run only the scripts the dashboard serves itself (including its
// pinned copies of htmx, marked and DOMPurify), connect only back to the dashboard, to
// Anthropic's API, to a model on the user's own computer and to the model servers an admin
// listed (the AI runs in the browser, web/js/assistant.js; nothing else on their network is
// reachable), and never be framed.
// No other site may run scripts here: an injected <script src> can't load, which matters because
// the page holds the key to the user's notes.
// Inline styles stay allowed: templates set widths and colours with style="".
func contentPolicy(modelServers []string) string {
	connect := append([]string{"'self'", "https://api.anthropic.com", "http://localhost:*", "http://127.0.0.1:*"}, modelServers...)
	return "default-src 'self'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src " + strings.Join(connect, " ") + "; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// Secure adds security headers to every answer, and refuses changes (POST…) that another
// origin started. Unlike SameSite cookies, that also covers other servers on localhost.
// modelServers are the origins an admin allowed (auth.ModelServerOrigin checked them).
func Secure(modelServers func() []string, next http.Handler) http.Handler {
	crossOrigin := http.NewCrossOriginProtection()
	protected := crossOrigin.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentPolicy(modelServers()))
		header.Set("X-Frame-Options", "DENY")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		protected.ServeHTTP(w, r)
	})
}

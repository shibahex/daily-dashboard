package httpx

import "net/http"

// contentPolicy lets the page run only its own script and HTMX from jsDelivr (pinned by
// integrity hashes in page.html), connect only back to the dashboard, and never be framed.
// Inline styles stay allowed: templates set widths and colours with style="".
const contentPolicy = "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Secure adds security headers to every answer, and refuses changes (POST…) that another
// origin started. Unlike SameSite cookies, that also covers other servers on localhost.
func Secure(next http.Handler) http.Handler {
	crossOrigin := http.NewCrossOriginProtection()
	protected := crossOrigin.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentPolicy)
		header.Set("X-Frame-Options", "DENY")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		protected.ServeHTTP(w, r)
	})
}

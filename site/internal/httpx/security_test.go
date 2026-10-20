package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecureRefusesOtherOrigins(t *testing.T) {
	handler := Secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []struct {
		name, method, fetchSite string
		want                    int
	}{
		{"same page", "POST", "same-origin", http.StatusOK},
		{"another website", "POST", "cross-site", http.StatusForbidden},
		{"another localhost port", "POST", "same-site", http.StatusForbidden},
		{"the bar plugin (no browser headers)", "POST", "", http.StatusOK},
		{"reading from another website", "GET", "cross-site", http.StatusOK},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://localhost:1628/tasks", nil)
		if c.fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", c.fetchSite)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: security headers missing", c.name)
		}
	}
}

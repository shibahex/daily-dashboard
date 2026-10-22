package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestSecureRefusesOtherOrigins(t *testing.T) {
	handler := Secure(func() []string { return []string{"https://models.example.lan"} }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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
		if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "http://localhost:* http://127.0.0.1:* https://models.example.lan;") {
			t.Errorf("%s: security headers missing", c.name)
		}
	}
}

func TestOnlyHosts(t *testing.T) {
	handler := OnlyHosts([]string{"localhost:1628", "health.example.lan"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for host, want := range map[string]int{
		"localhost:1628": http.StatusOK, "Health.Example.LAN": http.StatusOK,
		"evil.example": http.StatusForbidden, "192.168.1.5:1628": http.StatusForbidden, "localhost:9999": http.StatusForbidden,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestRealIPOnlyBelievesTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/24")}
	var seen string
	handler := RealIP(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = ClientIP(r) }))
	cases := []struct{ peer, forwarded, want string }{
		{"127.0.0.1:5000", "203.0.113.7", "203.0.113.7"},
		{"127.0.0.1:5000", "6.6.6.6, 203.0.113.7, 10.0.0.2", "203.0.113.7"}, // the client wrote 6.6.6.6 itself
		{"198.51.100.9:5000", "203.0.113.7", "198.51.100.9"},                // not through a proxy: ignore the header
		{"127.0.0.1:5000", "", "127.0.0.1"},
		{"127.0.0.1:5000", "not-an-ip", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-For", c.forwarded)
		}
		handler.ServeHTTP(httptest.NewRecorder(), r)
		if seen != c.want {
			t.Errorf("%s via %q: got %s, want %s", c.peer, c.forwarded, seen, c.want)
		}
	}
}

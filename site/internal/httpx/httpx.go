// Package httpx has the HTTP helpers the JSON API and the pages share.
package httpx

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// OnlyHosts refuses requests whose Host isn't one of allowed (localhost on this port and the
// public address), so a website can't reach the dashboard through some other name that points
// at it (DNS rebinding).
func OnlyHosts(allowed []string, next http.Handler) http.Handler {
	known := map[string]bool{}
	for _, host := range allowed {
		known[strings.ToLower(host)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !known[strings.ToLower(r.Host)] {
			WriteError(w, http.StatusForbidden, "bad host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RealIP sets the request's address to the client's when it came through a trusted proxy:
// the last address in X-Forwarded-For that isn't one of the proxies. Anything else in that
// header could have been written by the client, so it's ignored.
func RealIP(trusted []netip.Prefix, next http.Handler) http.Handler {
	isTrusted := func(addr netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(addr.Unmap()) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddr(ClientIP(r))
		forwarded := r.Header.Values("X-Forwarded-For")
		if err == nil && isTrusted(peer) && len(forwarded) > 0 {
			hops := strings.Split(strings.Join(forwarded, ","), ",")
			for i := len(hops) - 1; i >= 0; i-- {
				hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil {
					break
				}
				if !isTrusted(hop) || i == 0 {
					r.RemoteAddr = net.JoinHostPort(hop.String(), "0")
					break
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// WriteJSON sends v as JSON. Nothing the dashboard serves should be cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"couldn't encode the answer"}`)
	}
	WriteBytes(w, status, data, "application/json")
}

func WriteBytes(w http.ResponseWriter, status int, data []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(data)
}

func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// ClientIP is the address the request came from.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

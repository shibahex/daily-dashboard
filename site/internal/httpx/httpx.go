// Package httpx has the HTTP helpers the JSON API and the pages share.
package httpx

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

// OnlyLocalHost refuses requests whose Host isn't this server on this port, so a website can't
// reach the dashboard through a DNS name that points at 127.0.0.1.
func OnlyLocalHost(port int, next http.Handler) http.Handler {
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			WriteError(w, http.StatusForbidden, "bad host")
			return
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

package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
)

// The web app manifest and service worker let Chrome (Android and desktop) and Samsung Internet
// install the dashboard as an app: its own icon and window, opening on Today. Both are public,
// since browsers fetch the manifest without the session cookie.

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

var manifest, _ = json.Marshal(map[string]any{
	"id":               "/",
	"name":             "Daily Dashboard",
	"short_name":       "Dashboard",
	"description":      "Today's plan, food, lifting and tasks.",
	"start_url":        "/",
	"scope":            "/",
	"display":          "standalone",
	"orientation":      "any",
	"background_color": "#f6f5f2",
	"theme_color":      "#2f6f5e",
	"icons": []manifestIcon{
		{Src: "/static/icons/icon-192.png", Sizes: "192x192", Type: "image/png", Purpose: "any"},
		{Src: "/static/icons/icon-512.png", Sizes: "512x512", Type: "image/png", Purpose: "any"},
		{Src: "/static/icons/icon-maskable.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
	},
})

func (ui *UI) webManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(manifest)
}

// serviceWorker serves web/js/sw.js from the root, so it covers every page.
func (ui *UI) serviceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(ui.Paths.Web, "js", "sw.js"))
}

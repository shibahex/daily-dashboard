package web

import "net/http"

// The MCPs tab: its AI runs in the browser (web/js/assistant.js, served by browserai.go).
func (ui *UI) chatPage(w http.ResponseWriter, r *http.Request) {
	ui.renderPage(r, w, "mcps", "MCPs", "browser-chat", nil)
}

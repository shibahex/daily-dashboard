package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/egress"
	"dashboard/internal/mcp"
	"dashboard/internal/problem"
	"dashboard/internal/websearch"
	"dashboard/internal/workspace"
)

// The chat's Web search switch. Claude in the browser searches with Anthropic's own tool
// (assistant.js), and the AIs on the server with their own; a local model gets these two tools,
// which the server runs: web__search asks the admin's SearXNG, web__fetch reads a public page.
// They're listed with the MCPs' tools (/assistant/tools) and called the same way (/assistant/tool).

// webServer is the tools' prefix, like an MCP's name.
const webServer = "web"

// webOn reports whether the user's AI may use the web now: their switch is on and their admin
// allows it.
func webOn(user auth.User, conversation *assistant.Conversation) bool {
	return conversation != nil && conversation.Web && !user.Policy.NoWeb
}

// webTools are the tools there are: reading pages always, searching once a SearXNG is set.
func (ui *UI) webTools() []mcp.Tool {
	var tools []mcp.Tool
	if ui.Auth.SearchURL() != "" {
		tools = append(tools, mcp.Tool{Name: "search",
			Description: "Search the web. Answers the top results: title, address and a snippet. Read a result's page with web__fetch for the details. Never put the user's notes or personal data in a query.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"}},"required":["query"]}`)})
	}
	return append(tools, mcp.Tool{Name: "fetch",
		Description: "Read a public web page (http or https) as plain text, such as a search result. Long pages are cut off.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The page's full address"}},"required":["url"]}`)})
}

// webTool runs web__search or web__fetch and answers its result like an MCP tool's.
func (ui *UI) webTool(w http.ResponseWriter, r *http.Request, user auth.User, ws *workspace.Workspace, tool string) {
	if !webOn(user, ws.Conversations.Load()) {
		fail(w, problem.Forbiddenf("Web search is off: turn it on above the chat"))
		return
	}
	var input struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	if json.Unmarshal([]byte(r.FormValue("input")), &input) != nil {
		fail(w, problem.Invalidf("bad tool input"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var text string
	var err error
	switch tool {
	case "search":
		var results []websearch.Result
		// The SearXNG may be on the local network (the admin chose it), so a plain client.
		results, err = websearch.Search(ctx, &http.Client{Timeout: 30 * time.Second}, ui.Auth.SearchURL(), input.Query)
		text = websearch.Format(results)
	case "fetch":
		proxy := ui.Egress
		if proxy == nil {
			proxy = egress.New()
		}
		text, err = websearch.Fetch(ctx, proxy.Client(30*time.Second), input.URL)
	default:
		fail(w, problem.Invalidf("that tool isn't available"))
		return
	}
	if err != nil {
		writeNoStoreJSON(w, mcp.Result{Text: problem.Public(err), IsError: true})
		return
	}
	writeNoStoreJSON(w, mcp.Result{Text: text})
}

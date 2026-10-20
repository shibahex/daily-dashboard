// Package anki talks to Anki through the AnkiConnect add-on (Anki has to be open).
package anki

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"dashboard/internal/mcp"
)

const (
	AnkiConnectAddon = "2055492159"                      // AnkiConnect's add-on code
	MCPPackage       = "@ankimcp/anki-mcp-server@0.27.0" // pinned Anki MCP server
	defaultURL       = "http://127.0.0.1:8765"
)

// ErrUnreachable means AnkiConnect didn't answer (Anki closed, or the add-on missing).
var ErrUnreachable = errors.New("Anki isn't reachable")

// ActionError is an error AnkiConnect returned for an action.
type ActionError struct{ Message string }

func (e ActionError) Error() string { return e.Message }

type Client struct {
	URL    string
	APIKey string // only if one is set in AnkiConnect's config
	http   *http.Client

	// Gateway is what the agents' Anki MCP talks to instead of AnkiConnect (see Gateway).
	Gateway *Gateway
}

// NewClientFromEnv reads ANKI_CONNECT_URL and ANKI_CONNECT_API_KEY.
func NewClientFromEnv() Client {
	url := os.Getenv("ANKI_CONNECT_URL")
	if url == "" {
		url = defaultURL
	}
	return Client{URL: url, APIKey: os.Getenv("ANKI_CONNECT_API_KEY"), http: &http.Client{Timeout: 30 * time.Second}}
}

// Call runs one AnkiConnect action. params may be nil, a map, or raw JSON.
func (c Client) Call(action string, params any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	request := map[string]any{"action": action, "version": 6, "params": params}
	if c.APIKey != "" {
		request["key"] = c.APIKey
	}
	payload, _ := json.Marshal(request)
	response, err := c.http.Post(c.URL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrUnreachable, err)
	}
	defer response.Body.Close()

	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%w (bad answer: %v)", ErrUnreachable, err)
	}
	if answer.Error != nil {
		return nil, ActionError{Message: *answer.Error}
	}
	return answer.Result, nil
}

// callInto runs an action and decodes its result into v.
func (c Client) callInto(action string, params any, v any) error {
	result, err := c.Call(action, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(result, v)
}

// MCPServer is how an AI agent starts the Anki MCP server. It reaches Anki only through the
// Gateway, so there's none (false) without one. It needs Node 22.12+, so it runs on Nix's
// Node 22 when Nix is there.
func (c Client) MCPServer() (mcp.Server, bool) {
	if c.Gateway == nil {
		return mcp.Server{}, false
	}
	npx := []string{"npx"}
	if _, err := exec.LookPath("nix"); err == nil {
		npx = []string{"nix", "shell", "nixpkgs#nodejs_22", "--command", "npx"}
	}
	return mcp.Server{
		Command: npx[0],
		Args:    append(npx[1:], "-y", MCPPackage, "--stdio"),
		Env:     map[string]string{"ANKI_CONNECT_URL": c.Gateway.URL, "ANKI_CONNECT_API_KEY": c.Gateway.Key},
	}, true
}

// ---------- the Anki app ----------

// AppInstalled reports whether the `anki` command exists.
func AppInstalled() bool { _, err := exec.LookPath("anki"); return err == nil }

// AppRunning reports whether Anki is open (on NixOS its process is ".anki-wrapped").
func AppRunning() bool {
	return exec.Command("pgrep", "-x", `anki|\.anki-wrapped`).Run() == nil
}

// OpenApp starts Anki in the background, detached from the server.
func OpenApp() error {
	return startDetached("anki")
}

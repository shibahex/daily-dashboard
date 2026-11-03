// Package anki talks to Anki through the AnkiConnect add-on: the user's own Anki (which has to
// be open), or their Anki on the dashboard's server (NewServerClient, internal/ankiserver).
package anki

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"dashboard/internal/problem"
)

// AnkiConnectAddon is AnkiConnect's add-on code. The Anki MCP the AI uses is built by Nix
// (site/nix/anki-mcp.nix).
const AnkiConnectAddon = "2055492159"

// ErrUnreachable means AnkiConnect didn't answer (Anki closed, or the add-on missing).
var ErrUnreachable error = problem.Error{Kind: problem.Unavailable, Message: "Anki isn't reachable"}

// ErrNotSetUp means the user hasn't told the dashboard where their AnkiConnect is.
var ErrNotSetUp error = problem.Error{Kind: problem.Unavailable, Message: "Anki isn't reachable: set up your Anki on the Account page"}

// ActionError is an error AnkiConnect returned for an action. People see it (problem.Public).
type ActionError struct{ Message string }

func (e ActionError) Error() string { return e.Message }
func (e ActionError) Unwrap() error { return problem.Failedf(e.Message) }

type Client struct {
	URL    string
	APIKey string // only if one is set in AnkiConnect's config
	Server bool   // the user's Anki on the dashboard's server (NewServerClient)
	// Awake is, for a server Anki, a client for it while it's running that doesn't keep it
	// running (false when it's stopped), for things that only show what's there.
	Awake func() (Client, bool)
	http  *http.Client

	// Gateway is what the agents' Anki MCP talks to instead of AnkiConnect (see Gateway), and
	// GatewayKey the key that sends this user's requests to this AnkiConnect.
	Gateway    *Gateway
	GatewayKey string
}

// NewClient talks to the AnkiConnect at url ("" = not set up yet).
func NewClient(url, apiKey string) Client { return newClient(url, apiKey, false) }

// maxAnswer caps what AnkiConnect may send back: a big cardsInfo is a few MB.
const maxAnswer = 64 << 20

// newClient's connections are checked as they're made, not only when the address was saved: a
// name can later point somewhere else, and redirects aren't followed, so the address can't lead
// to the server's own services or cloud metadata (CheckConnection). serverOK allows the server
// itself, for tests.
func newClient(url, apiKey string, serverOK bool) Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || (!serverOK && forbiddenAddr(addr)) {
			return errServerAddress
		}
		return nil
	}}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second}
	return Client{URL: url, APIKey: apiKey, http: &http.Client{Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Call runs one AnkiConnect action. params may be nil, a map, or raw JSON.
func (c Client) Call(action string, params any) (json.RawMessage, error) {
	if c.URL == "" {
		return nil, ErrNotSetUp
	}
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
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, fmt.Errorf("%w (it answered with a redirect, which isn't followed)", ErrUnreachable)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswer)).Decode(&answer); err != nil {
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

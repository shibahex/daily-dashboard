package auth

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"dashboard/internal/problem"
)

// Model servers are the addresses on the network, besides each person's own computer, that the
// browser's AI may talk to (an Ollama box, say). An admin lists them; they go into the pages'
// Content-Security-Policy, so the page can reach nothing else.

const maxModelServers = 20

// hostPattern is a host name or IPv4 address and an optional port: nothing that could end the
// policy's source list (spaces, ';') or widen it ('*').
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// ModelServerOrigin is the origin of a typed address ("https://box.lan/v1" → "https://box.lan").
// Without a scheme it's https.
func ModelServerOrigin(typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	if !strings.Contains(typed, "://") {
		typed = "https://" + typed
	}
	parsed, err := url.Parse(typed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		!hostPattern.MatchString(strings.ToLower(parsed.Host)) {
		return "", problem.Invalidf(fmt.Sprintf("%q isn't an http:// or https:// address", typed))
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host), nil
}

// ModelServers are the allowed model servers' origins.
func (a *Auth) ModelServers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.state.ModelServers...)
}

// SetModelServers replaces the list with these addresses, one origin each.
func (a *Auth) SetModelServers(typed []string) error {
	var origins []string
	for _, address := range typed {
		if strings.TrimSpace(address) == "" {
			continue
		}
		origin, err := ModelServerOrigin(address)
		if err != nil {
			return err
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	if len(origins) > maxModelServers {
		return problem.Invalidf(fmt.Sprintf("At most %d model servers", maxModelServers))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.ModelServers = origins
	return a.save()
}

// SkipModelCerts is the admin's testing switch: the server relays the browser AI's requests to
// the https model servers and doesn't check their certificates (web/modelrelay.go). People's
// chats and notes then pass through the server.
func (a *Auth) SkipModelCerts() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.SkipModelCerts
}

func (a *Auth) SetSkipModelCerts(skip bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.SkipModelCerts = skip
	return a.save()
}

// RelayedModelServers are the model servers the server relays to without checking certificates:
// the https ones, while SkipModelCerts is on.
func (a *Auth) RelayedModelServers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var relayed []string
	if a.state.SkipModelCerts {
		for _, origin := range a.state.ModelServers {
			if strings.HasPrefix(origin, "https://") {
				relayed = append(relayed, origin)
			}
		}
	}
	return relayed
}

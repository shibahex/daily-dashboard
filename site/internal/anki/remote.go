package anki

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"dashboard/internal/problem"
)

// CheckConnection checks an AnkiConnect address a user wants to use, and returns it cleaned up.
//
// AnkiConnect without an API key lets anyone who can reach it read and change that Anki, so a
// user could otherwise point the dashboard at someone else's open AnkiConnect on the network.
// So the address must answer only with the key. Users other than the host also can't use
// addresses on the server itself (loopback) or link-local ones, which reach the server's own
// services rather than the user's computer.
func CheckConnection(rawURL, apiKey string, host bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", problem.Invalidf("Enter AnkiConnect's address, like http://192.168.1.20:8765")
	}
	cleaned := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	if !host {
		if err := checkReachableHost(parsed.Hostname()); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", problem.Invalidf("Set an API key in AnkiConnect's config (Tools → Add-ons → AnkiConnect → Config: \"apiKey\") and enter it here")
	}
	if _, err := NewClient(cleaned, "").Call("version", nil); err == nil {
		return "", problem.Invalidf("That AnkiConnect answers without an API key, so anyone could use it. Set \"apiKey\" in its config first")
	} else if errors.Is(err, ErrUnreachable) {
		return "", problem.Unavailablef("Couldn't reach AnkiConnect at " + cleaned + ". Is Anki open, and does AnkiConnect listen on the network (\"webBindAddress\": \"0.0.0.0\")?")
	}
	if _, err := NewClient(cleaned, apiKey).Call("version", nil); err != nil {
		return "", problem.Invalidf("AnkiConnect refused that API key")
	}
	return cleaned, nil
}

// checkReachableHost refuses names and addresses that point at the server itself.
func checkReachableHost(hostname string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", hostname)
	if err != nil || len(addrs) == 0 {
		return problem.Invalidf("Couldn't find " + hostname + " on the network")
	}
	for _, addr := range addrs {
		if forbiddenAddr(addr) {
			return problem.Forbiddenf("That address is the server itself. Use your own computer's network address")
		}
	}
	return nil
}

func forbiddenAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsUnspecified() || addr.IsMulticast()
}

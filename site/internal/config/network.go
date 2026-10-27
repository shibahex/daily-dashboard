package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Network is how the dashboard is reached: where it listens, the addresses people use (usually
// through a reverse proxy that handles HTTPS), and which proxies to believe about who's asking.
type Network struct {
	Listen         string         // host:port to listen on
	Scheme         string         // "https" or "http" for the public addresses; "" when it's only used on this computer
	PublicHosts    []string       // health.example.lan…; empty with AnyHost
	AnyHost        bool           // answers at whatever address it's opened at (DASHBOARD_PUBLIC_URL=https://*)
	TrustedProxies []netip.Prefix // peers whose X-Forwarded-For is believed
}

// NetworkFromEnv reads DASHBOARD_LISTEN, DASHBOARD_PUBLIC_URL, DASHBOARD_TRUSTED_PROXIES and
// DASHBOARD_ALLOW_HTTP. port (from the command line) is used when DASHBOARD_LISTEN is unset.
// Listening beyond this computer without HTTPS is refused unless DASHBOARD_ALLOW_HTTP=1:
// passwords and sessions would cross the network in the clear.
func NetworkFromEnv(port int) (Network, error) {
	n := Network{Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	if listen := strings.TrimSpace(os.Getenv("DASHBOARD_LISTEN")); listen != "" {
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return n, fmt.Errorf("DASHBOARD_LISTEN must be host:port, like 0.0.0.0:1628 (%v)", err)
		}
		n.Listen = listen
	}
	// One or more addresses, comma-separated; https://* is any address, for a dashboard behind a
	// proxy that decides which names reach it.
	for _, raw := range strings.Split(os.Getenv("DASHBOARD_PUBLIC_URL"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		public, err := url.Parse(raw)
		if err != nil || (public.Scheme != "https" && public.Scheme != "http") || public.Host == "" {
			return n, fmt.Errorf("DASHBOARD_PUBLIC_URL must be full addresses, like https://health.example.lan (or https://* for any)")
		}
		if n.Scheme != "" && n.Scheme != public.Scheme {
			return n, errors.New("DASHBOARD_PUBLIC_URL's addresses must all be https or all http")
		}
		n.Scheme = public.Scheme
		if public.Host == "*" {
			n.AnyHost = true
		} else {
			n.PublicHosts = append(n.PublicHosts, strings.ToLower(public.Host))
		}
	}
	if n.AnyHost {
		n.PublicHosts = nil
	}
	proxies := os.Getenv("DASHBOARD_TRUSTED_PROXIES")
	if strings.TrimSpace(proxies) == "" {
		proxies = "127.0.0.1/32,::1/128"
	}
	for _, field := range strings.Split(proxies, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			addr, addrErr := netip.ParseAddr(field)
			if addrErr != nil {
				return n, fmt.Errorf("DASHBOARD_TRUSTED_PROXIES: %q isn't an address or CIDR range", field)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		n.TrustedProxies = append(n.TrustedProxies, prefix)
	}
	if !n.LocalOnly() && !n.HTTPS() && os.Getenv("DASHBOARD_ALLOW_HTTP") != "1" {
		return n, errors.New("the dashboard would be reachable from the network without HTTPS. Put it behind a " +
			"reverse proxy with DASHBOARD_PUBLIC_URL=https://…, or set DASHBOARD_ALLOW_HTTP=1 to accept the risk")
	}
	return n, nil
}

// LocalOnly is true when it only listens on this computer (the proxy, if any, runs here too).
func (n Network) LocalOnly() bool {
	host, _, _ := net.SplitHostPort(n.Listen)
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// HTTPS is true when people reach it at an https:// address.
func (n Network) HTTPS() bool { return n.Scheme == "https" }

// AllowedHosts are the Host headers it answers to: localhost on its port, and the public
// addresses. nil with AnyHost: every name is answered.
func (n Network) AllowedHosts() []string {
	if n.AnyHost {
		return nil
	}
	_, port, _ := net.SplitHostPort(n.Listen)
	return append([]string{"localhost:" + port, "127.0.0.1:" + port}, n.PublicHosts...)
}

// BaseURL is the address to print when there's no request to take it from: the first public
// one, a stand-in with AnyHost, or localhost.
func (n Network) BaseURL() string {
	switch {
	case len(n.PublicHosts) > 0:
		return n.Scheme + "://" + n.PublicHosts[0]
	case n.AnyHost:
		return n.Scheme + "://<the dashboard's address>"
	}
	_, port, _ := net.SplitHostPort(n.Listen)
	return "http://localhost:" + port
}

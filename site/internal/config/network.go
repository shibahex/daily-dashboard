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

// Network is how the dashboard is reached: where it listens, the address people use (usually
// through a reverse proxy that handles HTTPS), and which proxies to believe about who's asking.
type Network struct {
	Listen         string         // host:port to listen on
	PublicURL      *url.URL       // https://health.example.lan; nil when it's only used on this computer
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
	if raw := strings.TrimSpace(os.Getenv("DASHBOARD_PUBLIC_URL")); raw != "" {
		public, err := url.Parse(raw)
		if err != nil || (public.Scheme != "https" && public.Scheme != "http") || public.Host == "" {
			return n, fmt.Errorf("DASHBOARD_PUBLIC_URL must be a full address, like https://health.example.lan")
		}
		n.PublicURL = &url.URL{Scheme: public.Scheme, Host: public.Host}
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
func (n Network) HTTPS() bool { return n.PublicURL != nil && n.PublicURL.Scheme == "https" }

// AllowedHosts are the Host headers it answers to: localhost on its port, and the public address.
func (n Network) AllowedHosts() []string {
	_, port, _ := net.SplitHostPort(n.Listen)
	hosts := []string{"localhost:" + port, "127.0.0.1:" + port}
	if n.PublicURL != nil {
		hosts = append(hosts, n.PublicURL.Host)
	}
	return hosts
}

// BaseURL is the address to print and put in links: the public one, or localhost.
func (n Network) BaseURL() string {
	if n.PublicURL != nil {
		return n.PublicURL.String()
	}
	_, port, _ := net.SplitHostPort(n.Listen)
	return "http://localhost:" + port
}

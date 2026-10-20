// Package egress is the only way out to the network for a sandboxed AI agent: an HTTP proxy
// (CONNECT for HTTPS, plain requests for HTTP) that reaches public addresses on the web ports
// and nothing else. Not the server itself, not the local network, not cloud metadata: so an
// agent can search the web, call its AI provider and use MyFitnessPal, but can't reach the
// dashboard, other people's AnkiConnect, or anything else on the LAN.
//
// It resolves names itself and connects to the address it checked, so a name can't resolve to
// a public address for the check and a private one for the connection.
package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// AllowedPorts are the ports an agent may connect to.
var AllowedPorts = map[int]bool{80: true, 443: true}

// blocked are the ranges that aren't the public internet.
var blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/3",
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(cidrs))
	for i, cidr := range cidrs {
		prefixes[i] = netip.MustParsePrefix(cidr)
	}
	return prefixes
}

// Public reports whether addr is on the public internet.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range blocked {
		if prefix.Contains(addr) {
			return false
		}
	}
	return addr.IsValid()
}

// Proxy answers proxy requests. Lookup is how names are resolved (net.DefaultResolver unless a
// test sets it).
type Proxy struct {
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Allowed func(netip.Addr) bool // Public, unless a test needs a local server
	dialer  net.Dialer
}

func New() *Proxy {
	return &Proxy{
		Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		Allowed: Public,
	}
}

// Serve answers requests on listener until it's closed (one per agent run, on a unix socket).
func (p *Proxy) Serve(listener net.Listener) error {
	server := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	return server.Serve(listener)
}

// target checks hostport and returns the address to connect to.
func (p *Proxy) target(ctx context.Context, hostport string) (string, error) {
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", fmt.Errorf("bad address %q", hostport)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || !AllowedPorts[port] {
		return "", fmt.Errorf("port %s isn't allowed", portText)
	}
	var addrs []netip.Addr
	if addr, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{addr}
	} else {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if addrs, err = p.Lookup(lookupCtx, host); err != nil || len(addrs) == 0 {
			return "", fmt.Errorf("can't resolve %s", host)
		}
	}
	for _, addr := range addrs {
		if !p.Allowed(addr) {
			// One private answer is enough to refuse: the name is meant for inside the network.
			return "", fmt.Errorf("%s isn't on the public internet", host)
		}
	}
	return net.JoinHostPort(addrs[0].Unmap().String(), portText), nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.forward(w, r)
}

// tunnel answers CONNECT: an HTTPS (or other TLS) connection the proxy only passes through.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	address, err := p.target(r.Context(), r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	upstream, err := p.dialer.DialContext(r.Context(), "tcp", address)
	if err != nil {
		http.Error(w, "can't connect", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "can't tunnel", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	pipe(client, buffered, upstream)
}

func pipe(client net.Conn, buffered *bufio.ReadWriter, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if n := buffered.Reader.Buffered(); n > 0 {
			data, _ := buffered.Reader.Peek(n)
			upstream.Write(data)
		}
		io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
	client.Close()
	upstream.Close()
}

func closeWrite(conn net.Conn) {
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		c.CloseWrite()
	}
}

// hopHeaders are for this connection only, never passed on.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward answers a plain-HTTP proxy request (absolute URL).
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "only proxy requests", http.StatusBadRequest)
		return
	}
	hostport := r.URL.Host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(hostport, "80")
	}
	address, err := p.target(r.Context(), hostport)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dialer.DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "can't connect", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

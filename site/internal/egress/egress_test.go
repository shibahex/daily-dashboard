package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"1.1.1.1": true, "142.250.72.14": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.1.20": false, "172.20.0.1": false, "169.254.169.254": false,
		"100.100.1.1": false, "0.0.0.0": false, "::1": false, "fd00::1": false, "fe80::1": false, "::ffff:127.0.0.1": false,
		"2002:a00:1::1": false, "64:ff9b:1::a00:1": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func lookupTable(table map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		var addrs []netip.Addr
		for _, a := range table[host] {
			addrs = append(addrs, netip.MustParseAddr(a))
		}
		if addrs == nil {
			return nil, fmt.Errorf("no such host")
		}
		return addrs, nil
	}
}

func TestTargetRefusesWhatIsntPublicWeb(t *testing.T) {
	p := New()
	p.Lookup = lookupTable(map[string][]string{
		"example.com":   {"93.184.215.14"},
		"sneaky.test":   {"93.184.215.14", "10.0.0.5"},
		"rebind.test":   {"127.0.0.1"},
		"metadata.test": {"169.254.169.254"},
	})
	if got, err := p.target(context.Background(), "example.com:443"); err != nil || got != "93.184.215.14:443" {
		t.Fatalf("a public name should be allowed and pinned to its address: %q %v", got, err)
	}
	for _, refused := range []string{"example.com:22", "example.com:8765", "rebind.test:443", "sneaky.test:443",
		"metadata.test:80", "127.0.0.1:443", "[::1]:443", "192.168.1.20:80", "nowhere.test:443", "example.com"} {
		if _, err := p.target(context.Background(), refused); err == nil {
			t.Errorf("%s should be refused", refused)
		}
	}
}

// serveProxy starts the proxy on a unix socket, the way a sandbox reaches it.
func serveProxy(t *testing.T, p *Proxy) string {
	socket := t.TempDir() + "/egress.sock"
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(listener)
	t.Cleanup(func() { listener.Close() })
	return socket
}

func TestTunnelAndForward(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from "+r.URL.Path) }))
	defer site.Close()
	sitePort := site.Listener.Addr().(*net.TCPAddr).Port
	AllowedPorts[sitePort] = true
	defer delete(AllowedPorts, sitePort)

	p := New()
	p.Allowed = func(netip.Addr) bool { return true } // the test site is on loopback
	socket := serveProxy(t, p)
	dial := func() net.Conn {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}

	conn := dial()
	fmt.Fprintf(conn, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", sitePort, sitePort)
	reader := bufio.NewReader(conn)
	status, _ := reader.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT: %q", status)
	}
	reader.ReadString('\n')
	fmt.Fprintf(conn, "GET /tunnel HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	body, _ := io.ReadAll(reader)
	if !strings.Contains(string(body), "hello from /tunnel") {
		t.Fatalf("through the tunnel: %q", body)
	}
	conn.Close()

	conn = dial()
	fmt.Fprintf(conn, "GET http://127.0.0.1:%d/plain HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", sitePort, sitePort)
	body, _ = io.ReadAll(conn)
	if !strings.Contains(string(body), "hello from /plain") {
		t.Fatalf("plain HTTP: %q", body)
	}
	conn.Close()

	strict := New()
	conn, _ = net.Dial("unix", serveProxy(t, strict))
	fmt.Fprintf(conn, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", sitePort, sitePort)
	status, _ = bufio.NewReader(conn).ReadString('\n')
	if !strings.Contains(status, "403") {
		t.Fatalf("the real proxy must refuse loopback: %q", status)
	}
	conn.Close()
}

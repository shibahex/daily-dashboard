package config

import "testing"

func TestNetworkFromEnv(t *testing.T) {
	clear := func() {
		for _, key := range []string{"DASHBOARD_LISTEN", "DASHBOARD_PUBLIC_URL", "DASHBOARD_TRUSTED_PROXIES", "DASHBOARD_ALLOW_HTTP"} {
			t.Setenv(key, "")
		}
	}

	clear()
	n, err := NetworkFromEnv(1628)
	if err != nil || n.Listen != "127.0.0.1:1628" || !n.LocalOnly() || n.HTTPS() || n.BaseURL() != "http://localhost:1628" {
		t.Fatalf("default: %+v %v", n, err)
	}

	clear()
	t.Setenv("DASHBOARD_LISTEN", "0.0.0.0:1628")
	if _, err := NetworkFromEnv(1628); err == nil {
		t.Fatal("listening on the network without HTTPS should be refused")
	}
	t.Setenv("DASHBOARD_ALLOW_HTTP", "1")
	if _, err := NetworkFromEnv(1628); err != nil {
		t.Fatalf("DASHBOARD_ALLOW_HTTP=1 should allow it: %v", err)
	}

	clear()
	t.Setenv("DASHBOARD_LISTEN", "0.0.0.0:1628")
	t.Setenv("DASHBOARD_PUBLIC_URL", "https://health.example.lan/some/path")
	t.Setenv("DASHBOARD_TRUSTED_PROXIES", "10.0.0.5, 192.168.0.0/16")
	n, err = NetworkFromEnv(1628)
	if err != nil || !n.HTTPS() || n.BaseURL() != "https://health.example.lan" || len(n.TrustedProxies) != 2 {
		t.Fatalf("behind a proxy: %+v %v", n, err)
	}
	hosts := n.AllowedHosts()
	if hosts[len(hosts)-1] != "health.example.lan" {
		t.Errorf("the public host should be allowed: %v", hosts)
	}

	clear()
	t.Setenv("DASHBOARD_LISTEN", "0.0.0.0:1628")
	t.Setenv("DASHBOARD_PUBLIC_URL", "https://a.example.lan, https://B.example.lan")
	n, err = NetworkFromEnv(1628)
	if hosts := n.AllowedHosts(); err != nil || len(hosts) != 4 || hosts[3] != "b.example.lan" || n.BaseURL() != "https://a.example.lan" {
		t.Errorf("several addresses: %v %v", hosts, err)
	}

	clear()
	t.Setenv("DASHBOARD_LISTEN", "0.0.0.0:1628")
	t.Setenv("DASHBOARD_PUBLIC_URL", "https://*")
	n, err = NetworkFromEnv(1628)
	if err != nil || !n.HTTPS() || !n.AnyHost || n.AllowedHosts() != nil {
		t.Errorf("any address: %+v %v", n, err)
	}

	clear()
	t.Setenv("DASHBOARD_PUBLIC_URL", "https://a.example.lan,http://b.example.lan")
	if _, err := NetworkFromEnv(1628); err == nil {
		t.Error("mixing https and http should be refused")
	}

	clear()
	t.Setenv("DASHBOARD_PUBLIC_URL", "health.example.lan")
	if _, err := NetworkFromEnv(1628); err == nil {
		t.Error("a public URL without a scheme should be refused")
	}
	clear()
	t.Setenv("DASHBOARD_TRUSTED_PROXIES", "not-a-range")
	if _, err := NetworkFromEnv(1628); err == nil {
		t.Error("a bad proxy range should be refused")
	}
}

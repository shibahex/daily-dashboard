// Command dashboard serves the health dashboard on http://localhost:<port> (default 1628).
//
// Run it from the site/ folder (or set DASHBOARD_SITE): it reads and writes the files there.
// See docs/dashboard.md.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/api"
	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/httpx"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/secrets"
	"dashboard/internal/web"
	"dashboard/internal/workspace"
)

const defaultPort = 1628

func main() {
	port := portFromArgs()
	siteDir := findSiteDir()
	config.LoadEnvFile(filepath.Join(siteDir, ".env"))
	network, err := config.NetworkFromEnv(port)
	if err != nil {
		log.Fatal(err)
	}
	if network.HTTPS() {
		auth.UseSecureCookies()
	}
	paths := config.NewPaths(siteDir)
	paths.MakePrivate()

	account, err := auth.New(paths.ServerData)
	if err != nil {
		log.Fatal(err)
	}
	gateway, err := anki.StartGateway()
	if err != nil {
		log.Fatalf("starting the Anki gateway: %v", err)
	}
	box, err := secrets.LoadBox(filepath.Join(paths.ServerData, "secret.key"))
	if err != nil {
		log.Fatalf("loading the secrets key: %v", err)
	}
	providers := assistant.NewProviders(assistant.NewModelCatalog(filepath.Join(paths.ServerData, "assistant")))
	workspaces := workspace.NewManager(paths,
		myfitnesspal.NewClientFromEnv(filepath.Join(siteDir, "mfp_fetch.py")),
		anki.NewClientFromEnv(), gateway, providers, box)
	workspaces.OpenAll(account.Users())

	mux := http.NewServeMux()
	jsonAPI := &api.API{Workspaces: workspaces}
	jsonAPI.Routes(mux)
	pages := &web.UI{Auth: account, Paths: paths, Workspaces: workspaces, Providers: providers, BaseURL: network.BaseURL()}
	pages.Routes(mux)

	listener := listen(network.Listen)
	fmt.Printf("Health dashboard: %s  (listening on %s, %s)\n", network.BaseURL(), network.Listen, time.Now().Format("2006-01-02"))
	if code := account.SetupCode(); code != "" {
		fmt.Printf("Create the first (admin) account: %s/setup?code=%s\n", network.BaseURL(), code)
	}
	server := &http.Server{
		Handler:           httpx.RealIP(network.TrustedProxies, httpx.OnlyHosts(network.AllowedHosts(), httpx.Secure(account.Guard(mux)))),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.Serve(listener))
}

func portFromArgs() int {
	if len(os.Args) < 2 {
		return defaultPort
	}
	port, err := strconv.Atoi(os.Args[1])
	if err != nil || port < 1 || port > 65535 {
		log.Fatalf("bad port %q", os.Args[1])
	}
	return port
}

// findSiteDir is $DASHBOARD_SITE, or the current folder; it must contain the page templates.
func findSiteDir() string {
	dir := os.Getenv("DASHBOARD_SITE")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir, _ = filepath.Abs(dir)
	if _, err := os.Stat(filepath.Join(dir, "web", "templates", "page.html")); err != nil {
		log.Fatal("Run this from the site/ folder (cd there, then: nix run), or set DASHBOARD_SITE")
	}
	return dir
}

// listen binds the address (127.0.0.1 unless DASHBOARD_LISTEN says otherwise). After a restart
// the old socket can take a moment to free up, so it retries for a few seconds.
func listen(address string) net.Listener {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			return listener
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	log.Fatalf("%s is busy (%v). Is the dashboard already running? Try: ./run.sh <other-port>", address, lastErr)
	return nil
}

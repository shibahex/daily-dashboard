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
	"dashboard/internal/assistant"
	"dashboard/internal/config"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/server"
	"dashboard/internal/store"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

const defaultPort = 1628

func main() {
	port := portFromArgs()
	siteDir := findSiteDir()
	config.LoadEnvFile(filepath.Join(siteDir, ".env"))
	paths := config.NewPaths(siteDir)

	files := store.New(paths)
	files.SeedDocs()
	mfp := myfitnesspal.NewSyncer(
		myfitnesspal.NewClientFromEnv(filepath.Join(siteDir, "mfp_fetch.py")),
		files,
		paths.DataFile("mfp.json"),
	)
	providers := assistant.NewProviders(assistant.NewModelCatalog(filepath.Join(paths.Data, "assistant")))

	dashboard := &server.Server{
		Port:          port,
		Paths:         paths,
		Store:         files,
		Tasks:         taskwarrior.NewClient(),
		Time:          timewarrior.NewClient(),
		MyFitnessPal:  mfp,
		Anki:          anki.NewClientFromEnv(),
		Providers:     providers,
		Conversations: assistant.NewConversations(paths.DataFile("chat.json"), providers),
		Runner:        assistant.NewRunner(),
	}

	listener := listen(port)
	go mfp.RunForever()
	fmt.Printf("Health dashboard: http://localhost:%d  (%s)\n", port, time.Now().Format("2006-01-02"))
	log.Fatal(http.Serve(listener, dashboard.Handler()))
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

// findSiteDir is $DASHBOARD_SITE, or the current folder; it must contain the page.
func findSiteDir() string {
	dir := os.Getenv("DASHBOARD_SITE")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir, _ = filepath.Abs(dir)
	if _, err := os.Stat(filepath.Join(dir, "web", "index.html")); err != nil {
		log.Fatal("Run this from the site/ folder (cd there, then: nix run), or set DASHBOARD_SITE")
	}
	return dir
}

// listen binds 127.0.0.1 only. After a restart the old socket can take a moment to free up,
// so it retries for a few seconds.
func listen(port int) net.Listener {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			return listener
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	log.Fatalf("Port %d is busy (%v). Is the dashboard already running? Try: ./run.sh <other-port>", port, lastErr)
	return nil
}

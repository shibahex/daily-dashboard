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
	"dashboard/internal/store"
	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
	"dashboard/internal/web"
)

const defaultPort = 1628

func main() {
	port := portFromArgs()
	siteDir := findSiteDir()
	config.LoadEnvFile(filepath.Join(siteDir, ".env"))
	paths := config.NewPaths(siteDir)
	paths.MakePrivate()

	account, err := auth.New(paths.Data)
	if err != nil {
		log.Fatal(err)
	}
	files := store.New(paths)
	files.SeedDocs()
	mfp := myfitnesspal.NewSyncer(
		myfitnesspal.NewClientFromEnv(filepath.Join(siteDir, "mfp_fetch.py")),
		files,
		paths.DataFile("mfp.json"),
	)
	ankiClient := anki.NewClientFromEnv()
	ankiClient.Gateway, err = anki.StartGateway(ankiClient)
	if err != nil {
		log.Fatalf("starting the Anki gateway: %v", err)
	}
	providers := assistant.NewProviders(assistant.NewModelCatalog(filepath.Join(paths.Data, "assistant")))
	timeLog := timewarrior.NewClient()
	taskService := tasks.NewService(taskwarrior.NewClient(), timeLog)

	mux := http.NewServeMux()
	jsonAPI := &api.API{Store: files, Tasks: taskService, Time: timeLog}
	jsonAPI.Routes(mux)
	pages := &web.UI{
		Auth:          account,
		Paths:         paths,
		Store:         files,
		Tasks:         taskService,
		Time:          timeLog,
		MyFitnessPal:  mfp,
		Anki:          ankiClient,
		Providers:     providers,
		Conversations: assistant.NewConversations(paths.DataFile("chat.json"), providers),
		Chat:          assistant.NewRunner(),
	}
	pages.Routes(mux)

	listener := listen(port)
	go mfp.RunForever()
	fmt.Printf("Health dashboard: http://localhost:%d  (%s)\n", port, time.Now().Format("2006-01-02"))
	if code := account.SetupCode(); code != "" {
		fmt.Printf("Create your account: http://localhost:%d/setup?code=%s\n", port, code)
	}
	server := &http.Server{
		Handler:           httpx.OnlyLocalHost(port, httpx.Secure(account.Guard(mux))),
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

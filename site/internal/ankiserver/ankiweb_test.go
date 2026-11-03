package ankiserver

import (
	"os"
	"strings"
	"testing"

	"dashboard/internal/egress"
	"dashboard/internal/sandbox"
)

// TestAnkiReachesAnkiWebThroughTheProxy signs in with an address that doesn't exist: AnkiWeb
// saying so shows Anki gets out of its sandbox, through the egress proxy only. It talks to
// AnkiWeb, so it runs only with DASHBOARD_TEST_ANKIWEB=1.
func TestAnkiReachesAnkiWebThroughTheProxy(t *testing.T) {
	if os.Getenv("DASHBOARD_ANKI") == "" || os.Getenv("DASHBOARD_TEST_ANKIWEB") != "1" {
		t.Skip("set DASHBOARD_ANKI and DASHBOARD_TEST_ANKIWEB=1 to try AnkiWeb")
	}
	tools, err := sandbox.Find()
	if err != nil {
		t.Skip(err)
	}
	runner := New(os.Getenv("DASHBOARD_ANKI"), tools, egress.New())
	defer runner.StopAll()
	client := runner.Client("abc123", t.TempDir())
	_, err = client.SignIn("nobody@example.invalid", "not-a-password")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("want AnkiWeb's wrong-password answer, got %v", err)
	}
}

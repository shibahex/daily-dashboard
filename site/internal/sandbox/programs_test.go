package sandbox

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestInstallPrograms really installs both MCP servers (network, a minute or two), so it only
// runs with DASHBOARD_TEST_INSTALL=1.
func TestInstallPrograms(t *testing.T) {
	if os.Getenv("DASHBOARD_TEST_INSTALL") != "1" {
		t.Skip("set DASHBOARD_TEST_INSTALL=1 to install the MCP servers")
	}
	p := &Programs{Dir: t.TempDir(), MFPPackage: "mfp-mcp==0.4.1", AnkiPackage: "@ankimcp/anki-mcp-server@0.27.0"}
	mfp, err := p.MyFitnessPal()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(mfp.Command, "--help").CombinedOutput(); err != nil && !strings.Contains(string(out), "usage") {
		t.Fatalf("mfp-mcp --help: %v %s", err, out)
	}
	anki, err := p.Anki()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(anki.Command, append(anki.Args[:1], "--help")...).CombinedOutput(); err != nil {
		t.Fatalf("anki mcp --help: %v %s", err, out)
	}
	if again, _ := p.MyFitnessPal(); again.Command != mfp.Command {
		t.Fatal("a second call should reuse the install")
	}
}

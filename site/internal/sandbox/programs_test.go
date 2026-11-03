package sandbox

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestNixPrograms starts both MCP servers from Nix's builds, which `nix develop` provides.
func TestNixPrograms(t *testing.T) {
	p := &Programs{MFPEnv: os.Getenv("MFP_MCP_SANDBOX_ENV"), AnkiProgram: os.Getenv("ANKI_MCP")}
	if p.MFPEnv == "" || p.AnkiProgram == "" {
		t.Skip("MFP_MCP_SANDBOX_ENV and ANKI_MCP are set by nix develop")
	}
	mfp, err := p.MyFitnessPal()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(mfp.Command, "--help").CombinedOutput(); err != nil || !strings.Contains(string(out), "usage") {
		t.Fatalf("mfp-mcp --help: %v %s", err, out)
	}
	if mfp.Mount.Source != "" || mfp.Args == nil {
		t.Fatalf("a Nix program needs no mount and must have non-nil args: %+v", mfp)
	}
	anki, err := p.Anki()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(anki.Command, "--help").CombinedOutput(); err != nil {
		t.Fatalf("anki mcp --help: %v %s", err, out)
	}
}

func TestProgramsWithoutNixSayHowToStart(t *testing.T) {
	p := &Programs{}
	if _, err := p.MyFitnessPal(); err == nil || !strings.Contains(err.Error(), "nix run") {
		t.Fatalf("MyFitnessPal: %v", err)
	}
	if _, err := p.Anki(); err == nil || !strings.Contains(err.Error(), "nix run") {
		t.Fatalf("Anki: %v", err)
	}
}

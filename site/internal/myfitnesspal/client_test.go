package myfitnesspal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthFailureFindsTheReason(t *testing.T) {
	output := "Connect your MyFitnessPal account\n(no terminal detected)\n" +
		"Those cookies didn't authenticate: Could not access MyFitnessPal using the cookies provided.\n" +
		"If this mentions Cloudflare or a 403, try MFP_IMPERSONATE=chrome124.\nValidating with MyFitnessPal...\n"
	if got := authFailure(output, errors.New("exit status 1")); got != "Could not access MyFitnessPal using the cookies provided." {
		t.Fatalf("got %q", got)
	}
	if got := authFailure("something else broke\n", errors.New("exit status 1")); got != "something else broke" {
		t.Fatalf("got %q", got)
	}
}

// A user's own folders are for the MCP's login only: with a local checkout, uv keeps the server
// account's Pythons, so every run shares one interpreter and the checkout's .venv isn't rebuilt
// between runs. Nix's build needs no uv.
func TestUserFoldersKeepUvsPythons(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("UV_PYTHON_INSTALL_DIR", "")
	checkout := t.TempDir()
	os.WriteFile(filepath.Join(checkout, "pyproject.toml"), nil, 0o600)
	userDir := filepath.Join(t.TempDir(), "user")
	if _, set := (Client{EnvDir: "/nix/store/x"}).ForUser(userDir).env()["UV_PYTHON_INSTALL_DIR"]; set {
		t.Error("Nix's build doesn't run uv")
	}
	env := Client{CheckoutDir: checkout}.ForUser(userDir).env()
	if env["XDG_DATA_HOME"] != filepath.Join(userDir, "data") {
		t.Errorf("XDG_DATA_HOME = %q", env["XDG_DATA_HOME"])
	}
	if got, want := env["UV_PYTHON_INSTALL_DIR"], filepath.Join(home, ".local", "share", "uv", "python"); got != want || strings.HasPrefix(got, userDir) {
		t.Errorf("UV_PYTHON_INSTALL_DIR = %q, want %q", got, want)
	}
	t.Setenv("UV_PYTHON_INSTALL_DIR", "/opt/uv-python")
	if got := (Client{CheckoutDir: checkout}).ForUser(userDir).env()["UV_PYTHON_INSTALL_DIR"]; got != "/opt/uv-python" {
		t.Errorf("an explicit UV_PYTHON_INSTALL_DIR should win, got %q", got)
	}
	if _, set := (Client{CheckoutDir: checkout}).env()["UV_PYTHON_INSTALL_DIR"]; set {
		t.Error("the host account's own runs need no override")
	}
}

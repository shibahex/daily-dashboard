package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Programs are the MCP servers agents use. Nix builds them (site/nix/), with every dependency
// pinned, and the sandbox sees them read-only under /nix/store. Only a local checkout of the
// MyFitnessPal MCP is installed by the dashboard itself, into a folder sandboxes only get to read.
// Inside, nothing is downloaded or installed, and no package cache is shared between users.
type Programs struct {
	Dir         string // site/data/tools, for a local checkout's install
	MFPEnv      string // MFP_MCP_SANDBOX_ENV: the Nix-built Python environment with the MyFitnessPal MCP
	MFPCheckout string // a local checkout to install instead ("" = MFPEnv)
	MFPLibPath  string // LD_LIBRARY_PATH for a checkout's curl_cffi wheel (libstdc++), from MFP_LIBRARY_PATH
	AnkiProgram string // ANKI_MCP: the Nix-built Anki MCP's program

	mu sync.Mutex // one install at a time
}

// errNotBuilt is the answer when the dashboard wasn't started with Nix, which builds the MCPs.
func errNotBuilt(mcp string) error {
	return fmt.Errorf("the %s MCP comes with the dashboard's Nix build: start it with nix run", mcp)
}

// Program is how to start an installed MCP server.
type Program struct {
	Command string
	Args    []string
	Env     map[string]string
	Mount   Mount // its folder, read-only
	Path    string
}

// MyFitnessPal is the MyFitnessPal MCP: Nix's build, or a local checkout, installed first if needed.
func (p *Programs) MyFitnessPal() (Program, error) {
	// Args must be [] rather than nil: Claude Code silently drops a server whose args are null.
	if p.MFPCheckout == "" {
		if p.MFPEnv == "" {
			return Program{}, errNotBuilt("MyFitnessPal")
		}
		bin := filepath.Join(p.MFPEnv, "bin")
		return Program{Command: filepath.Join(bin, "mfp-mcp"), Args: []string{}, Path: bin}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	python, err := realPath("python3.12")
	if err != nil {
		return Program{}, errors.New("the MyFitnessPal MCP needs python3.12 (start the dashboard with nix run)")
	}
	source := p.MFPCheckout
	dir := filepath.Join(p.Dir, "mfp-local-"+treeStamp(p.MFPCheckout))
	bin := filepath.Join(dir, "bin", "mfp-mcp")
	if _, err := os.Stat(bin); err != nil {
		os.RemoveAll(dir)
		if err := run(10*time.Minute, "uv", "venv", "--quiet", "--python", python, dir); err != nil {
			return Program{}, err
		}
		if err := run(10*time.Minute, "uv", "pip", "install", "--quiet", "--python", filepath.Join(dir, "bin", "python"), source); err != nil {
			os.RemoveAll(dir)
			return Program{}, err
		}
	}
	var env map[string]string
	if p.MFPLibPath != "" {
		env = map[string]string{"LD_LIBRARY_PATH": p.MFPLibPath}
	}
	return Program{Command: bin, Args: []string{}, Env: env, Mount: Mount{Source: dir, Target: dir}, Path: filepath.Dir(bin)}, nil
}

// Anki is the Anki MCP, from Nix's build.
func (p *Programs) Anki() (Program, error) {
	if p.AnkiProgram == "" {
		return Program{}, errNotBuilt("Anki")
	}
	return Program{Command: p.AnkiProgram, Args: []string{"--stdio"}, Path: filepath.Dir(p.AnkiProgram)}, nil
}

// Self is the dashboard program itself, for the MCP servers built into it (`dashboard task-mcp`).
// Nothing is installed: under /nix/store the sandbox already sees it, and anywhere else (a build
// from `go run`) it's shown read-only at its own path. Mount.Source is "" when none is needed.
func (p *Programs) Self() (Program, error) {
	exe, err := os.Executable()
	if err != nil {
		return Program{}, errors.New("couldn't find the dashboard's own program")
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	program := Program{Command: exe, Args: []string{}, Path: filepath.Dir(exe)}
	if !strings.HasPrefix(exe, "/nix/store/") {
		program.Mount = Mount{Source: exe, Target: exe}
	}
	return program, nil
}

// treeStamp changes whenever a file in the checkout's source changes, so edits get reinstalled.
func treeStamp(dir string) string {
	sum := sha256.New()
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".venv" || d.Name() == ".git" || strings.HasPrefix(d.Name(), ".") && path != dir) {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			fmt.Fprintf(sum, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

func realPath(program string) (string, error) {
	path, err := exec.LookPath(program)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// run runs a setup command, with its output in the error when it fails.
func run(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		return fmt.Errorf("installing an MCP server (%s) failed: %v: %s", args[0], err, lines[len(lines)-1])
	}
	return nil
}

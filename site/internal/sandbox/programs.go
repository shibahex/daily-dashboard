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

// Programs are the MCP servers agents use, installed once by the dashboard itself (outside any
// sandbox, where downloading is allowed) into a folder that sandboxes only get to read. Inside,
// nothing is downloaded or installed, and no package cache is shared between users.
type Programs struct {
	Dir         string // site/data/tools
	MFPPackage  string // pip requirement for the released MyFitnessPal MCP ("mfp-mcp==0.4.1")
	MFPCheckout string // a local checkout to install instead ("" = the released package)
	MFPLibPath  string // LD_LIBRARY_PATH for its curl_cffi wheel (libstdc++), from MFP_LIBRARY_PATH
	AnkiPackage string // npm package for the Anki MCP ("@ankimcp/anki-mcp-server@0.27.0")

	mu sync.Mutex // one install at a time
}

// Program is how to start an installed MCP server.
type Program struct {
	Command string
	Args    []string
	Env     map[string]string
	Mount   Mount // its folder, read-only
	Path    string
}

// MyFitnessPal is the MyFitnessPal MCP, installing it first if needed.
func (p *Programs) MyFitnessPal() (Program, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	python, err := realPath("python3.12")
	if err != nil {
		return Program{}, errors.New("the MyFitnessPal MCP needs python3.12 (start the dashboard with nix run)")
	}
	source, name := p.MFPPackage, "mfp-"+safeName(p.MFPPackage)
	if p.MFPCheckout != "" {
		source, name = p.MFPCheckout, "mfp-local-"+treeStamp(p.MFPCheckout)
	}
	dir := filepath.Join(p.Dir, name)
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
	// Args must be [] rather than nil: Claude Code silently drops a server whose args are null.
	return Program{Command: bin, Args: []string{}, Env: env, Mount: Mount{Source: dir, Target: dir}, Path: filepath.Dir(bin)}, nil
}

// Anki is the Anki MCP, installing it first if needed.
func (p *Programs) Anki() (Program, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	node, err := realPath("node")
	if err != nil {
		return Program{}, errors.New("the Anki MCP needs Node.js (start the dashboard with nix run)")
	}
	dir := filepath.Join(p.Dir, "anki-"+safeName(p.AnkiPackage))
	pkg := strings.TrimSuffix(p.AnkiPackage, "@"+lastVersion(p.AnkiPackage))
	script := filepath.Join(dir, "node_modules", filepath.FromSlash(pkg), "bin", "ankimcp.js")
	if _, err := os.Stat(script); err != nil {
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o700)
		// --ignore-scripts: nothing the package or its dependencies ship runs at install time.
		if err := run(10*time.Minute, "npm", "install", "--prefix", dir, "--no-audit", "--no-fund", "--ignore-scripts",
			"--omit=dev", p.AnkiPackage); err != nil {
			os.RemoveAll(dir)
			return Program{}, err
		}
	}
	return Program{Command: node, Args: []string{script, "--stdio"}, Mount: Mount{Source: dir, Target: dir}, Path: filepath.Dir(node)}, nil
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

// lastVersion is "0.27.0" for "@scope/name@0.27.0".
func lastVersion(spec string) string {
	if i := strings.LastIndex(spec, "@"); i > 0 {
		return spec[i+1:]
	}
	return ""
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
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

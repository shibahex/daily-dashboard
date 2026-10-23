// Package sandbox runs an AI agent's CLI in a bubblewrap sandbox: its own empty home, a
// /workspace with only what the user's policy lets it see, the Nix store read-only, and no
// network of its own. Its only way out is the dashboard's egress proxy (and, for the Anki MCP,
// the user's Anki gateway), each reached through a unix socket the dashboard opens for the run
// and a socat inside the sandbox that offers it on 127.0.0.1.
//
// Nothing of the host is visible: not the dashboard's files, not other users' folders, not the
// server account's home or logins.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Where things are inside the sandbox.
const (
	HomeDir      = "/home/agent"
	WorkspaceDir = "/workspace"
	socketDir    = "/run/dashboard"
	ProxyURL     = "http://127.0.0.1:3128"
	AnkiURL      = "http://127.0.0.1:8765"
)

// Tools are the programs the sandbox needs, as real paths (in the Nix store on NixOS).
type Tools struct {
	Bwrap, Socat, Shell, Env string
	Coreutils                string // a bin/ folder with cat, ls... for shells inside
	CACerts                  string // the CA bundle, for TLS inside
}

// Find looks the tools up on the PATH (the flake's runtimeInputs provide them).
func Find() (Tools, error) {
	var t Tools
	var missing []string
	look := func(name string) string {
		path, err := exec.LookPath(name)
		if err != nil {
			missing = append(missing, name)
			return ""
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return path
		}
		return real
	}
	t.Bwrap, t.Socat, t.Shell, t.Env = look("bwrap"), look("socat"), look("bash"), look("env")
	if cat := look("cat"); cat != "" {
		t.Coreutils = filepath.Dir(cat)
	}
	if len(missing) > 0 {
		return Tools{}, fmt.Errorf("the AI sandbox needs %s (start the dashboard with nix run)", strings.Join(missing, ", "))
	}
	for _, bundle := range []string{os.Getenv("SSL_CERT_FILE"), "/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/certs/ca-bundle.crt"} {
		if bundle == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(bundle); err == nil {
			t.CACerts = real
			break
		}
	}
	if t.CACerts == "" {
		return Tools{}, errors.New("the AI sandbox needs a CA certificate bundle (set SSL_CERT_FILE)")
	}
	return t, nil
}

// Mount is a host path shown inside the sandbox.
type Mount struct {
	Source, Target string
	Writable       bool
}

// Spec is one run's sandbox.
type Spec struct {
	RunDir    string   // the dashboard's folder for this run: sockets, passwd (not mounted whole)
	Home      string   // host folder shown as HomeDir (writable)
	Workspace string   // host folder shown as WorkspaceDir (writable; the copy of the user's files)
	Mounts    []Mount  // anything else: MCP programs (read-only), the MyFitnessPal login folder
	Egress    string   // the egress proxy's socket
	Anki      string   // the user's Anki gateway socket ("" = no Anki)
	Path      []string // extra folders for PATH (the CLI's and MCPs' programs)
	Env       []string // KEY=value for inside, on top of the basics
}

// Command is the full command line that runs args inside the sandbox.
func (t Tools) Command(spec Spec, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("nothing to run")
	}
	passwd := filepath.Join(spec.RunDir, "passwd")
	group := filepath.Join(spec.RunDir, "group")
	if err := os.WriteFile(passwd, []byte("agent:x:1000:1000:agent:"+HomeDir+":/bin/sh\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(group, []byte("agent:x:1000:\n"), 0o600); err != nil {
		return nil, err
	}

	path := append(append([]string{}, spec.Path...), t.Coreutils, filepath.Dir(t.Shell))
	cmd := []string{t.Bwrap,
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--uid", "1000", "--gid", "1000", "--hostname", "sandbox",
		"--ro-bind", "/nix/store", "/nix/store",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--ro-bind", passwd, "/etc/passwd", "--ro-bind", group, "/etc/group",
		"--ro-bind", t.CACerts, "/etc/ssl/certs/ca-certificates.crt",
		"--ro-bind", t.Shell, "/bin/sh", "--ro-bind", t.Env, "/usr/bin/env",
		"--bind", spec.Home, HomeDir,
		"--bind", spec.Workspace, WorkspaceDir,
		"--dir", socketDir,
		"--ro-bind", spec.Egress, socketDir + "/egress.sock",
	}
	if spec.Anki != "" {
		cmd = append(cmd, "--ro-bind", spec.Anki, socketDir+"/anki.sock")
	}
	for _, m := range spec.Mounts {
		flag := "--ro-bind"
		if m.Writable {
			flag = "--bind"
		}
		cmd = append(cmd, flag, m.Source, m.Target)
	}
	env := []string{"HOME=" + HomeDir, "USER=agent", "LOGNAME=agent", "SHELL=/bin/sh", "TMPDIR=/tmp", "LANG=C.UTF-8",
		"PATH=" + strings.Join(path, ":"), "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt",
		"HTTPS_PROXY=" + ProxyURL, "HTTP_PROXY=" + ProxyURL, "https_proxy=" + ProxyURL, "http_proxy=" + ProxyURL,
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost"}
	for _, kv := range append(env, spec.Env...) {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		cmd = append(cmd, "--setenv", key, value)
	}
	cmd = append(cmd, "--chdir", WorkspaceDir, "--", t.Shell, "-c", t.startScript(spec.Anki != ""), "sandbox")
	return append(cmd, args...), nil
}

// startScript starts the socats that offer the dashboard's sockets on 127.0.0.1, waits until
// they listen, then becomes the CLI.
func (t Tools) startScript(anki bool) string {
	script := t.Socat + " TCP-LISTEN:3128,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:" + socketDir + "/egress.sock 2>/dev/null &\n"
	if anki {
		script += t.Socat + " TCP-LISTEN:8765,bind=127.0.0.1,fork,reuseaddr UNIX-CONNECT:" + socketDir + "/anki.sock 2>/dev/null &\n"
	}
	// bash's /dev/tcp tells when socat listens; give up after ~2s and let the CLI report it.
	script += "for i in $(seq 40); do (exec 3<>/dev/tcp/127.0.0.1/3128) 2>/dev/null && break; sleep 0.05; done\n"
	return script + "exec \"$@\"\n"
}

// Explain adds the fix to a failure the host causes: Ubuntu 23.10 and later strip the
// permissions from user namespaces made by programs without an AppArmor profile, so bwrap can't
// set up its network or mounts. The container needs a profile that allows user namespaces.
func Explain(err error) error {
	if err != nil && strings.Contains(err.Error(), "bwrap:") && strings.Contains(err.Error(), "Operation not permitted") {
		return fmt.Errorf("%w (the host restricts user namespaces: in Docker, set DASHBOARD_APPARMOR=podman; see docs/dashboard.md)", err)
	}
	return err
}

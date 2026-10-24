package sandbox

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// inSandbox runs script inside a sandbox whose egress socket answers with a fixed 403, and returns
// its output.
func inSandbox(t *testing.T, script string, extra ...Mount) string {
	t.Helper()
	tools, err := Find()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	runDir, home, workspace := filepath.Join(dir, "run"), filepath.Join(dir, "home"), filepath.Join(dir, "workspace")
	for _, d := range []string{runDir, home, workspace} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("my notes"), 0o600)
	socket := filepath.Join(runDir, "egress.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proxy saw "+r.Method+" "+r.Host, http.StatusForbidden)
	}))

	args, err := tools.Command(Spec{RunDir: runDir, Home: home, Workspace: workspace, Egress: socket, Mounts: extra,
		Env: []string{"SECRET_FOR_AGENT=key-123"}}, []string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Logf("sandbox exited: %v", err)
	}
	return string(out)
}

func TestTheSandboxSeesOnlyItsOwnFolders(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "someone-elses-data.json")
	os.WriteFile(secret, []byte("private"), 0o600)
	home, _ := os.UserHomeDir()

	out := inSandbox(t, fmt.Sprintf(`
		echo "whoami: $(id -u) $HOME $(pwd)"
		cat %q 2>/dev/null && echo "LEAK: other file" || echo "other file: hidden"
		[ -n "$(ls %q 2>/dev/null)" ] && echo "LEAK: server home" || echo "server home: hidden"
		ls /home
		cat /workspace/notes.md; echo
		echo edited > /workspace/notes.md && echo "workspace: writable"
		touch $HOME/x && echo "home: writable"
		touch /nix/store/x 2>/dev/null && echo "LEAK: store writable" || echo "store: read-only"
		echo "key: $SECRET_FOR_AGENT"
		[ -z "$DISPLAY$WAYLAND_DISPLAY$SSH_AUTH_SOCK$XDG_RUNTIME_DIR" ] && echo "env: clean" || echo "LEAK: host env [$DISPLAY|$WAYLAND_DISPLAY|$SSH_AUTH_SOCK|$XDG_RUNTIME_DIR]"
	`, secret, home))
	t.Log(out)
	for _, want := range []string{"whoami: 1000 /home/agent /workspace", "other file: hidden", "server home: hidden",
		"my notes", "workspace: writable", "home: writable", "store: read-only", "key: key-123", "env: clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "LEAK") {
		t.Errorf("the sandbox could see something it mustn't:\n%s", out)
	}
	if lines := strings.Fields(strings.SplitN(strings.SplitN(out, "server home: hidden\n", 2)[1], "\n", 2)[0]); len(lines) != 1 || lines[0] != "agent" {
		t.Errorf("/home should only hold the agent: %v", lines)
	}
}

func TestTheSandboxHasNoNetworkButTheProxy(t *testing.T) {
	out := inSandbox(t, `
		(exec 3<>/dev/tcp/1.1.1.1/443) 2>/dev/null && echo "LEAK: direct internet" || echo "direct: blocked"
		(exec 3<>/dev/tcp/127.0.0.1/1628) 2>/dev/null && echo "LEAK: host loopback" || echo "loopback: blocked"
		exec 3<>/dev/tcp/127.0.0.1/3128
		printf 'CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nConnection: close\r\n\r\n' >&3
		cat <&3
	`)
	t.Log(out)
	for _, want := range []string{"direct: blocked", "loopback: blocked", "proxy saw CONNECT example.com:443"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "LEAK") {
		t.Errorf("network leak:\n%s", out)
	}
}

func TestTheTasksSocketIsOnlyBoundWhenGiven(t *testing.T) {
	dir := t.TempDir()
	tools := Tools{Bwrap: "bwrap", Socat: "socat", Shell: "/bin/sh", Env: "/usr/bin/env", Coreutils: "/bin",
		CACerts: "/etc/ssl/certs/ca-certificates.crt"}
	spec := Spec{RunDir: dir, Home: dir, Workspace: dir, Egress: filepath.Join(dir, "egress.sock")}
	without, err := tools.Command(spec, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	spec.Tasks = filepath.Join(dir, "tasks.sock")
	with, err := tools.Command(spec, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(without, " "), TasksSocket) {
		t.Error("the Taskwarrior socket is in the sandbox without being asked for")
	}
	if !strings.Contains(" "+strings.Join(with, " ")+" ", " --ro-bind "+spec.Tasks+" "+TasksSocket+" ") {
		t.Errorf("the Taskwarrior socket isn't bound: %v", with)
	}
}

func TestTheDashboardCanRunItselfAsAnMCPServer(t *testing.T) {
	program, err := (&Programs{}).Self()
	if err != nil || program.Command == "" {
		t.Fatalf("Self() = %+v, %v", program, err)
	}
	// Outside /nix/store the sandbox can only run it if it's shown there.
	if !strings.HasPrefix(program.Command, "/nix/store/") && (program.Mount.Source != program.Command || program.Mount.Target != program.Command) {
		t.Errorf("%s isn't visible in the sandbox: %+v", program.Command, program.Mount)
	}
}

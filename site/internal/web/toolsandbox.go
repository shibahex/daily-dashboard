package web

import (
	"cmp"
	"net"
	"os"
	"path/filepath"

	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/mcp"
	"dashboard/internal/problem"
	"dashboard/internal/sandbox"
	"dashboard/internal/taskmcp"
	"dashboard/internal/workspace"
)

// toolSandbox is where a user's MCP tools run for the AI in their browser (browserai.go): a
// bubblewrap sandbox with an empty home and workspace, the network only through the egress
// proxy, and only that user's own MyFitnessPal login, Anki gateway key or Taskwarrior gateway.
type toolSandbox struct {
	ui        *UI
	ws        *workspace.Workspace
	listeners []net.Listener
	sockets   string // a private folder for the sandbox's sockets
	workspace string // the workspace folder's name in the user's folder ("" = agent-tools)
}

func (sb *toolSandbox) homeDir() string { return sb.ws.Paths.DataFile("agent-home") }

// workspaceDir is emptied after each use, and never saved with the user's data (vault.Mirror).
func (sb *toolSandbox) workspaceDir() string {
	return sb.ws.Paths.DataFile(cmp.Or(sb.workspace, "agent-tools"))
}

// allowedMCPs are the turned-on MCPs the user's policy allows.
func allowedMCPs(policy auth.Policy, conversation *assistant.Conversation) []string {
	var allowed []string
	for _, name := range conversation.EnabledMCPs() {
		if policy.AllowsMCP(name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

// open makes the sandbox's folders and opens its egress proxy socket.
func (sb *toolSandbox) open() (sandbox.Spec, error) {
	for _, dir := range []string{sb.homeDir(), sb.workspaceDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return sandbox.Spec{}, err
		}
	}
	// Unix socket paths can't be longer than 108 bytes, so a deep user folder can't hold them:
	// they go in a short, private (0700), randomly named folder instead.
	sockets, err := os.MkdirTemp(shortTempDir(), "dashboard-tools-")
	if err != nil {
		return sandbox.Spec{}, err
	}
	sb.sockets = sockets
	egressSocket := filepath.Join(sockets, "egress.sock")
	egress, err := net.Listen("unix", egressSocket)
	if err != nil {
		return sandbox.Spec{}, err
	}
	sb.listeners = append(sb.listeners, egress)
	go sb.ui.Egress.Serve(egress)
	return sandbox.Spec{RunDir: sockets, Home: sb.homeDir(), Workspace: sb.workspaceDir(), Egress: egressSocket}, nil
}

// mcpServer is how one MCP server starts inside the sandbox, adding what it needs to spec: its
// program (read-only) and the user's own MyFitnessPal login or Anki socket.
func (sb *toolSandbox) mcpServer(name string, spec *sandbox.Spec) (mcp.Server, error) {
	switch name {
	case "myfitnesspal":
		program, err := sb.ui.Programs.MyFitnessPal()
		if err != nil {
			return mcp.Server{}, err
		}
		login := filepath.Dir(sb.ws.MyFitnessPal.Client().CookiePath())
		if err := os.MkdirAll(login, 0o700); err != nil {
			return mcp.Server{}, err
		}
		spec.Mounts = append(spec.Mounts, program.Mount,
			sandbox.Mount{Source: login, Target: sandbox.HomeDir + "/.config/myfitnesspal-mcp", Writable: true})
		spec.Path = append(spec.Path, program.Path)
		return mcp.Server{Command: program.Command, Args: program.Args, Env: program.Env}, nil
	case "anki":
		program, err := sb.ui.Programs.Anki()
		if err != nil {
			return mcp.Server{}, err
		}
		socket := filepath.Join(sb.sockets, "anki.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			return mcp.Server{}, err
		}
		sb.listeners = append(sb.listeners, listener)
		go sb.ui.Workspaces.Gateway().ServeOn(listener)
		spec.Anki = socket
		spec.Mounts = append(spec.Mounts, program.Mount)
		spec.Path = append(spec.Path, program.Path)
		return mcp.Server{Command: program.Command, Args: program.Args,
			Env: map[string]string{"ANKI_CONNECT_URL": sandbox.AnkiURL, "ANKI_CONNECT_API_KEY": sb.ws.Anki().GatewayKey}}, nil
	case "taskwarrior":
		// The MCP is the dashboard's own program: it only passes the AI's calls through a socket
		// to the gateway here, which works on this user's tasks. No Taskwarrior data, taskrc or
		// `task` program is inside the sandbox.
		program, err := sb.ui.Programs.Self()
		if err != nil {
			return mcp.Server{}, err
		}
		socket := filepath.Join(sb.sockets, "tasks.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			return mcp.Server{}, err
		}
		sb.listeners = append(sb.listeners, listener)
		go taskmcp.NewGateway(sb.ws.Tasks).Serve(listener)
		spec.Tasks = socket
		if program.Mount.Source != "" {
			spec.Mounts = append(spec.Mounts, program.Mount)
		}
		spec.Path = append(spec.Path, program.Path)
		return mcp.Server{Command: program.Command, Args: []string{taskmcp.Command},
			Env: map[string]string{taskmcp.SocketEnv: sandbox.TasksSocket}}, nil
	}
	return mcp.Server{}, problem.Invalidf("unknown MCP " + name)
}

func (sb *toolSandbox) cleanUp() {
	for _, listener := range sb.listeners {
		listener.Close()
	}
	if sb.sockets != "" {
		os.RemoveAll(sb.sockets)
	}
	os.RemoveAll(sb.workspaceDir())
}

// shortTempDir is /tmp when it's there (TMPDIR can be long inside nix develop).
func shortTempDir() string {
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

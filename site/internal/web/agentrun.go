package web

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dashboard/internal/assistant"
	"dashboard/internal/auth"
	"dashboard/internal/mcp"
	"dashboard/internal/problem"
	"dashboard/internal/sandbox"
	"dashboard/internal/workspace"
)

// agentRun is one answer from a user's AI agent, run in their sandbox: it sees a copy of what
// their policy lets it read, reaches the network only through the egress proxy, and its MCP
// servers use only that user's MyFitnessPal login and Anki gateway key.
type agentRun struct {
	ui           *UI
	ws           *workspace.Workspace
	provider     assistant.Provider
	conversation *assistant.Conversation
	message      string
	mcps         []string // allowed and turned on
	caps         assistant.Capabilities
	env          []string // the user's API key

	listeners []net.Listener
	staged    workspace.Staged
	sockets   string // a private folder for this run's sockets
}

// Where a run's files live in the user's folder.
func (run *agentRun) homeDir() string      { return run.ws.Paths.DataFile("agent-home") }
func (run *agentRun) workspaceDir() string { return run.ws.Paths.DataFile("agent-workspace") }
func (run *agentRun) configDir() string    { return filepath.Join(run.homeDir(), "run-config") }

// capabilitiesFor is what the policy lets the agent do beyond its MCP servers.
func capabilitiesFor(policy auth.Policy) assistant.Capabilities {
	return assistant.Capabilities{Web: policy.Web, Read: policy.ReadFiles, Edit: policy.EditFiles && policy.ReadFiles}
}

// answer runs in the background, so the answer is saved even if the page goes away.
func (run *agentRun) answer() {
	ws := run.ws
	defer ws.Live.Finish()
	defer run.cleanUp()
	request, err := run.prepare()
	var outcome assistant.Outcome
	if err == nil {
		outcome, err = ws.Chat.Answer(run.provider, run.conversation, request, ws.Live.Progress)
	}
	if err != nil {
		recordFailure(run.conversation, run.message, run.provider, err)
	} else if run.caps.Edit {
		if changed := ws.ApplyEdits(run.workspaceDir(), run.staged); len(changed) > 0 {
			noteSaved(run.conversation, changed)
		}
	}
	if err := ws.Conversations.Save(run.conversation); err != nil {
		log.Printf("saving the chat: %v", err)
	}
	if outcome.ChangedDiary {
		ws.MyFitnessPal.Sync() // so the Today tab shows the change right away
	}
}

// noteSaved lists the files the agent changed under its reply.
func noteSaved(conversation *assistant.Conversation, changed []string) {
	if n := len(conversation.Messages); n > 0 && conversation.Messages[n-1].Role == "assistant" {
		reply := &conversation.Messages[n-1]
		reply.Tools = append(reply.Tools, assistant.Tool{Label: "Saved", Detail: strings.Join(changed, ", ")})
	}
}

// prepare stages the workspace, opens the sockets and builds the request.
func (run *agentRun) prepare() (assistant.Request, error) {
	ui, ws := run.ui, run.ws
	for _, dir := range []string{run.homeDir(), run.configDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return assistant.Request{}, err
		}
	}
	// Unix socket paths can't be longer than 108 bytes, so a deep user folder can't hold them:
	// they go in a short, private (0700), randomly named folder instead.
	sockets, err := os.MkdirTemp(shortTempDir(), "dashboard-agent-")
	if err != nil {
		return assistant.Request{}, err
	}
	run.sockets = sockets
	staged, err := ws.StageFiles(run.workspaceDir(), run.caps.Read)
	if err != nil {
		return assistant.Request{}, err
	}
	run.staged = staged

	egressSocket := filepath.Join(run.sockets, "egress.sock")
	egress, err := net.Listen("unix", egressSocket)
	if err != nil {
		return assistant.Request{}, err
	}
	run.listeners = append(run.listeners, egress)
	go ui.Egress.Serve(egress)

	spec := sandbox.Spec{RunDir: run.sockets, Home: run.homeDir(), Workspace: run.workspaceDir(), Egress: egressSocket}
	var servers []assistant.NamedServer
	for _, name := range run.mcps {
		server, err := run.mcpServer(name, &spec)
		if err != nil {
			return assistant.Request{}, err
		}
		servers = append(servers, assistant.NamedServer{Name: name, Server: server})
	}

	return assistant.Request{
		Message:      run.message,
		SessionID:    run.conversation.SessionID,
		Model:        run.conversation.Model(),
		Servers:      servers,
		Instructions: assistant.Instructions(time.Now(), assistant.Context(ws.Paths.Docs, ws.Paths.Plan(), ws.Paths.Data), servers, run.caps),
		Caps:         run.caps,
		WorkDir:      run.configDir(),
		InnerWorkDir: sandbox.HomeDir + "/run-config",
		Workspace:    sandbox.WorkspaceDir,
		Env:          run.env,
		Wrap: func(inv assistant.Invocation) (assistant.Invocation, error) {
			inner := spec
			inner.Env = inv.Env
			inner.Path = append(append([]string{}, spec.Path...), filepath.Dir(inv.Args[0]))
			args, err := ui.Sandbox.Command(inner, inv.Args)
			return assistant.Invocation{Args: args, Dir: run.sockets, Stdin: inv.Stdin}, err
		},
	}, nil
}

// mcpServer is how the agent starts one MCP server inside the sandbox, adding what it needs to
// the sandbox: its program (read-only) and the user's own MyFitnessPal login or Anki socket.
func (run *agentRun) mcpServer(name string, spec *sandbox.Spec) (mcp.Server, error) {
	switch name {
	case "myfitnesspal":
		program, err := run.ui.Programs.MyFitnessPal()
		if err != nil {
			return mcp.Server{}, err
		}
		login := filepath.Dir(run.ws.MyFitnessPal.Client().CookiePath())
		if err := os.MkdirAll(login, 0o700); err != nil {
			return mcp.Server{}, err
		}
		spec.Mounts = append(spec.Mounts, program.Mount,
			sandbox.Mount{Source: login, Target: sandbox.HomeDir + "/.config/myfitnesspal-mcp", Writable: true})
		spec.Path = append(spec.Path, program.Path)
		return mcp.Server{Command: program.Command, Args: program.Args, Env: program.Env}, nil
	case "anki":
		program, err := run.ui.Programs.Anki()
		if err != nil {
			return mcp.Server{}, err
		}
		socket := filepath.Join(run.sockets, "anki.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			return mcp.Server{}, err
		}
		run.listeners = append(run.listeners, listener)
		go run.ui.Workspaces.Gateway().ServeOn(listener)
		spec.Anki = socket
		spec.Mounts = append(spec.Mounts, program.Mount)
		spec.Path = append(spec.Path, program.Path)
		return mcp.Server{Command: program.Command, Args: program.Args,
			Env: map[string]string{"ANKI_CONNECT_URL": sandbox.AnkiURL, "ANKI_CONNECT_API_KEY": run.ws.Anki().GatewayKey}}, nil
	}
	return mcp.Server{}, problem.Invalidf("unknown MCP " + name)
}

func (run *agentRun) cleanUp() {
	for _, listener := range run.listeners {
		listener.Close()
	}
	if run.sockets != "" {
		os.RemoveAll(run.sockets)
	}
	os.RemoveAll(run.workspaceDir())
}

// shortTempDir is /tmp when it's there (TMPDIR can be long inside nix develop).
func shortTempDir() string {
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

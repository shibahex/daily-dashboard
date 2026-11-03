// Package ankiserver runs each user's Anki on the dashboard's server: Anki itself, without a
// screen, with AnkiConnect and the dashboard's add-on (site/nix/anki-server.nix), in a
// bubblewrap sandbox of its own. Its only way out is the egress proxy (to AnkiWeb), and the
// dashboard reaches its AnkiConnect through a unix socket (sandbox.Spec.Expose).
//
// A user's Anki starts when something asks it (the Anki tab, the AI's Anki tools), syncs with
// AnkiWeb, and stops again when unused for IdleAfter, syncing first. Its folder is the user's
// own (anki/ in their folder, inside their vault).
package ankiserver

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"dashboard/internal/anki"
	"dashboard/internal/egress"
	"dashboard/internal/problem"
	"dashboard/internal/sandbox"
)

// Folder is the user's Anki folder, in their own folder.
const Folder = "anki"

const (
	port         = 8765 // AnkiConnect's, inside the sandbox
	startTimeout = 90 * time.Second
	stopTimeout  = 60 * time.Second
)

// Runner starts and stops the users' Ankis.
type Runner struct {
	Program   string        // DASHBOARD_ANKI: the dashboard-anki launcher ("" = not available)
	Tools     sandbox.Tools // the sandbox's programs
	Egress    *egress.Proxy
	SyncURL   string        // DASHBOARD_ANKI_SYNC_URL: a sync server instead of AnkiWeb ("" = AnkiWeb)
	IdleAfter time.Duration // stop a user's Anki after this long unused
	SyncEvery time.Duration // sync a running Anki this often

	mu      sync.Mutex
	running map[string]*instance // by user id
}

// instance is one user's running Anki.
type instance struct {
	dir     string // the user's Anki folder on the host
	run     string // a private folder for its sockets
	socket  string
	cmd     *exec.Cmd
	exited  chan struct{}
	ready   chan struct{} // closed once AnkiConnect answers (or the start failed: err)
	err     error
	egress  net.Listener
	mu      sync.Mutex
	used    time.Time
	synced  time.Time
	syncing bool
	stopped bool
}

// client talks to this Anki directly: unlike Runner.Client, it neither starts it nor counts as use.
func (inst *instance) client() anki.Client {
	return anki.NewServerClient(func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", inst.socket)
	})
}

func New(program string, tools sandbox.Tools, proxy *egress.Proxy) *Runner {
	return &Runner{Program: program, Tools: tools, Egress: proxy, IdleAfter: 15 * time.Minute,
		SyncEvery: 20 * time.Minute, running: map[string]*instance{}}
}

// Available reports whether Anki can run on this server (the dashboard was started with Nix).
func (r *Runner) Available() bool { return r != nil && r.Program != "" && r.Tools.Bwrap != "" }

// Client is the user's Anki on the server, started on first use. dir is the user's folder.
func (r *Runner) Client(userID, dir string) anki.Client {
	client := anki.NewServerClient(func(ctx context.Context) (net.Conn, error) {
		inst, err := r.ensure(userID, filepath.Join(dir, Folder))
		if err != nil {
			return nil, err
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", inst.socket)
	})
	client.Awake = func() (anki.Client, bool) {
		r.mu.Lock()
		inst, ok := r.running[userID]
		r.mu.Unlock()
		if !ok {
			return anki.Client{}, false
		}
		select {
		case <-inst.ready:
			return inst.client(), inst.err == nil
		default:
			return anki.Client{}, false
		}
	}
	return client
}

// Running reports whether the user's Anki is running (its folder is in use).
func (r *Runner) Running(userID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[userID]
	return ok
}

// ensure starts the user's Anki unless it's running, and waits until it answers.
func (r *Runner) ensure(userID, dir string) (*instance, error) {
	if !r.Available() {
		return nil, problem.Unavailablef("Anki can't run on this server (start the dashboard with nix run)")
	}
	r.mu.Lock()
	inst, ok := r.running[userID]
	if !ok {
		inst = &instance{dir: dir, exited: make(chan struct{}), ready: make(chan struct{})}
		r.running[userID] = inst
		go r.start(userID, inst)
	}
	r.mu.Unlock()
	select {
	case <-inst.ready:
	case <-time.After(startTimeout):
		return nil, problem.Unavailablef("Your Anki is taking too long to start; try again in a minute")
	}
	if inst.err != nil {
		return nil, inst.err
	}
	inst.mu.Lock()
	inst.used = time.Now()
	inst.mu.Unlock()
	return inst, nil
}

// start runs Anki in its sandbox and closes inst.ready once AnkiConnect answers.
func (r *Runner) start(userID string, inst *instance) {
	fail := func(err error) {
		log.Printf("starting %s's Anki: %v", userID, err)
		inst.err = problem.Unavailablef("Your Anki on the server couldn't start")
		r.forget(userID, inst)
		close(inst.ready)
	}
	if err := os.MkdirAll(inst.dir, 0o700); err != nil {
		fail(err)
		return
	}
	run, err := os.MkdirTemp(shortTempDir(), "dashboard-anki-")
	if err != nil {
		fail(err)
		return
	}
	inst.run = run
	out, work := filepath.Join(run, "out"), filepath.Join(run, "work")
	for _, d := range []string{out, work} {
		os.MkdirAll(d, 0o700)
	}
	inst.socket = filepath.Join(out, sandbox.ExposedSocket)
	egressSocket := filepath.Join(run, "egress.sock")
	if inst.egress, err = net.Listen("unix", egressSocket); err != nil {
		fail(err)
		return
	}
	go r.Egress.Serve(inst.egress)

	spec := sandbox.Spec{RunDir: run, Home: inst.dir, Workspace: work, Egress: egressSocket,
		Expose: port, ExposeDir: out, Path: []string{filepath.Dir(r.Program)}}
	if r.SyncURL != "" {
		spec.Env = append(spec.Env, "DASHBOARD_ANKI_SYNC_URL="+r.SyncURL)
	}
	args, err := r.Tools.Command(spec, []string{r.Program, sandbox.HomeDir + "/base"})
	if err != nil {
		fail(err)
		return
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = run
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fail(sandbox.Explain(err))
		return
	}
	inst.cmd = cmd
	go func() {
		cmd.Wait()
		close(inst.exited)
		r.forget(userID, inst)
	}()

	client := inst.client()
	deadline := time.Now().Add(startTimeout)
	for {
		if _, err := client.Call("version", nil); err == nil {
			break
		}
		select {
		case <-inst.exited:
			fail(errors.New("Anki exited while starting"))
			return
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			r.kill(inst)
			fail(errors.New("AnkiConnect didn't answer"))
			return
		}
	}
	close(inst.ready)
	// Fresh cards from AnkiWeb before the user studies (when they're signed in).
	r.sync(userID, inst, client)
}

// forget drops inst from the running list (once it has stopped or failed) and its sockets.
func (r *Runner) forget(userID string, inst *instance) {
	r.mu.Lock()
	if r.running[userID] == inst {
		delete(r.running, userID)
	}
	r.mu.Unlock()
	if inst.egress != nil {
		inst.egress.Close()
	}
	if inst.run != "" {
		os.RemoveAll(inst.run)
	}
}

func (r *Runner) sync(userID string, inst *instance, client anki.Client) {
	inst.mu.Lock()
	if inst.syncing {
		inst.mu.Unlock()
		return
	}
	inst.syncing = true
	inst.mu.Unlock()
	defer func() { inst.mu.Lock(); inst.syncing = false; inst.mu.Unlock() }()
	if status, err := client.ServerStatus(); err != nil || !status.SignedIn {
		return
	}
	if _, err := client.SyncServer(); err != nil {
		log.Printf("syncing %s's Anki: %v", userID, err)
		return
	}
	inst.mu.Lock()
	inst.synced = time.Now()
	inst.mu.Unlock()
}

// Stop syncs the user's Anki and closes it (Anki saves its collection as it closes). It waits
// until Anki has exited, so the user's folder can be saved or locked afterwards.
func (r *Runner) Stop(userID string) { r.stop(userID, true) }

func (r *Runner) stop(userID string, sync bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	inst, ok := r.running[userID]
	r.mu.Unlock()
	if !ok {
		return
	}
	<-inst.ready
	if inst.err != nil {
		return
	}
	inst.mu.Lock()
	already := inst.stopped
	inst.stopped = true
	inst.mu.Unlock()
	if !already {
		client := inst.client()
		if sync {
			r.sync(userID, inst, client)
		}
		client.Call("dashboardQuit", nil)
	}
	select {
	case <-inst.exited:
	case <-time.After(stopTimeout):
		log.Printf("%s's Anki didn't close; stopping it", userID)
		r.kill(inst)
		<-inst.exited
	}
}

// StopAll closes every user's Anki, for shutting down. It doesn't sync first: a stopping
// server has seconds, and the user's folder must still be saved (their next start syncs).
func (r *Runner) StopAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	var users []string
	for id := range r.running {
		users = append(users, id)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range users {
		wg.Add(1)
		go func() { defer wg.Done(); r.stop(id, false) }()
	}
	wg.Wait()
}

func (r *Runner) kill(inst *instance) {
	if inst.cmd != nil && inst.cmd.Process != nil {
		syscall.Kill(-inst.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// RunUntil stops Ankis nobody has used for IdleAfter and syncs running ones every SyncEvery.
func (r *Runner) RunUntil(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		running := map[string]*instance{}
		for id, inst := range r.running {
			running[id] = inst
		}
		r.mu.Unlock()
		for id, inst := range running {
			select {
			case <-inst.ready:
			default:
				continue // still starting
			}
			inst.mu.Lock()
			idle, due := time.Since(inst.used) > r.IdleAfter, time.Since(inst.synced) > r.SyncEvery
			inst.mu.Unlock()
			switch {
			case idle:
				go r.Stop(id)
			case due:
				go r.sync(id, inst, inst.client())
			}
		}
	}
}

// shortTempDir is /tmp when it's there: unix socket paths can't be longer than 108 bytes.
func shortTempDir() string {
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

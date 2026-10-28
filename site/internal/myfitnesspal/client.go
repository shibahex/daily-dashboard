// Package myfitnesspal reads and changes the user's MyFitnessPal diary over HTTP (mfp.go), with
// the login the MyFitnessPal MCP (mfp-mcp) saves. The MCP itself is what AI agents use, and it
// signs the user in (Connect) and renews an expired login in its headless browser.
package myfitnesspal

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/mcp"
	"dashboard/internal/problem"
	"dashboard/internal/text"
)

const (
	Version = "0.4.1" // pinned, like the Anki MCP: a new release runs only after review
	Package = "mfp-mcp[autorefresh]==" + Version
	RepoURL = "https://github.com/Mason-Levyy/myfitnesspal-mcp"
)

// Client is one user's MyFitnessPal. The MCP comes from a local checkout or the released package.
type Client struct {
	CheckoutDir string // MFP_MCP_DIR: a local checkout; empty uses the released package
	BrowserPath string // MFP_BROWSER_PATH: the browser the MCP renews the login with

	// A user's own folders for the MCP's saved login (config) and browser profile (data).
	// Empty uses this computer's own (~/.config/myfitnesspal-mcp): the host account.
	ConfigHome, DataHome string
}

// ForUser is the client for a user whose MyFitnessPal login lives under dir.
func (c Client) ForUser(dir string) Client {
	c.ConfigHome, c.DataHome = filepath.Join(dir, "config"), filepath.Join(dir, "data")
	return c
}

// env is what the MCP runs with: the user's own folders, and the browser for renewing the login.
func (c Client) env() map[string]string {
	env := map[string]string{}
	if c.ConfigHome != "" {
		env["XDG_CONFIG_HOME"], env["XDG_DATA_HOME"] = c.ConfigHome, c.DataHome
		// uv reads XDG_DATA_HOME too, for its Pythons. Left to follow it, uv installs another
		// Python per user, and since every run shares the checkout's one .venv, each run with a
		// different Python rebuilds it under the others ("No such file or directory").
		env["UV_PYTHON_INSTALL_DIR"] = uvPythonDir()
	}
	if c.BrowserPath != "" {
		env["MFP_BROWSER_PATH"] = c.BrowserPath
	}
	return env
}

// CookiePath is where the MCP keeps this user's MyFitnessPal login.
func (c Client) CookiePath() string {
	configHome := c.ConfigHome
	if configHome == "" {
		configHome = os.Getenv("XDG_CONFIG_HOME")
	}
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "myfitnesspal-mcp", "cookies.json")
}

// NewClientFromEnv reads MFP_MCP_DIR and MFP_BROWSER_PATH.
func NewClientFromEnv() Client {
	return Client{
		CheckoutDir: config.ExpandHome(os.Getenv("MFP_MCP_DIR")),
		BrowserPath: os.Getenv("MFP_BROWSER_PATH"),
	}
}

// Setup says where the MCP comes from and whether it has a saved login.
type Setup struct {
	Source   string  `json:"source"` // "local" or "package"
	Dir      *string `json:"dir"`
	SignedIn bool    `json:"signed_in"`
	Repo     string  `json:"repo"`
}

// Checkout is the local checkout the MCP comes from ("" = the released package).
func (c Client) Checkout() string {
	if c.usesCheckout() {
		return c.CheckoutDir
	}
	return ""
}

func (c Client) usesCheckout() bool {
	if c.CheckoutDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(c.CheckoutDir, "pyproject.toml"))
	return err == nil
}

// Setup reports the source and whether the MCP's login (~/.config/myfitnesspal-mcp) exists.
func (c Client) Setup() Setup {
	setup := Setup{Source: "package", Repo: RepoURL, SignedIn: c.ConfigHome == "" && os.Getenv("MFP_COOKIE") != ""}
	if c.usesCheckout() {
		dir := c.CheckoutDir
		setup.Source, setup.Dir = "local", &dir
	}
	if _, err := os.Stat(c.CookiePath()); err == nil {
		setup.SignedIn = true
	}
	return setup
}

// MCPServer is how an AI agent starts the MyFitnessPal MCP server.
func (c Client) MCPServer() mcp.Server {
	uv := uvCommand()
	var args []string
	if c.usesCheckout() {
		args = []string{"run", "--python", "3.12", "--managed-python", "--directory", c.CheckoutDir, "--extra", "autorefresh", "mfp-mcp"}
	} else {
		args = []string{"tool", "run", "--python", "3.12", "--managed-python", "--from", Package, "mfp-mcp"}
	}
	server := mcp.Server{Command: uv[0], Args: append(uv[1:], args...)}
	if env := c.env(); len(env) > 0 {
		server.Env = env
	}
	return server
}

// call runs op signed in. When MyFitnessPal no longer accepts the login, the MCP's headless
// browser renews it (when set up) and op runs once more: an auth failure means nothing was done.
func (c Client) call(timeout time.Duration, op func(ctx context.Context) error) error {
	if !c.Setup().SignedIn {
		return problem.Unavailablef("MyFitnessPal isn't connected yet. Connect it on the Account page.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := op(ctx)
	if errors.Is(err, errSignedOut) && c.renewLogin(ctx) {
		err = op(ctx)
	}
	return err
}

// renewLogin has the MCP revisit MyFitnessPal in its browser profile and save the fresh cookies.
// It reports whether the saved login changed.
func (c Client) renewLogin(ctx context.Context) bool {
	before, _ := os.Stat(c.CookiePath())
	cmd := c.command(ctx, "python", "-c", "from myfitnesspal_mcp import refresh; refresh.refresh_session()")
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("renewing the MyFitnessPal login: %s", text.LastLine(string(output), err.Error()))
		return false
	}
	after, err := os.Stat(c.CookiePath())
	return err == nil && (before == nil || !after.ModTime().Equal(before.ModTime()))
}

// command runs program inside the MCP package's environment (uv run), with the user's own
// login folders.
func (c Client) command(ctx context.Context, program ...string) *exec.Cmd {
	where := []string{"--no-project", "--with", Package}
	if c.usesCheckout() {
		where = []string{"--directory", c.CheckoutDir, "--extra", "autorefresh"}
	}
	uv := uvCommand()
	args := append(uv[1:], "run", "--quiet")
	args = append(append(append(args, pythonArgs()...), where...), program...)
	cmd := exec.CommandContext(ctx, uv[0], args...)
	cmd.Env = os.Environ()
	if lib := os.Getenv("MFP_LIBRARY_PATH"); lib != "" {
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH="+lib)
	}
	if c.ConfigHome != "" {
		// Another user's MFP_COOKIE from the server's environment mustn't sign this one in.
		cmd.Env = slices.DeleteFunc(cmd.Env, func(v string) bool { return strings.HasPrefix(v, "MFP_COOKIE=") })
	}
	for key, value := range c.env() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

// ErrNeedsUsername is Connect's answer when the cookie works but MyFitnessPal won't say whose
// it is (it happens to some accounts that sign in by email): try again with the username.
var ErrNeedsUsername = problem.Invalidf("Your cookie works, but MyFitnessPal didn't say which account it's for. Enter your MyFitnessPal username (not your email) and connect again.")

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,50}$`)

// Connect signs the user in with their browser's MyFitnessPal cookies, from whatever they pasted
// (CookieFromPaste: they must include remember_me; the session token alone doesn't sign in), through the MCP's own `auth`,
// which checks it with MyFitnessPal before saving it in the user's folder. username is only
// needed after ErrNeedsUsername; the MCP saves it with the login.
func (c Client) Connect(token, username string) error {
	username = strings.TrimSpace(username)
	token, err := CookieFromPaste(token)
	if err != nil {
		return err
	}
	if len(token) > 32000 {
		return problem.Invalidf("That's too long to be MyFitnessPal's cookies. Copy just the first request in the Network list.")
	}
	if username != "" && !usernamePattern.MatchString(username) {
		return problem.Invalidf("That doesn't look like a MyFitnessPal username: use the one on your profile, not your email")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := c.command(ctx, "mfp-mcp", "auth")
	cmd.Stdin = strings.NewReader(token + "\n")
	// Unbuffered, so the output keeps its order: buffered stdout ("Validating…") would
	// otherwise land after the reason on stderr and hide it.
	cmd.Env = append(cmd.Env, "PYTHONUNBUFFERED=1")
	if username != "" {
		cmd.Env = append(cmd.Env, "MFP_USERNAME="+username)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		reason := authFailure(output.String(), err)
		if strings.Contains(reason, "couldn't read your MyFitnessPal profile") {
			return ErrNeedsUsername
		}
		return problem.Invalidf("MyFitnessPal didn't accept that token: " + reason)
	}
	c.session().reset()
	return nil
}

// authFailure is the reason `mfp-mcp auth` gives for refusing a token.
func authFailure(output string, err error) string {
	for _, line := range strings.Split(output, "\n") {
		if _, reason, found := strings.Cut(line, "didn't authenticate:"); found {
			return strings.TrimSpace(reason)
		}
	}
	return text.LastLine(output, err.Error())
}

// Disconnect forgets the user's MyFitnessPal login.
func (c Client) Disconnect() error {
	if c.ConfigHome == "" {
		return problem.Invalidf("This account uses the server computer's own MyFitnessPal login")
	}
	c.session().reset()
	for _, path := range []string{filepath.Dir(c.CookiePath()), filepath.Join(c.DataHome, "myfitnesspal-mcp")} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// uvPythonDir is where uv keeps its Pythons for the server's own account: UV_PYTHON_INSTALL_DIR,
// or $XDG_DATA_HOME/uv/python, or ~/.local/share/uv/python.
func uvPythonDir() string {
	if dir := os.Getenv("UV_PYTHON_INSTALL_DIR"); dir != "" {
		return dir
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "uv", "python")
}

// pythonArgs picks the MCP's Python: Nix's python3.12 when it's on the PATH (nix run and the
// container put it there, as the AI's sandbox uses), otherwise one uv downloads. A downloaded
// Python can't run in the container: it has no /bin/sh for uv to read the libc from, and no
// system loader to start it with.
func pythonArgs() []string {
	if python, err := exec.LookPath("python3.12"); err == nil {
		return []string{"--python", python}
	}
	return []string{"--python", "3.12", "--managed-python"}
}

// uvCommand is uv from the PATH, or from Nix when it isn't installed.
func uvCommand() []string {
	if _, err := exec.LookPath("uv"); err == nil {
		return []string{"uv"}
	}
	return []string{"nix", "run", "nixpkgs#uv", "--"}
}

// SearchQuery cleans up a food search: one line, 1-100 characters.
func SearchQuery(query string) (string, bool) {
	query = strings.Join(strings.Fields(query), " ")
	return query, query != "" && len([]rune(query)) <= 100
}

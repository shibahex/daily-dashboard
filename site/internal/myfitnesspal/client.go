// Package myfitnesspal talks to MyFitnessPal through the MyFitnessPal MCP (mfp-mcp), whose
// Python package does the signed-in requests. site/mfp_fetch.py runs inside that package and
// answers one JSON request per run.
package myfitnesspal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// Client runs mfp_fetch.py, either in a local checkout of the MCP or with the released package.
type Client struct {
	CheckoutDir string // MFP_MCP_DIR: a local checkout; empty uses the released package
	BrowserPath string // MFP_BROWSER_PATH: the browser the MCP renews the login with
	ScriptPath  string // site/mfp_fetch.py

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
func NewClientFromEnv(scriptPath string) Client {
	return Client{
		CheckoutDir: config.ExpandHome(os.Getenv("MFP_MCP_DIR")),
		BrowserPath: os.Getenv("MFP_BROWSER_PATH"),
		ScriptPath:  scriptPath,
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

// Run sends one request to mfp_fetch.py ({"op": "fetch"}, {"op": "search", ...}) and returns
// its JSON answer.
func (c Client) Run(request map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if !c.Setup().SignedIn {
		return nil, errors.New("MyFitnessPal isn't connected yet. Connect it on the Account page.")
	}
	requestJSON, _ := json.Marshal(request)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := c.command(ctx, "python", c.ScriptPath, string(requestJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New("MyFitnessPal took too long to answer")
	}
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return nil, errors.New("uv isn't available (start the dashboard with nix run)")
	}

	// The answer is the last line; anything before it is the package's own output.
	answer := json.RawMessage(text.LastLine(stdout.String(), ""))
	var fields map[string]json.RawMessage
	if json.Unmarshal(answer, &fields) != nil {
		return nil, errors.New(text.LastLine(stderr.String(), "MyFitnessPal sync failed"))
	}
	if problem, failed := fields["error"]; failed {
		var message string
		json.Unmarshal(problem, &message)
		return nil, errors.New(message)
	}
	return answer, nil
}

// command runs program inside the MCP package's environment (uv run), with the user's own
// login folders.
func (c Client) command(ctx context.Context, program ...string) *exec.Cmd {
	where := []string{"--no-project", "--with", Package}
	if c.usesCheckout() {
		where = []string{"--directory", c.CheckoutDir, "--extra", "autorefresh"}
	}
	uv := uvCommand()
	args := append(uv[1:], "run", "--quiet", "--python", "3.12", "--managed-python")
	args = append(append(args, where...), program...)
	cmd := exec.CommandContext(ctx, uv[0], args...)
	cmd.Env = os.Environ()
	if c.ConfigHome != "" {
		// Another user's MFP_COOKIE from the server's environment mustn't sign this one in.
		cmd.Env = slices.DeleteFunc(cmd.Env, func(v string) bool { return strings.HasPrefix(v, "MFP_COOKIE=") })
	}
	for key, value := range c.env() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

// Connect signs the user in with their browser's MyFitnessPal Cookie header (it must include
// remember_me; the session token alone no longer signs in), through the MCP's own `auth`,
// which checks it with MyFitnessPal before saving it in the user's folder.
func (c Client) Connect(token string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 32000 || strings.ContainsAny(token, "\r\n") {
		return problem.Invalidf("Paste the whole Cookie header from a myfitnesspal.com request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := c.command(ctx, "mfp-mcp", "auth")
	cmd.Stdin = strings.NewReader(token + "\n")
	// Unbuffered, so the output keeps its order: buffered stdout ("Validating…") would
	// otherwise land after the reason on stderr and hide it.
	cmd.Env = append(cmd.Env, "PYTHONUNBUFFERED=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return problem.Invalidf("MyFitnessPal didn't accept that token: " + authFailure(output.String(), err))
	}
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
	for _, path := range []string{filepath.Dir(c.CookiePath()), filepath.Join(c.DataHome, "myfitnesspal-mcp")} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
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

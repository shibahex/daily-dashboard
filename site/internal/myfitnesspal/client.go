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
	"strings"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/mcp"
	"dashboard/internal/text"
)

const (
	Package = "mfp-mcp[autorefresh]"
	RepoURL = "https://github.com/Mason-Levyy/myfitnesspal-mcp"
)

// Client runs mfp_fetch.py, either in a local checkout of the MCP or with the released package.
type Client struct {
	CheckoutDir string // MFP_MCP_DIR: a local checkout; empty uses the released package
	BrowserPath string // MFP_BROWSER_PATH: the browser the MCP renews the login with
	ScriptPath  string // site/mfp_fetch.py
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

func (c Client) usesCheckout() bool {
	if c.CheckoutDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(c.CheckoutDir, "pyproject.toml"))
	return err == nil
}

// Setup reports the source and whether the MCP's login (~/.config/myfitnesspal-mcp) exists.
func (c Client) Setup() Setup {
	setup := Setup{Source: "package", Repo: RepoURL, SignedIn: os.Getenv("MFP_COOKIE") != ""}
	if c.usesCheckout() {
		dir := c.CheckoutDir
		setup.Source, setup.Dir = "local", &dir
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	if _, err := os.Stat(filepath.Join(configHome, "myfitnesspal-mcp", "cookies.json")); err == nil {
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
	if c.BrowserPath != "" {
		server.Env = map[string]string{"MFP_BROWSER_PATH": c.BrowserPath}
	}
	return server
}

// Run sends one request to mfp_fetch.py ({"op": "fetch"}, {"op": "search", ...}) and returns
// its JSON answer.
func (c Client) Run(request map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if !c.Setup().SignedIn {
		return nil, errors.New("MyFitnessPal isn't connected yet. Set up the MyFitnessPal MCP (see the MCPs tab).")
	}
	requestJSON, _ := json.Marshal(request)
	where := []string{"--no-project", "--with", Package}
	if c.usesCheckout() {
		where = []string{"--directory", c.CheckoutDir, "--extra", "autorefresh"}
	}
	uv := uvCommand()
	args := append(uv[1:], "run", "--quiet", "--python", "3.12", "--managed-python")
	args = append(args, where...)
	args = append(args, "python", c.ScriptPath, string(requestJSON))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, uv[0], args...)
	cmd.Env = os.Environ()
	if c.BrowserPath != "" {
		cmd.Env = append(cmd.Env, "MFP_BROWSER_PATH="+c.BrowserPath)
	}
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

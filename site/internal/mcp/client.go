package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client talks to one MCP server over stdio (JSON-RPC 2.0, one message a line): the dashboard
// runs the user's MCP tools for an AI that runs in their browser (see web/js/assistant.js).
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  *bufio.Reader
	stderr limitedWriter // the last of what the server wrote to stderr

	mu     sync.Mutex // one request at a time
	nextID int
}

// Tool is one tool an MCP server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Start runs args (args[0] is the program) with env and says hello (initialize).
func Start(args, env []string, dir string) (*Client, error) {
	if len(args) == 0 {
		return nil, errors.New("mcp: nothing to run")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env, cmd.Dir = env, dir
	c := &Client{cmd: cmd}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.stderr.max = 8 << 10
	cmd.Stderr = &c.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.stdin, c.lines = stdin, bufio.NewReaderSize(stdout, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "dashboard", "version": "1"},
	}); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Tools lists the server's tools.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return tools, nil
		}
		cursor = page.NextCursor
	}
}

// Result is a tool call's answer: its text content, and whether the tool reports an error.
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError"`
}

// Call runs a tool with its input (a JSON object).
func (c *Client) Call(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": input})
	if err != nil {
		return Result{}, err
	}
	var answer struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return Result{}, err
	}
	var parts []string
	for _, part := range answer.Content {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		} else {
			parts = append(parts, "["+part.Type+" content]")
		}
	}
	if len(parts) == 0 && len(answer.StructuredContent) > 0 {
		parts = append(parts, string(answer.StructuredContent))
	}
	return Result{Text: strings.Join(parts, "\n"), IsError: answer.IsError}, nil
}

// Close stops the server.
func (c *Client) Close() {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
		c.cmd.Wait()
	}
}

func (c *Client) send(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// request sends one request and waits for its answer, skipping the server's own messages
// (notifications, logs) meanwhile.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, c.failure(err)
	}
	type answer struct {
		result json.RawMessage
		err    error
	}
	got := make(chan answer, 1)
	go func() {
		for {
			line, err := c.lines.ReadBytes('\n')
			if err != nil {
				got <- answer{err: c.failure(err)}
				return
			}
			var message struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &message) != nil || message.ID == nil || *message.ID != id || message.Method != "" {
				continue
			}
			if message.Error != nil {
				got <- answer{err: errors.New(message.Error.Message)}
			} else {
				got <- answer{result: message.Result}
			}
			return
		}
	}()
	select {
	case a := <-got:
		return a.result, a.err
	case <-ctx.Done():
		c.Close() // the reader can't be left waiting on a server that may answer later
		return nil, fmt.Errorf("%s took too long", method)
	}
}

// failure is err with the last thing the server wrote to stderr, which usually says why.
func (c *Client) failure(err error) error {
	if lines := strings.Split(strings.TrimSpace(c.stderr.String()), "\n"); lines[len(lines)-1] != "" {
		return fmt.Errorf("the MCP server stopped: %s", lines[len(lines)-1])
	}
	return fmt.Errorf("the MCP server stopped: %v", err)
}

// limitedWriter keeps the first max bytes written to it, safe to read while it's written.
type limitedWriter struct {
	mu  sync.Mutex
	b   strings.Builder
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.Len() < w.max {
		w.b.Write(p[:min(len(p), w.max-w.b.Len())])
	}
	return len(p), nil
}

func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

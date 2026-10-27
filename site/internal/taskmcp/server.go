package taskmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// CallFunc runs one tool call. A tool that fails answers with IsError set; the error is for a
// call that couldn't be made at all.
type CallFunc func(tool string, arguments json.RawMessage) (Result, error)

// Server is an MCP server that only has tools: its name and the tools it offers.
type Server struct {
	Name  string
	Tools []Tool
}

// Taskwarrior is this package's server: the tasks_* tools.
var Taskwarrior = Server{Name: "taskwarrior", Tools: Tools}

func (s Server) has(name string) bool {
	for _, t := range s.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// Serve speaks MCP over stdio for the Taskwarrior tools (see Server.Serve).
func Serve(in io.Reader, out io.Writer, call CallFunc) error { return Taskwarrior.Serve(in, out, call) }

// Serve speaks MCP over stdio (JSON-RPC 2.0, one message a line) until in ends: initialize, ping,
// tools/list and tools/call, which it passes to call. It's all an AI's MCP client needs of a
// server that only has tools.
func (s Server) Serve(in io.Reader, out io.Writer, call CallFunc) error {
	lines := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := lines.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if reply := s.handle(bytes.TrimSpace(line), call); reply != nil {
				if _, werr := out.Write(append(reply, '\n')); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// handle answers one message (nil for a notification, which gets no answer).
func (s Server) handle(message []byte, call CallFunc) []byte {
	var request rpcRequest
	if err := json.Unmarshal(message, &request); err != nil {
		return rpcError(json.RawMessage("null"), -32700, "parse error")
	}
	if len(request.ID) == 0 { // a notification (notifications/initialized...)
		return nil
	}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(request.Params, &params)
		if params.ProtocolVersion == "" {
			params.ProtocolVersion = "2025-06-18"
		}
		return rpcResult(request.ID, map[string]any{
			"protocolVersion": params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.Name, "version": "1"},
		})
	case "ping":
		return rpcResult(request.ID, map[string]any{})
	case "tools/list":
		return rpcResult(request.ID, map[string]any{"tools": s.Tools})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return rpcError(request.ID, -32602, "bad params")
		}
		if !s.has(params.Name) {
			return rpcError(request.ID, -32602, "unknown tool "+params.Name)
		}
		result, err := call(params.Name, params.Arguments)
		if err != nil {
			result = Result{Text: err.Error(), IsError: true}
		}
		return rpcResult(request.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": result.Text}},
			"isError": result.IsError,
		})
	}
	return rpcError(request.ID, -32601, "method not found: "+request.Method)
}

func rpcResult(id json.RawMessage, result any) []byte {
	reply, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		return rpcError(id, -32603, "internal error: the answer can't be written")
	}
	return reply
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	return reply
}

// RunStdio is `dashboard task-mcp`: the MCP server inside the sandbox. It passes every tool call
// through the gateway's socket (SocketEnv) and returns when the AI closes its input.
func RunStdio() error {
	socket := os.Getenv(SocketEnv)
	if socket == "" {
		return fmt.Errorf("%s isn't set: this program is started by the dashboard's AI, not by hand", SocketEnv)
	}
	return Forward(socket, os.Stdin, os.Stdout)
}

// Forward serves the Taskwarrior tools on in and out (see Server.Forward).
func Forward(socket string, in io.Reader, out io.Writer) error {
	return Taskwarrior.Forward(socket, in, out)
}

// Forward serves MCP on in and out, and passes each tool call to the gateway at socket.
func (s Server) Forward(socket string, in io.Reader, out io.Writer) error {
	client := &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	return s.Serve(in, out, func(tool string, arguments json.RawMessage) (Result, error) {
		body, _ := json.Marshal(map[string]any{"tool": tool, "arguments": arguments})
		response, err := client.Post("http://dashboard/call", "application/json", bytes.NewReader(body))
		if err != nil {
			return Result{}, errors.New("couldn't reach the dashboard")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
		if err != nil || len(data) > maxResponse {
			return Result{}, errors.New("the dashboard's answer was unreadable")
		}
		var result Result
		if response.StatusCode != http.StatusOK || json.Unmarshal(data, &result) != nil {
			return Result{}, errors.New("the dashboard couldn't run that")
		}
		return result, nil
	})
}

// maxResponse is the most a tool answer may hold: it goes into the AI's context.
const maxResponse = 256 << 10

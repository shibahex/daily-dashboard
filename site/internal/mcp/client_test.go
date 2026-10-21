package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// TestMain lets the test binary act as a tiny MCP server (FAKE_MCP=1), with one tool, "echo".
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_MCP") != "1" {
		os.Exit(m.Run())
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var request struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(in.Bytes(), &request)
		if request.ID == nil {
			continue // a notification
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "Says it back",
				"inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			var call struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(request.Params, &call)
			// A log notification first, which the client must skip.
			fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"working"}}`)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprint(call.Arguments["say"])}},
				"isError": call.Arguments["say"] == "fail"}
		}
		answer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
		fmt.Println(string(answer))
	}
	os.Exit(0)
}

func TestClient(t *testing.T) {
	c, err := Start([]string{os.Args[0]}, append(os.Environ(), "FAKE_MCP=1"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	result, err := c.Call(ctx, "echo", json.RawMessage(`{"say":"hello"}`))
	if err != nil || result.Text != "hello" || result.IsError {
		t.Fatalf("call = %+v, %v", result, err)
	}
	if result, _ := c.Call(ctx, "echo", json.RawMessage(`{"say":"fail"}`)); !result.IsError {
		t.Error("a tool error should say so")
	}
}

func TestClientReportsAServerThatDies(t *testing.T) {
	if _, err := Start([]string{"sh", "-c", "echo broken config >&2; exit 1"}, nil, ""); err == nil {
		t.Fatal("expected an error")
	} else if got := err.Error(); got != "the MCP server stopped: broken config" {
		t.Errorf("error = %q", got)
	}
}

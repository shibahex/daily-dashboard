package localmodel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer is an OpenAI-style server that records the last chat request and answers with reply.
func fakeServer(t *testing.T, reply string, got *map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"object":"list","data":[{"id":"qwen3:8b"},{"id":"llama3.2"}]}`)
		case "/v1/chat/completions":
			json.NewDecoder(r.Body).Decode(got)
			io.WriteString(w, reply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestModels(t *testing.T) {
	server := fakeServer(t, "", nil)
	models, err := Server{Base: server.URL + "/v1", Key: "secret"}.Models(t.Context())
	if err != nil || strings.Join(models, ",") != "qwen3:8b,llama3.2" {
		t.Fatalf("got %v, %v", models, err)
	}
	if _, err := (Server{Base: server.URL + "/v1"}).Models(t.Context()); err == nil {
		t.Fatal("expected an error without the key")
	}
}

func TestParseBase(t *testing.T) {
	for input, want := range map[string]string{
		"localhost:11434":                "http://localhost:11434/v1",
		"http://192.168.1.5:1234/v1/":    "http://192.168.1.5:1234/v1",
		"https://llm.example.com/api/v1": "https://llm.example.com/api/v1",
		"https://llm.example.com":        "https://llm.example.com/v1",
	} {
		if got, err := ParseBase(input); err != nil || got != want {
			t.Errorf("ParseBase(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"ftp://x", "http://user:pw@host", "://"} {
		if _, err := ParseBase(bad); err == nil {
			t.Errorf("ParseBase(%q) should fail", bad)
		}
	}
}

// A conversation with a tool round trip translates to chat messages in the right order.
func TestRequestTranslation(t *testing.T) {
	var in anthropicRequest
	json.Unmarshal([]byte(`{
		"model": "qwen3:8b", "max_tokens": 100, "stream": true,
		"system": [{"type": "text", "text": "Be brief."}],
		"tools": [{"name": "get_day", "description": "A day", "input_schema": {"type": "object"}},
		          {"type": "web_search_20250305", "name": "web_search"}],
		"messages": [
			{"role": "user", "content": "What did I eat?"},
			{"role": "assistant", "content": [{"type": "text", "text": "Checking."},
				{"type": "tool_use", "id": "t1", "name": "get_day", "input": {"date": "today"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": [{"type": "text", "text": "eggs"}]}]}
		]}`), &in)
	out := toOpenAI(in)
	encoded, _ := json.Marshal(out)
	var got struct {
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
	}
	json.Unmarshal(encoded, &got)

	roles := []string{}
	for _, m := range got.Messages {
		roles = append(roles, m["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("roles %v", roles)
	}
	call := got.Messages[2]["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "t1" || call["function"].(map[string]any)["arguments"] != `{"date":"today"}` {
		t.Errorf("tool call %v", call)
	}
	if got.Messages[3]["tool_call_id"] != "t1" || got.Messages[3]["content"] != "eggs" {
		t.Errorf("tool result %v", got.Messages[3])
	}
	if len(got.Tools) != 1 {
		t.Errorf("Anthropic's own server tools should be left out: %v", got.Tools)
	}
}

func post(t *testing.T, bridge Bridge, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	bridge.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(body)))
	return recorder
}

// A streamed answer with text and a tool call (its arguments in pieces) comes out as
// Anthropic's events.
func TestStream(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"role":"assistant","content":"Let me "}}]}`,
		`{"choices":[{"delta":{"content":"look."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_day","arguments":"{\"da"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"te\":\"today\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
	}
	reply := ""
	for _, c := range chunks {
		reply += "data: " + c + "\n\n"
	}
	reply += "data: [DONE]\n\n"
	var sent map[string]any
	server := fakeServer(t, reply, &sent)
	recorder := post(t, Bridge{Server{Base: server.URL + "/v1"}}, `{"model":"qwen3:8b","stream":true,"max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)

	if sent["model"] != "qwen3:8b" || sent["stream"] != true {
		t.Fatalf("sent %v", sent)
	}
	var events []string
	text, args := "", ""
	stop := ""
	for _, block := range strings.Split(recorder.Body.String(), "\n\n") {
		event, data, ok := strings.Cut(block, "\ndata: ")
		if !ok {
			continue
		}
		events = append(events, strings.TrimPrefix(event, "event: "))
		var payload struct {
			Delta struct {
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		json.Unmarshal([]byte(data), &payload)
		text += payload.Delta.Text
		args += payload.Delta.PartialJSON
		if payload.Delta.StopReason != "" {
			stop = payload.Delta.StopReason
		}
	}
	want := "message_start,content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(events, ",") != want {
		t.Errorf("events %v", events)
	}
	if text != "Let me look." || args != `{"date":"today"}` || stop != "tool_use" {
		t.Errorf("text %q, args %q, stop %q", text, args, stop)
	}
}

func TestWhole(t *testing.T) {
	var sent map[string]any
	server := fakeServer(t, `{"id":"x","choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, &sent)
	recorder := post(t, Bridge{Server{Base: server.URL + "/v1"}}, `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	var got struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &got)
	if len(got.Content) != 1 || got.Content[0].Text != "Hello" || got.StopReason != "end_turn" {
		t.Errorf("got %s", recorder.Body.String())
	}
}

// The server's own error message reaches Claude Code as an Anthropic error.
func TestUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"model \"nope\" not found"}}`)
	}))
	defer server.Close()
	recorder := post(t, Bridge{Server{Base: server.URL + "/v1"}}, `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `model \"nope\" not found`) {
		t.Errorf("got %d %s", recorder.Code, recorder.Body.String())
	}
}

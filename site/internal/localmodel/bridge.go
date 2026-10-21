package localmodel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Bridge answers Anthropic Messages API requests (POST /v1/messages, from Claude Code in the
// sandbox) by asking the Server's chat completions endpoint, streaming or not. It's served on a
// socket only the sandbox sees, and only ever calls that one server.
type Bridge struct{ Server Server }

// Serve answers requests on listener until it's closed (one per agent run).
func (b Bridge) Serve(listener net.Listener) error {
	server := &http.Server{Handler: b, ReadHeaderTimeout: 30 * time.Second}
	return server.Serve(listener)
}

func (b Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		b.messages(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/count_tokens":
		// Chat completions has no token counter: a rough guess of 4 bytes a token.
		body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		writeJSON(w, http.StatusOK, map[string]int{"input_tokens": len(body) / 4})
	default:
		apiError(w, http.StatusNotFound, "not_found_error", "the local model bridge only answers /v1/messages")
	}
}

// ---------- Anthropic's side ----------

type anthropicRequest struct {
	Model      string             `json:"model"`
	System     json.RawMessage    `json:"system"`
	Messages   []anthropicMessage `json:"messages"`
	Tools      []anthropicTool    `json:"tools"`
	ToolChoice *struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
	MaxTokens     int      `json:"max_tokens"`
	Temperature   *float64 `json:"temperature"`
	TopP          *float64 `json:"top_p"`
	StopSequences []string `json:"stop_sequences"`
	Stream        bool     `json:"stream"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // a string, or a list of blocks
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // a tool result's: a string or blocks
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

type anthropicTool struct {
	Type        string          `json:"type"` // "" or "custom" for a normal tool; others are Anthropic's own server tools
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// blocks reads content that may be a plain string.
func blocks(raw json.RawMessage) []anthropicBlock {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []anthropicBlock{{Type: "text", Text: text}}
	}
	var list []anthropicBlock
	json.Unmarshal(raw, &list)
	return list
}

// plainText joins the text blocks of content that may be a plain string.
func plainText(raw json.RawMessage) string {
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ---------- OpenAI's side ----------

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"` // a string, a list of parts, or null
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// toOpenAI translates the request. Anthropic's server tools (web search…) are left out: a local
// server can't run them.
func toOpenAI(in anthropicRequest) map[string]any {
	var messages []openaiMessage
	if system := plainText(in.System); system != "" {
		messages = append(messages, openaiMessage{Role: "system", Content: system})
	}
	for _, m := range in.Messages {
		if m.Role == "assistant" {
			out := openaiMessage{Role: "assistant"}
			var text []string
			for _, b := range blocks(m.Content) {
				switch b.Type {
				case "text":
					text = append(text, b.Text)
				case "tool_use":
					call := openaiToolCall{ID: b.ID, Type: "function"}
					call.Function.Name = b.Name
					call.Function.Arguments = "{}"
					var compact bytes.Buffer
					if json.Compact(&compact, b.Input) == nil {
						call.Function.Arguments = compact.String()
					}
					out.ToolCalls = append(out.ToolCalls, call)
				}
			}
			if len(text) > 0 {
				out.Content = strings.Join(text, "")
			}
			messages = append(messages, out)
			continue
		}
		// A user turn: tool results become "tool" messages (which must come right after the
		// assistant's calls), and the rest one user message.
		var parts []map[string]any
		for _, b := range blocks(m.Content) {
			switch b.Type {
			case "tool_result":
				result := plainText(b.Content)
				if b.IsError {
					result = "Error: " + result
				}
				messages = append(messages, openaiMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: result})
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			case "image":
				if b.Source == nil {
					continue
				}
				link := b.Source.URL
				if b.Source.Type == "base64" {
					link = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": link}})
			}
		}
		switch {
		case len(parts) == 1 && parts[0]["type"] == "text":
			messages = append(messages, openaiMessage{Role: "user", Content: parts[0]["text"]})
		case len(parts) > 0:
			messages = append(messages, openaiMessage{Role: "user", Content: parts})
		}
	}

	out := map[string]any{"model": in.Model, "messages": messages, "stream": in.Stream}
	if in.Stream {
		out["stream_options"] = map[string]bool{"include_usage": true}
	}
	if in.MaxTokens > 0 {
		out["max_tokens"] = in.MaxTokens
	}
	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		out["top_p"] = *in.TopP
	}
	if len(in.StopSequences) > 0 {
		out["stop"] = in.StopSequences
	}
	var tools []map[string]any
	for _, t := range in.Tools {
		if (t.Type != "" && t.Type != "custom") || len(t.InputSchema) == 0 {
			continue
		}
		tools = append(tools, map[string]any{"type": "function",
			"function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if c := in.ToolChoice; c != nil {
			switch c.Type {
			case "any":
				out["tool_choice"] = "required"
			case "none":
				out["tool_choice"] = "none"
			case "tool":
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": c.Name}}
			}
		}
	}
	return out
}

// stopReason is Anthropic's name for why the answer ended.
func stopReason(finish string, usedTools bool) string {
	switch {
	case usedTools || finish == "tool_calls" || finish == "function_call":
		return "tool_use"
	case finish == "length":
		return "max_tokens"
	}
	return "end_turn"
}

// toolInput is a call's arguments as a JSON object ({} when the model sent nothing usable).
func toolInput(arguments string) json.RawMessage {
	var object map[string]any
	if json.Unmarshal([]byte(arguments), &object) != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(arguments)
}

// ---------- answering ----------

func (b Bridge) messages(w http.ResponseWriter, r *http.Request) {
	var in anthropicRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request_error", "couldn't read the request: "+err.Error())
		return
	}
	body, _ := json.Marshal(toOpenAI(in))
	req, err := b.Server.request(r.Context(), http.MethodPost, "/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		apiError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		apiError(w, http.StatusBadGateway, "api_error", "can't reach your model server at "+b.Server.Base)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4000))
		status := resp.StatusCode
		if status < 400 || status >= 600 {
			status = http.StatusBadGateway
		}
		apiError(w, status, "api_error", fmt.Sprintf("your model server answered %s: %s", resp.Status, upstreamMessage(detail)))
		return
	}
	if in.Stream {
		b.stream(w, in.Model, resp.Body)
	} else {
		b.whole(w, in.Model, resp.Body)
	}
}

// whole answers a request that didn't ask to stream.
func (b Bridge) whole(w http.ResponseWriter, model string, body io.Reader) {
	var answer struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content   string           `json:"content"`
				ToolCalls []openaiToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage openaiUsage `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&answer); err != nil || len(answer.Choices) == 0 {
		apiError(w, http.StatusBadGateway, "api_error", "your model server sent an answer the dashboard can't read")
		return
	}
	choice := answer.Choices[0]
	content := []map[string]any{}
	if choice.Message.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": choice.Message.Content})
	}
	for i, call := range choice.Message.ToolCalls {
		content = append(content, map[string]any{"type": "tool_use", "id": toolID(call.ID, i),
			"name": call.Function.Name, "input": toolInput(call.Function.Arguments)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": messageID(answer.ID), "type": "message", "role": "assistant", "model": model, "content": content,
		"stop_reason": stopReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0), "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": answer.Usage.PromptTokens, "output_tokens": answer.Usage.CompletionTokens},
	})
}

// stream turns chat completion chunks into Anthropic's stream events. Text goes out as it
// comes; tool calls are collected (their arguments arrive in pieces, sometimes interleaved) and
// sent whole at the end, which is all Claude Code needs.
func (b Bridge) stream(w http.ResponseWriter, model string, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}

	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": messageID(""), "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})

	index := 0
	textOpen := false
	var calls []*openaiToolCall // in the order the model started them
	byIndex := map[int]*openaiToolCall{}
	finish := ""
	var usage openaiUsage

	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 64*1024), 8<<20)
	for lines.Scan() {
		data, ok := strings.CutPrefix(lines.Text(), "data:")
		data = strings.TrimSpace(data)
		if !ok || data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string           `json:"content"`
					ToolCalls []openaiToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *openaiUsage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			send("error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": chunk.Error.Message}})
			return
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if text := choice.Delta.Content; text != "" {
				if !textOpen {
					send("content_block_start", map[string]any{"type": "content_block_start", "index": index,
						"content_block": map[string]any{"type": "text", "text": ""}})
					textOpen = true
				}
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
					"delta": map[string]any{"type": "text_delta", "text": text}})
			}
			for i, piece := range choice.Delta.ToolCalls {
				at := i
				if piece.Index != nil {
					at = *piece.Index
				}
				call, seen := byIndex[at]
				if !seen {
					call = &openaiToolCall{}
					byIndex[at] = call
					calls = append(calls, call)
				}
				if piece.ID != "" {
					call.ID = piece.ID
				}
				call.Function.Name += piece.Function.Name
				call.Function.Arguments += piece.Function.Arguments
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
	}

	if textOpen {
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	for i, call := range calls {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": index,
			"content_block": map[string]any{"type": "tool_use", "id": toolID(call.ID, i), "name": call.Function.Name, "input": map[string]any{}}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(toolInput(call.Function.Arguments))}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	send("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stopReason(finish, len(calls) > 0), "stop_sequence": nil},
		"usage": map[string]int{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

// ---------- small pieces ----------

// toolID is the model's id for a call, or one made up (some servers send none).
func toolID(id string, i int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("toolu_local_%d_%d", time.Now().UnixNano(), i)
}

func messageID(id string) string {
	if id != "" {
		return "msg_" + id
	}
	return fmt.Sprintf("msg_local_%d", time.Now().UnixNano())
}

// upstreamMessage is the error message in an OpenAI-style error body, or the body itself.
func upstreamMessage(body []byte) string {
	var answer struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &answer) == nil {
		switch e := answer.Error.(type) {
		case string:
			return e
		case map[string]any:
			if message, ok := e["message"].(string); ok {
				return message
			}
		}
	}
	return strings.TrimSpace(string(body))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
}

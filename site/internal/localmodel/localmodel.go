// Package localmodel connects the MCPs tab to a model server the user runs themselves (Ollama,
// llama.cpp's llama-server, LM Studio, vLLM…) through the OpenAI-style API they all share:
// GET /models for the model list and POST /chat/completions for answers. The dashboard never
// runs a model; it only talks to the address the user saved.
//
// The agent is Claude Code, which speaks Anthropic's Messages API, so Bridge translates its
// requests to chat completions and the answers back.
package localmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Server is the user's model server: its base URL (with the /v1 part, as "http://localhost:11434/v1")
// and an optional API key.
type Server struct {
	Base string
	Key  string
}

// client talks to the server directly (never through a proxy) with no overall timeout: an
// answer from a slow local model can take minutes. Requests carry their own deadlines.
var client = &http.Client{Transport: &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       time.Minute,
}}

func (s Server) request(ctx context.Context, method, path string, body *strings.Reader) (*http.Request, error) {
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, s.Base+path, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, s.Base+path, body)
		req.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		return nil, err
	}
	if s.Key != "" {
		req.Header.Set("Authorization", "Bearer "+s.Key)
	}
	return req, nil
}

// Models asks the server which models it has (GET /models).
func (s Server) Models(ctx context.Context) ([]string, error) {
	req, err := s.request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach %s", s.Base)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", s.Base+"/models", resp.Status)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("%s didn't send a model list", s.Base+"/models")
	}
	models := []string{}
	for _, m := range list.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// ---------- a short-lived cache, so pages don't wait on the server every time ----------

type cached struct {
	models []string
	err    error
	at     time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[Server]cached{}
)

const cacheAge = time.Minute

// CachedModels is Models, remembered for a minute (failures too, so a server that's off doesn't
// slow every page). fresh skips the cache.
func (s Server) CachedModels(fresh bool) ([]string, error) {
	cacheMu.Lock()
	entry, ok := cache[s]
	cacheMu.Unlock()
	if ok && !fresh && time.Since(entry.at) < cacheAge {
		return entry.models, entry.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	models, err := s.Models(ctx)
	cacheMu.Lock()
	cache[s] = cached{models: models, err: err, at: time.Now()}
	cacheMu.Unlock()
	return models, err
}

// ParseBase turns what the user typed ("localhost:11434", "192.168.1.5:1234",
// "https://llm.example.com/v1") into the base URL the API lives under. Without a path, it's /v1,
// where Ollama, llama.cpp and LM Studio all put it.
func ParseBase(input string) (string, error) {
	input = strings.TrimSpace(input)
	if !strings.Contains(input, "://") {
		input = "http://" + input
	}
	u, err := url.Parse(input)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("that doesn't look like an address, try localhost:11434")
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/v1"
	}
	return u.Scheme + "://" + u.Host + path, nil
}

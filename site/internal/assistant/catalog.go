package assistant

import (
	"cmp"
	"context"
	"dashboard/internal/problem"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// ListModels asks the AI's maker which models the user's key can use: Anthropic for Claude Code
// (an API key, or a subscription token), OpenAI for Codex, Google for Gemini CLI. Only models
// that chat are listed, newest first where the maker says.
func ListModels(ctx context.Context, provider, key string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch provider {
	case "claude-code":
		return anthropicModels(ctx, key)
	case "codex":
		return openAIModels(ctx, key)
	case "gemini":
		return geminiModels(ctx, key)
	}
	return nil, problem.Invalidf(fmt.Sprintf("unknown AI %q", provider))
}

var catalogClient = &http.Client{Timeout: 20 * time.Second}

// The makers' APIs (a test points them elsewhere).
var (
	anthropicAPI = "https://api.anthropic.com"
	openAIAPI    = "https://api.openai.com"
	geminiAPI    = "https://generativelanguage.googleapis.com"
)

func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := catalogClient.Do(req)
	if err != nil {
		return problem.Unavailablef("Couldn't reach " + req.URL.Host)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return problem.Invalidf(req.URL.Host + " didn't accept the key")
		}
		return problem.Failedf(fmt.Sprintf("%s answered %d", req.URL.Host, resp.StatusCode))
	}
	return json.Unmarshal(body, out)
}

func anthropicModels(ctx context.Context, key string) ([]Model, error) {
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if strings.HasPrefix(key, "sk-ant-oat") {
		// A subscription token signs in like Claude Code does.
		headers["Authorization"] = "Bearer " + key
		headers["anthropic-beta"] = "oauth-2025-04-20"
	} else {
		headers["x-api-key"] = key
	}
	var answer struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := getJSON(ctx, anthropicAPI+"/v1/models?limit=1000", headers, &answer); err != nil {
		return nil, err
	}
	var models []Model
	for _, m := range answer.Data { // newest first
		models = append(models, Model{ID: m.ID, Label: cmp.Or(m.DisplayName, m.ID)})
	}
	return models, nil
}

// openAIChat picks OpenAI's models that Codex can talk with: GPT, the o-series and Codex's own,
// without audio, image, search or realtime variants.
func openAIChat(id string) bool {
	if !(strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "codex") || len(id) > 1 && id[0] == 'o' && id[1] >= '1' && id[1] <= '9') {
		return false
	}
	for _, skip := range []string{"audio", "realtime", "tts", "transcribe", "image", "search", "instruct", "embedding", "moderation"} {
		if strings.Contains(id, skip) {
			return false
		}
	}
	return true
}

func openAIModels(ctx context.Context, key string) ([]Model, error) {
	var answer struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := getJSON(ctx, openAIAPI+"/v1/models", map[string]string{"Authorization": "Bearer " + key}, &answer); err != nil {
		return nil, err
	}
	slices.SortFunc(answer.Data, func(a, b struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
	}) int {
		return cmp.Compare(b.Created, a.Created)
	})
	var models []Model
	for _, m := range answer.Data {
		if openAIChat(m.ID) {
			models = append(models, Model{ID: m.ID, Label: m.ID})
		}
	}
	return models, nil
}

func geminiModels(ctx context.Context, key string) ([]Model, error) {
	var answer struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := getJSON(ctx, geminiAPI+"/v1beta/models?pageSize=1000", map[string]string{"x-goog-api-key": key}, &answer); err != nil {
		return nil, err
	}
	var models []Model
	for _, m := range answer.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if strings.HasPrefix(id, "gemini") && slices.Contains(m.Methods, "generateContent") &&
			!strings.Contains(id, "embedding") && !strings.Contains(id, "image") && !strings.Contains(id, "tts") {
			models = append(models, Model{ID: id, Label: cmp.Or(m.DisplayName, id)})
		}
	}
	return models, nil
}

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIChatModelsOnly(t *testing.T) {
	for id, want := range map[string]bool{
		"gpt-5.5": true, "o3": true, "o4-mini": true, "codex-mini-latest": true,
		"gpt-4o-audio-preview": false, "gpt-realtime": false, "gpt-image-1": false, "text-embedding-3-large": false,
		"omni-moderation-latest": false, "dall-e-3": false, "gpt-4o-search-preview": false,
	} {
		if got := openAIChat(id); got != want {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
}

func TestListModelsAsksEachMaker(t *testing.T) {
	var headers http.Header
	maker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") == "Bearer sk-openai" {
				w.Write([]byte(`{"data":[{"id":"gpt-old","created":1},{"id":"gpt-new","created":2},{"id":"gpt-realtime","created":3}]}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5"}]}`))
		case "/v1beta/models":
			w.Write([]byte(`{"models":[{"name":"models/gemini-3-pro","displayName":"Gemini 3 Pro","supportedGenerationMethods":["generateContent"]},
				{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`))
		}
	}))
	defer maker.Close()
	anthropicAPI, openAIAPI, geminiAPI = maker.URL, maker.URL, maker.URL

	models, err := ListModels(context.Background(), "claude-code", "sk-ant-oat01-x")
	if err != nil || len(models) != 1 || models[0] != (Model{ID: "claude-opus-5-5", Label: "Claude Opus 5.5"}) {
		t.Fatalf("anthropic: %v %v", models, err)
	}
	if headers.Get("Authorization") != "Bearer sk-ant-oat01-x" || headers.Get("anthropic-beta") == "" || headers.Get("x-api-key") != "" {
		t.Errorf("a subscription token signs in as a bearer: %v", headers)
	}
	ListModels(context.Background(), "claude-code", "sk-ant-api03-x")
	if headers.Get("x-api-key") != "sk-ant-api03-x" {
		t.Errorf("an API key goes in x-api-key: %v", headers)
	}
	models, _ = ListModels(context.Background(), "codex", "sk-openai")
	if len(models) != 2 || models[0].ID != "gpt-new" {
		t.Errorf("openai: newest chat models only: %v", models)
	}
	models, _ = ListModels(context.Background(), "gemini", "AIza")
	if len(models) != 1 || models[0].ID != "gemini-3-pro" || headers.Get("x-goog-api-key") != "AIza" {
		t.Errorf("gemini: %v", models)
	}
}

func TestReadOnlyTool(t *testing.T) {
	for name, want := range map[string]bool{
		"myfitnesspal__fitness_get_day": true, "fitness_search_food": true, "fitness_log_food": false,
		"anki__findNotes": true, "anki__deckNames": true, "anki__addNote": false, "anki__deleteDecks": false,
		"tasks_add": false, "getaway_car": false,
	} {
		if got := ReadOnlyTool(name, false); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	if !ReadOnlyTool("anything", true) {
		t.Error("the MCP's own hint wins")
	}
}

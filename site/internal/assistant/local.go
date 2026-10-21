package assistant

import "errors"

// local runs Claude Code against the user's own model server (Ollama, llama.cpp's llama-server,
// LM Studio…). Those speak the OpenAI-style chat completions API, so Claude Code talks to the
// dashboard's bridge (internal/localmodel) on a private socket in the sandbox, which translates.
// Everything else is Claude Code's: the MCP servers, tools and event stream. The dashboard
// never runs the model.
type local struct{ *claude }

func newLocal() *local {
	c := newClaude(nil)
	c.id, c.label = "local", "Local model"
	c.signIn = "Add your local server's address and model on the Account page."
	return &local{c}
}

// The menu lists what the user's own server has, which the page asks it for (web/chat.go).
func (l *local) Models() []Model { return nil }

func (l *local) Invocation(req Request) (Invocation, error) {
	if req.Model == "" {
		return Invocation{}, errors.New("pick a model: set one on the Account page, or type one in the model menu")
	}
	// Claude Code also asks for its small "haiku" model in the background and knows only Claude
	// names, so every role is the one local model.
	for _, role := range []string{"OPUS", "SONNET", "HAIKU"} {
		req.Env = append(req.Env, "ANTHROPIC_DEFAULT_"+role+"_MODEL="+req.Model)
	}
	req.Env = append(req.Env, "ANTHROPIC_SMALL_FAST_MODEL="+req.Model, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	return l.claude.Invocation(req)
}

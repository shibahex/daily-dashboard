package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ModelCatalog is Claude Code's model menu (aliases like "opus" plus older full names). Claude
// Code reports it in its SDK "initialize" handshake, which needs no API call. The list is
// cached and refreshed in the background; until it arrives, Fallback is used.
type ModelCatalog struct {
	workDir    string
	mu         sync.Mutex
	models     []Model
	fetchedAt  time.Time
	refreshing bool
}

const catalogMaxAge = 6 * time.Hour

// Fallback is the newest models, used until Claude Code's own list is known.
var Fallback = []Model{
	{ID: "opus", Label: "Opus 5.5"}, {ID: "sonnet", Label: "Sonnet 5.5"},
	{ID: "fable", Label: "Fable 5.1"}, {ID: "haiku", Label: "Haiku 5.5"},
}

func NewModelCatalog(workDir string) *ModelCatalog { return &ModelCatalog{workDir: workDir} }

// Models returns the cached list and starts a refresh when it's missing or old.
func (c *ModelCatalog) Models() []Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.refreshing && time.Since(c.fetchedAt) > catalogMaxAge {
		c.refreshing = true
		go c.refresh()
	}
	if c.models == nil {
		return Fallback
	}
	return c.models
}

func (c *ModelCatalog) refresh() {
	models := fetchClaudeModels(c.workDir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if models != nil {
		c.models = models
	}
	c.fetchedAt, c.refreshing = time.Now(), false
}

// fetchClaudeModels asks Claude Code for its model menu, or returns nil.
func fetchClaudeModels(workDir string) []Model {
	program := newClaude(nil).command()
	if program == nil {
		return nil
	}
	os.MkdirAll(workDir, 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := append(program[1:], "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "")
	cmd := exec.CommandContext(ctx, program[0], args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(`{"type":"control_request","request_id":"models","request":{"subtype":"initialize"}}` + "\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run()

	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		var answer struct {
			Type     string `json:"type"`
			Response struct {
				Response struct {
					Models []struct {
						Value       string `json:"value"`
						DisplayName string `json:"displayName"`
						Description string `json:"description"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &answer) != nil || answer.Type != "control_response" {
			continue
		}
		var models []Model
		for _, m := range answer.Response.Response.Models {
			// "default" is the CLI's own default, which the menu already offers as "Default model".
			if m.Value == "" || m.Value == "default" {
				continue
			}
			label := m.DisplayName
			if label == "" {
				label = m.Value
			}
			models = append(models, Model{ID: m.Value, Label: label, Description: m.Description})
		}
		return models
	}
	return nil
}

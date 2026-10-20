package assistant

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"dashboard/internal/text"
)

// ToolLabel is a tool call as the page shows it:
// mcp__myfitnesspal__fitness_log_food → "log food", mcp__anki__addNote → "add note".
func ToolLabel(toolName string) string {
	name := strings.TrimPrefix(snakeCase(shortName(toolName)), "fitness_")
	return strings.ToLower(strings.ReplaceAll(name, "_", " "))
}

// ToolDetail is a short hint of what the call was about (a query, deck, file...).
func ToolDetail(input map[string]any) string {
	if note, ok := input["note"].(map[string]any); ok {
		if deck, ok := note["deckName"].(string); ok && deck != "" {
			return text.Truncate(deck, 60)
		}
	}
	if notes, ok := input["notes"].([]any); ok && len(notes) > 0 {
		return fmt.Sprintf("%d notes", len(notes))
	}
	for _, key := range []string{"query", "deck", "deckName", "file_path", "absolute_path", "path", "pattern", "command", "name", "date"} {
		value, ok := input[key]
		if !ok || value == nil || value == "" {
			continue
		}
		detail := fmt.Sprint(value)
		if key == "file_path" || key == "absolute_path" || key == "path" {
			detail = filepath.Base(detail)
		}
		return text.Truncate(detail, 60)
	}
	return ""
}

// mfpReadTools are MyFitnessPal tools that only read; any other fitness_* tool changed the diary.
var mfpReadTools = []string{"get_", "search", "list_", "draft_", "bulk_export"}

// ChangesDiary reports whether the call changed the MyFitnessPal diary, goals or weight.
func ChangesDiary(toolName string) bool {
	name := shortName(toolName)
	if !strings.HasPrefix(name, "fitness_") {
		return false
	}
	return !hasAnyPrefix(strings.TrimPrefix(name, "fitness_"), mfpReadTools)
}

var ankiReadTools = []string{"list", "find", "notes_info", "deck_stats", "get", "are", "model",
	"collection_stats", "review_stats", "present", "gui", "sync"}

var builtinReadTools = map[string]bool{"read": true, "glob": true, "grep": true, "read_file": true,
	"read_many_files": true, "search_file_content": true, "list_directory": true, "run": true}

// ChangesAnki reports whether the call changed the Anki collection (so the Anki tab reloads).
func ChangesAnki(toolName string) bool {
	if strings.Contains(toolName, "__") && !strings.HasPrefix(toolName, "mcp__anki__") {
		return false
	}
	name := strings.ToLower(snakeCase(shortName(toolName)))
	return !strings.HasPrefix(name, "fitness_") && !hasAnyPrefix(name, ankiReadTools) && !builtinReadTools[name]
}

// FriendlyError points at signing in when the CLI isn't logged in.
func FriendlyError(p Provider, message string) string {
	lower := strings.ToLower(message)
	for _, hint := range []string{"401", "unauthorized", "api key", "not logged in", "login", "auth", "credential"} {
		if strings.Contains(lower, hint) {
			return p.Label() + " isn't signed in. " + p.SignInHint()
		}
	}
	return text.Truncate(message, 300)
}

// shortName drops an MCP prefix: mcp__anki__addNote → addNote.
func shortName(toolName string) string {
	if i := strings.LastIndex(toolName, "__"); i >= 0 {
		return toolName[i+2:]
	}
	return toolName
}

// snakeCase puts an underscore where a lower-case letter meets an upper-case one: addNote → add_Note.
func snakeCase(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			out.WriteByte('_')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

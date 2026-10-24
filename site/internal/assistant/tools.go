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
	for _, key := range []string{"query", "deck", "deckName", "file_path", "absolute_path", "path", "pattern", "command", "name", "date", "search", "description"} {
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

// ChangesDiary reports whether a tool call ("myfitnesspal__fitness_log_food") changed the
// MyFitnessPal diary, goals or weight, so the Today tab syncs.
func ChangesDiary(toolName string) bool {
	name := toolName
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if !strings.HasPrefix(name, "fitness_") {
		return false
	}
	for _, prefix := range mfpReadTools {
		if strings.HasPrefix(strings.TrimPrefix(name, "fitness_"), prefix) {
			return false
		}
	}
	return true
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

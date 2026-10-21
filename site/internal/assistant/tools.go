package assistant

import "strings"

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

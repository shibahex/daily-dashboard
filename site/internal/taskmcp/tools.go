// Package taskmcp lets the chat's AI create and edit the user's Taskwarrior tasks.
//
// The AI's tools run in a sandbox that can't see the user's tasks, `task` or its data. So this
// MCP server is the dashboard itself, started inside the sandbox as `dashboard task-mcp`
// (RunStdio): it only passes each tool call through a unix socket to the Gateway the dashboard
// serves outside the sandbox, which does the work with the user's own tasks.Service (the same
// code the Tasks tab uses, with the same checks on every value). The socket belongs to one
// user's run, so an agent can only ever reach its own user's tasks, and it can only ask for the
// tools below: never for an arbitrary `task` command, an import, or `undo`.
package taskmcp

import (
	"encoding/json"
)

// Command is the argument that makes the dashboard program run as this MCP server.
const Command = "task-mcp"

// SocketEnv names the environment variable that holds the gateway's socket inside the sandbox.
const SocketEnv = "DASHBOARD_TASKS_SOCKET"

// Tool is one tool the MCP server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Result is a tool call's answer.
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError,omitempty"`
}

const uuidProperty = `{"type":"string","description":"The task's uuid, as tasks_list shows it."}`

// schema makes an object schema from its properties (JSON) and the names that are required.
func schema(properties string, required ...string) json.RawMessage {
	req := "[]"
	if len(required) > 0 {
		quoted, _ := json.Marshal(required)
		req = string(quoted)
	}
	return json.RawMessage(`{"type":"object","properties":` + properties + `,"required":` + req + `,"additionalProperties":false}`)
}

const (
	dateHelp = "A date or time Taskwarrior understands: 2026-10-24, 2026-10-24T09:30, tomorrow, friday, eow (end of week), eom, 3d. " +
		"Local time. An empty string clears it."
	tagsHelp = "Tags without the + sign (letters, digits and _ only)."
)

// Tools are the tools, in the order they're offered.
var Tools = []Tool{
	{
		Name: "tasks_list",
		Description: "List the user's Taskwarrior tasks, soonest due first. By default only pending tasks " +
			"(not waiting, completed, deleted or repeating schedules). Repeating tasks show up as separate " +
			"copies with a parent. Dates are local time, like 2026-10-24T09:30. Look tasks up here before " +
			"changing them: never guess a uuid.",
		InputSchema: schema(`{
"status":{"type":"string","enum":["pending","waiting","completed","deleted","all"],"description":"Which tasks. Default pending."},
"project":{"type":"string","description":"Only tasks in this project (or one of its sub-projects, like home.garden for home)."},
"tag":{"type":"string","description":"Only tasks with this tag, without the + sign."},
"search":{"type":"string","description":"Only tasks whose description or notes contain this text (not case sensitive)."},
"due_by":{"type":"string","description":"Only tasks due by then, overdue ones included: today, tomorrow, a date like 2026-10-25 (that whole day counts) or a time like 2026-10-25T09:30."},
"uuid":` + uuidProperty + `,
"limit":{"type":"integer","minimum":1,"maximum":200,"description":"At most this many tasks. Default 50."}
}`),
	},
	{
		Name: "tasks_add",
		Description: "Add a task. Check tasks_list first for a task that already does the job. " +
			"A repeating task (recur) needs a due date for its first copy.",
		InputSchema: schema(`{
"description":{"type":"string","description":"What to do, one line (at most 300 characters)."},
"project":{"type":"string","description":"Project name: letters, digits, _ . and - only. Use the user's existing projects (see tasks_list)."},
"priority":{"type":"string","enum":["H","M","L"]},
"due":{"type":"string","description":"`+dateHelp+`"},
"scheduled":{"type":"string","description":"When to start working on it. `+dateHelp+`"},
"wait":{"type":"string","description":"Hide the task until this date. `+dateHelp+`"},
"tags":{"type":"array","items":{"type":"string"},"description":"`+tagsHelp+`"},
"recur":{"type":"string","description":"Repeat: daily, weekly, monthly, 2w, 3d... Needs due."}
}`, "description"),
	},
	{
		Name: "tasks_modify",
		Description: "Change a task: only the fields you pass change. An empty string clears project, priority, " +
			"due, scheduled or wait. On a copy of a repeating task this changes that copy only. " +
			"Repeating schedules themselves can only be changed on the Tasks tab.",
		InputSchema: schema(`{
"uuid":`+uuidProperty+`,
"description":{"type":"string","description":"The new description."},
"project":{"type":"string","description":"The new project, or an empty string for none."},
"priority":{"type":"string","enum":["H","M","L",""],"description":"The new priority, or an empty string for none."},
"due":{"type":"string","description":"`+dateHelp+`"},
"scheduled":{"type":"string","description":"`+dateHelp+`"},
"wait":{"type":"string","description":"`+dateHelp+`"},
"add_tags":{"type":"array","items":{"type":"string"},"description":"`+tagsHelp+`"},
"remove_tags":{"type":"array","items":{"type":"string"},"description":"`+tagsHelp+`"}
}`, "uuid"),
	},
	{
		Name:        "tasks_done",
		Description: "Mark a pending task done (now).",
		InputSchema: schema(`{"uuid":`+uuidProperty+`}`, "uuid"),
	},
	{
		Name:        "tasks_reopen",
		Description: "Make a completed or deleted task pending again.",
		InputSchema: schema(`{"uuid":`+uuidProperty+`}`, "uuid"),
	},
	{
		Name: "tasks_start",
		Description: "Start working on a task: it becomes active, and the dashboard's time tracking " +
			"starts counting.",
		InputSchema: schema(`{"uuid":`+uuidProperty+`}`, "uuid"),
	},
	{
		Name:        "tasks_stop",
		Description: "Stop working on an active task (time tracking stops; the task stays pending).",
		InputSchema: schema(`{"uuid":`+uuidProperty+`}`, "uuid"),
	},
	{
		Name:        "tasks_annotate",
		Description: "Add a note to a task (shown with it; at most 500 characters).",
		InputSchema: schema(`{"uuid":`+uuidProperty+`,"note":{"type":"string"}}`, "uuid", "note"),
	},
	{
		Name: "tasks_delete",
		Description: "Delete a pending task, keeping why as a note (History shows it as skipped or dismissed). " +
			"Only when the user asks to delete, skip or drop a task. Not for finishing one: use tasks_done.",
		InputSchema: schema(`{"uuid":`+uuidProperty+`,"reason":{"type":"string","description":"Why, in a few words (at most 300 characters)."}}`, "uuid"),
	},
}

// toolNamed finds a tool by name.
func toolNamed(name string) (Tool, bool) {
	for _, t := range Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

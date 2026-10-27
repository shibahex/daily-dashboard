// Package dashmcp lets the chat's AI read and change the user's dashboard: the Today tab (its
// checklist and skip reasons, weigh-ins and weight plan, this week's training, reminders and
// goals), the Lifting tab (routines, logging sets, starting a workout) and Timewarrior.
//
// It works like the Taskwarrior tools (taskmcp): the browser's AI calls the Gateway in the
// dashboard directly, and an AI on the server runs `dashboard dashboard-mcp` in its sandbox, which
// passes each call through a unix socket to the Gateway of the user the run is for. Every change
// goes through the same store code and checks the pages use.
package dashmcp

import (
	"fmt"
	"os"

	"dashboard/internal/taskmcp"
)

// Command is the argument that makes the dashboard program run as this MCP server.
const Command = "dashboard-mcp"

// SocketEnv names the environment variable that holds the gateway's socket inside the sandbox.
const SocketEnv = "DASHBOARD_DATA_SOCKET"

var schema = taskmcp.Schema

const (
	dayHelp  = `A day: today, yesterday or YYYY-MM-DD (local). Default today.`
	timeHelp = `Local time to the minute: 2026-10-27T18:30.`
)

// Tools are the tools, in the order they're offered. The ones marked ReadOnly only read; the chat
// asks the user before running any other.
var Tools = []taskmcp.Tool{
	{
		Name: "dashboard_overview", Annotations: taskmcp.ReadOnly,
		Description: "Read the Today tab for a day: the checklist with each item's state (done, skipped with its " +
			"reason, or not done), weigh-ins and the weight plan (target, whether it's reached), this week's " +
			"training schedule, the reminders, the goals, and the workout in progress if any. Read this before changing them.",
		InputSchema: schema(`{"day":{"type":"string","description":"` + dayHelp + `"}}`),
	},
	{
		Name:        "checklist_set",
		Description: "Tick, skip or clear one of the checklist's items for a day, or change why it was skipped. Use the item's name as dashboard_overview lists it.",
		InputSchema: schema(`{
"item":{"type":"string"},
"state":{"type":"string","enum":["done","skip","clear"],"description":"done ticks it, skip skips it (it still counts toward the day), clear un-ticks or un-skips it."},
"reason":{"type":"string","description":"Why it's skipped (with skip). Skipping again with a new reason changes the reason."},
"day":{"type":"string","description":"`+dayHelp+`"}
}`, "item", "state"),
	},
	{
		Name:        "checklist_items",
		Description: "Replace the list of daily checklist items (to add, remove, rename or reorder them). Send the whole new list; past days keep their ticks.",
		InputSchema: schema(`{"items":{"type":"array","items":{"type":"string"},"maxItems":40}}`, "items"),
	},
	{
		Name:        "weight_log",
		Description: "Log the user's weigh-in for a day, or remove one. When MyFitnessPal is connected it's sent there too unless the user turned that off on the Weight card; set myfitnesspal to decide for this one.",
		InputSchema: schema(`{
"weight":{"type":"number","description":"The weight, in unit."},
"unit":{"type":"string","enum":["lb","kg"],"description":"Default lb."},
"day":{"type":"string","description":"` + dayHelp + `"},
"remove":{"type":"boolean","description":"Remove that day's weigh-in instead."},
"myfitnesspal":{"type":"boolean","description":"Also log it in MyFitnessPal. Leave out to use the user's choice on the Weight card."}
}`),
	},
	{
		Name:        "weight_plan",
		Description: "Change the weight plan on the Weight card. Fields left out stay as they are. Weights in lb.",
		InputSchema: schema(`{
"start":{"type":"number","description":"Starting weight, lb."},
"start_date":{"type":"string","description":"YYYY-MM-DD."},
"target":{"type":"string","description":"The goal: one weight (\"170\") or a range (\"165-175\"), lb."},
"lowest":{"type":"number","description":"Lowest weight ever, lb."}
}`),
	},
	{
		Name: "week_set",
		Description: "Change which training session is planned on which weekday (the This week card). A session name that " +
			"doesn't exist yet is added. Days left out stay as they are.",
		InputSchema: schema(`{"schedule":{"type":"object","description":"Weekday (Monday … Sunday) to session name, like {\"Monday\":\"Push\",\"Tuesday\":\"Rest\"}.","additionalProperties":{"type":"string"}}}`, "schedule"),
	},
	{
		Name:        "reminders_set",
		Description: "Replace the Reminders card's list (to add, remove or change reminders). Send the whole new list.",
		InputSchema: schema(`{"reminders":{"type":"array","items":{"type":"string"},"maxItems":50}}`, "reminders"),
	},
	{
		Name:        "goals_set",
		Description: "Replace the plan's goals (label, value and unit each, like Protein 180 g). Send the whole new list.",
		InputSchema: schema(`{"goals":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"label":{"type":"string"},"value":{"type":"string"},"unit":{"type":"string"}},"required":["label","value"],"additionalProperties":false}}}`, "goals"),
	},
	{
		Name: "lifting_overview", Annotations: taskmcp.ReadOnly,
		Description: "Read the Lifting tab: the routines (with their ids, days and each exercise's sets, reps or hold seconds, " +
			"rest, alternatives, optional and superset flags), the workout in progress, the latest finished workouts, and the " +
			"one-rep max goals with each lift's actual one-rep max (its heaviest single; estimates never count).",
		InputSchema: schema(`{"workouts":{"type":"integer","minimum":0,"maximum":50,"description":"How many recent workouts to include. Default 5."}}`),
	},
	{
		Name: "lifting_history", Annotations: taskmcp.ReadOnly,
		Description: "One exercise's sessions, newest first: each day's sets, its best estimated one-rep max, and its heaviest single (an actual one-rep max) when there was one.",
		InputSchema: schema(`{"exercise":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100,"description":"Default 10."}}`, "exercise"),
	},
	{
		Name:        "lifting_goal",
		Description: "Set a lift's one-rep max goal (\"squat 1RM goal 225\"), or clear it with 0. It's reached only by an actual single (one rep) at that weight, never by an estimate.",
		InputSchema: schema(`{
"exercise":{"type":"string","description":"The lift, named as the user already names it (see lifting_overview)."},
"one_rep_max":{"type":"number","minimum":0,"description":"The goal, in unit; 0 clears it."},
"unit":{"type":"string","enum":["lb","kg"],"description":"Default: the unit the Lifting tab shows."}
}`, "exercise", "one_rep_max"),
	},
	{
		Name: "lifting_log",
		Description: "Log sets the user did, as done: \"just did 225 bench for 5\". They go into the workout in progress, " +
			"else into a workout that finished in the last two hours, else into a new workout called Quick log. " +
			"Use the exercise name the user already uses (see lifting_overview) when it's the same exercise.",
		InputSchema: schema(`{
"exercise":{"type":"string"},
"sets":{"type":"array","minItems":1,"maxItems":30,"items":{"type":"object","properties":{
  "weight":{"type":"number","description":"In unit; 0 for bodyweight."},
  "reps":{"type":"integer"},
  "seconds":{"type":"integer","description":"For a hold (plank): seconds held, instead of reps."},
  "note":{"type":"string"}},"additionalProperties":false}},
"unit":{"type":"string","enum":["lb","kg"],"description":"Default: the unit the Lifting tab shows."}
}`, "exercise", "sets"),
	},
	{
		Name: "lifting_routine_save",
		Description: "Create a routine, or replace one (with its id from lifting_overview: send all of it, days and " +
			"exercises you don't change included, with their ids). Exercises: name, sets, reps (\"10\", \"8-12\", \"max\"; " +
			"for a hold, its seconds and hold true), rest in seconds, and optionally options (exercises that can take its " +
			"place), optional and superset (done back to back with the next exercise).",
		InputSchema: schema(`{"routine":{"type":"object","properties":{
"id":{"type":"string","description":"Leave out to create a new routine."},
"name":{"type":"string"},
"days":{"type":"array","maxItems":40,"items":{"type":"object","properties":{
  "id":{"type":"string"},"name":{"type":"string"},
  "task":{"type":"string","description":"A task's description: finishing this day's workout ticks it."},
  "exercises":{"type":"array","maxItems":40,"items":{"type":"object","properties":{
    "name":{"type":"string"},"sets":{"type":"integer"},"reps":{"type":"string"},"rest":{"type":"integer"},
    "hold":{"type":"boolean"},"optional":{"type":"boolean"},"superset":{"type":"boolean"},
    "options":{"type":"array","items":{"type":"string"}}},"required":["name"],"additionalProperties":false}}},
  "required":["name"],"additionalProperties":false}}},
"required":["name","days"],"additionalProperties":false}}`, "routine"),
	},
	{
		Name:        "lifting_routine_delete",
		Description: "Delete a routine by its id. Past workouts stay in the history.",
		InputSchema: schema(`{"id":{"type":"string"}}`, "id"),
	},
	{
		Name:        "lifting_start",
		Description: "Start a workout: a routine's day (ids from lifting_overview), or an empty one without them.",
		InputSchema: schema(`{"routine":{"type":"string"},"day":{"type":"string"}}`),
	},
	{
		Name: "time_list", Annotations: taskmcp.ReadOnly,
		Description: "List the time tracked in Timewarrior between two days (default today), with each entry's id, start, end, tags and minutes, and what's being tracked now.",
		InputSchema: schema(`{"from":{"type":"string","description":"` + dayHelp + `"},"to":{"type":"string","description":"The last day included. Default: from."}}`),
	},
	{
		Name:        "time_track",
		Description: "Log a stretch of time that already happened, with tags (like Reading, Lifting).",
		InputSchema: schema(`{"start":{"type":"string","description":"`+timeHelp+`"},"end":{"type":"string","description":"`+timeHelp+`"},"tags":{"type":"array","items":{"type":"string"}}}`, "start", "end", "tags"),
	},
	{
		Name:        "time_start",
		Description: "Start tracking time now with tags (stopping whatever was being tracked).",
		InputSchema: schema(`{"tags":{"type":"array","items":{"type":"string"},"minItems":1}}`, "tags"),
	},
	{
		Name:        "time_stop",
		Description: "Stop tracking time.",
		InputSchema: schema(`{}`),
	},
	{
		Name:        "time_edit",
		Description: "Change a tracked entry's tags, start or end. id and start_was come from time_list (they make sure it's the same entry).",
		InputSchema: schema(`{"id":{"type":"integer"},"start_was":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"start":{"type":"string","description":"`+timeHelp+`"},"end":{"type":"string","description":"`+timeHelp+`"}}`, "id", "start_was"),
	},
	{
		Name:        "time_delete",
		Description: "Delete a tracked entry. id and start_was come from time_list.",
		InputSchema: schema(`{"id":{"type":"integer"},"start_was":{"type":"string"}}`, "id", "start_was"),
	},
}

// Server is the MCP server an AI on the server runs in its sandbox.
var Server = taskmcp.Server{Name: "dashboard", Tools: Tools}

// RunStdio is `dashboard dashboard-mcp`: the MCP server inside the sandbox. It passes every tool
// call through the gateway's socket (SocketEnv) and returns when the AI closes its input.
func RunStdio() error {
	socket := os.Getenv(SocketEnv)
	if socket == "" {
		return fmt.Errorf("%s isn't set: this program is started by the dashboard's AI, not by hand", SocketEnv)
	}
	return Server.Forward(socket, os.Stdin, os.Stdout)
}

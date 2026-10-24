package taskmcp

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/tasks"
	"dashboard/internal/taskwarrior"
	"dashboard/internal/timewarrior"
)

const (
	taskUUID     = "3f2b0c1e-6a1d-4c55-9a52-0d1c1a1b2c3d"
	templateUUID = "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a"
)

// A gateway on a stand-in for `task`: a script that writes down every call, answers `export` with
// the given JSON, and answers `add` with a new task's uuid. It returns what was called.
func gatewayWithFakeTask(t *testing.T, exported string) (*Gateway, func() string) {
	t.Helper()
	dir := t.TempDir()
	calls, export := filepath.Join(dir, "calls.log"), filepath.Join(dir, "export.json")
	if err := os.WriteFile(export, []byte(exported), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> '" + calls + "'\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    export) cat '" + export + "'; exit 0;;\n" +
		"    add) echo 'Created task " + taskUUID + ".'; exit 0;;\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(filepath.Join(dir, "task"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	g := NewGateway(tasks.NewService(taskwarrior.NewClient(), timewarrior.NewClient()))
	return g, func() string {
		data, _ := os.ReadFile(calls)
		return string(data)
	}
}

var oneTask = `[{"uuid":"` + taskUUID + `","description":"Buy milk","status":"pending","project":"home","tags":["errand"],` +
	`"entry":"20261001T120000Z","due":"20261024T090000Z"}]`

var someTasks = `[` +
	`{"uuid":"` + taskUUID + `","description":"Buy milk","status":"pending","project":"home.shop","entry":"20261001T120000Z","due":"20261024T090000Z"},` +
	`{"uuid":"11111111-2222-4333-8444-555555555555","description":"Write report","status":"pending","project":"work","tags":["deep"],"entry":"20261001T120000Z",` +
	`"annotations":[{"entry":"20261002T120000Z","description":"for Monday"}]},` +
	`{"uuid":"66666666-7777-4888-9999-000000000000","description":"Old chore","status":"completed","entry":"20260901T120000Z","end":"20260902T120000Z"}]`

func TestToolsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range Tools {
		if seen[tool.Name] || tool.Description == "" {
			t.Errorf("tool %q is repeated or has no description", tool.Name)
		}
		seen[tool.Name] = true
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema.Type != "object" {
			t.Fatalf("%s: bad input schema (%v): %s", tool.Name, err, tool.InputSchema)
		}
		for _, name := range schema.Required {
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s requires %q, which isn't one of its properties", tool.Name, name)
			}
		}
	}
	if _, err := json.Marshal(Tools); err != nil {
		t.Errorf("the tool list can't be written: %v", err)
	}
}

func TestEveryToolIsHandled(t *testing.T) {
	g, _ := gatewayWithFakeTask(t, oneTask)
	for _, tool := range Tools {
		// With no arguments each tool either works or says what's missing; none is unknown.
		if result := g.Call(tool.Name, json.RawMessage(`{}`)); strings.Contains(result.Text, "unknown tool") {
			t.Errorf("%s isn't handled: %s", tool.Name, result.Text)
		}
	}
}

func TestAddAndModifyRunTaskwarrior(t *testing.T) {
	g, calls := gatewayWithFakeTask(t, oneTask)
	added := g.Call("tasks_add", json.RawMessage(`{"description":"Buy milk","project":"home","priority":"h","due":"tomorrow","tags":["errand"]}`))
	if added.IsError || !strings.Contains(added.Text, "Added the task") || !strings.Contains(added.Text, taskUUID) {
		t.Fatalf("tasks_add = %+v", added)
	}
	for _, want := range []string{" add ", "project:home", "priority:H", "due:tomorrow", "+errand", "-- Buy milk"} {
		if !strings.Contains(calls(), want) {
			t.Errorf("task wasn't called with %q: %s", want, calls())
		}
	}

	changed := g.Call("tasks_modify", json.RawMessage(`{"uuid":"`+taskUUID+`","due":"friday","add_tags":["urgent"],"priority":""}`))
	if changed.IsError || !strings.Contains(changed.Text, "Changed the task") {
		t.Fatalf("tasks_modify = %+v", changed)
	}
	var modify string
	for _, line := range strings.Split(calls(), "\n") {
		if strings.Contains(line, " modify ") {
			modify = line
		}
	}
	for _, want := range []string{taskUUID + " modify", "priority:", "due:friday", "+urgent"} {
		if !strings.Contains(modify, want) {
			t.Errorf("modify wasn't called with %q: %q", want, modify)
		}
	}

	if done := g.Call("tasks_done", json.RawMessage(`{"uuid":"`+taskUUID+`"}`)); done.IsError || !strings.Contains(calls(), taskUUID+" done") {
		t.Errorf("tasks_done = %+v, calls: %s", done, calls())
	}
	deleted := g.Call("tasks_delete", json.RawMessage(`{"uuid":"`+taskUUID+`","reason":"not needed"}`))
	if deleted.IsError || !strings.Contains(calls(), "annotate") || !strings.Contains(calls(), "delete") {
		t.Errorf("tasks_delete = %+v, calls: %s", deleted, calls())
	}
}

func TestMistakesNeverReachTaskwarrior(t *testing.T) {
	g, calls := gatewayWithFakeTask(t, oneTask)
	for name, call := range map[string][2]string{
		"unknown tool":      {"tasks_nope", `{}`},
		"no description":    {"tasks_add", `{"description":""}`},
		"long description":  {"tasks_add", `{"description":"` + strings.Repeat("x", 301) + `"}`},
		"bad priority":      {"tasks_add", `{"description":"x","priority":"urgent"}`},
		"bad date":          {"tasks_add", `{"description":"x","due":"tomorrow; rm -rf"}`},
		"bad tag":           {"tasks_add", `{"description":"x","tags":["a b"]}`},
		"bad project":       {"tasks_add", `{"description":"x","project":"a b"}`},
		"repeat needs due":  {"tasks_add", `{"description":"x","recur":"daily"}`},
		"unknown field":     {"tasks_add", `{"description":"x","dueDate":"tomorrow"}`},
		"bad uuid":          {"tasks_modify", `{"uuid":"not-a-uuid","due":"friday"}`},
		"nothing to change": {"tasks_modify", `{"uuid":"` + taskUUID + `"}`},
		"uuid with a flag":  {"tasks_done", `{"uuid":"` + taskUUID + ` rc.hooks=0"}`},
		"empty note":        {"tasks_annotate", `{"uuid":"` + taskUUID + `","note":" "}`},
		"bad status":        {"tasks_list", `{"status":"everything"}`},
		"bad due_by":        {"tasks_list", `{"due_by":"someday"}`},
	} {
		if result := g.Call(call[0], json.RawMessage(call[1])); !result.IsError || result.Text == "" {
			t.Errorf("%s: %s %s answered %+v", name, call[0], call[1], result)
		}
	}
	for _, change := range []string{" add ", " modify ", " done", " annotate", " delete", " start", " stop"} {
		if strings.Contains(calls(), change) {
			t.Errorf("a mistake got through to Taskwarrior (%q): %s", change, calls())
		}
	}
}

func TestRepeatingSchedulesAreOffLimits(t *testing.T) {
	template := `[{"uuid":"` + templateUUID + `","description":"Stretch","status":"recurring","recur":"daily","entry":"20261001T120000Z","due":"20261024T090000Z"}]`
	g, calls := gatewayWithFakeTask(t, template)
	for _, call := range [][2]string{
		{"tasks_modify", `{"uuid":"` + templateUUID + `","description":"Stretch more"}`},
		{"tasks_done", `{"uuid":"` + templateUUID + `"}`},
		{"tasks_start", `{"uuid":"` + templateUUID + `"}`},
		{"tasks_delete", `{"uuid":"` + templateUUID + `"}`},
	} {
		if result := g.Call(call[0], json.RawMessage(call[1])); !result.IsError || !strings.Contains(result.Text, "Tasks tab") {
			t.Errorf("%s on a repeating schedule answered %+v", call[0], result)
		}
	}
	for _, change := range []string{" modify ", " done", " delete", " start"} {
		if strings.Contains(calls(), change) {
			t.Errorf("a repeating schedule was changed (%q): %s", change, calls())
		}
	}
	listed := g.Call("tasks_list", json.RawMessage(`{"status":"all"}`))
	if listed.IsError || strings.Contains(listed.Text, "Stretch") {
		t.Errorf("schedules shouldn't be listed as tasks: %+v", listed)
	}
}

type listing struct {
	Total int              `json:"total"`
	Shown int              `json:"shown"`
	Tasks []map[string]any `json:"tasks"`
}

func list(t *testing.T, g *Gateway, arguments string) listing {
	t.Helper()
	result := g.Call("tasks_list", json.RawMessage(arguments))
	var out listing
	if result.IsError || json.Unmarshal([]byte(result.Text), &out) != nil {
		t.Fatalf("tasks_list %s = %+v", arguments, result)
	}
	return out
}

func TestListFilters(t *testing.T) {
	g, _ := gatewayWithFakeTask(t, someTasks)

	pending := list(t, g, `{}`)
	if pending.Total != 2 || len(pending.Tasks) != 2 || pending.Tasks[0]["description"] != "Buy milk" {
		t.Errorf("pending = %+v: want the two pending tasks, the one with a due date first", pending)
	}
	if all := list(t, g, `{"status":"all"}`); all.Total != 3 {
		t.Errorf("all = %+v", all)
	}
	if done := list(t, g, `{"status":"completed"}`); done.Total != 1 || done.Tasks[0]["description"] != "Old chore" {
		t.Errorf("completed = %+v", done)
	}
	if home := list(t, g, `{"project":"home"}`); home.Total != 1 || home.Tasks[0]["description"] != "Buy milk" {
		t.Errorf("project home should include home.shop: %+v", home)
	}
	if tagged := list(t, g, `{"tag":"deep"}`); tagged.Total != 1 || tagged.Tasks[0]["description"] != "Write report" {
		t.Errorf("tag = %+v", tagged)
	}
	if found := list(t, g, `{"search":"MONDAY"}`); found.Total != 1 || found.Tasks[0]["description"] != "Write report" {
		t.Errorf("a search should look in the notes, not case sensitive: %+v", found)
	}
	if soon := list(t, g, `{"due_by":"2099-01-01"}`); soon.Total != 1 {
		t.Errorf("due_by far ahead should give the one task with a due date: %+v", soon)
	}
	if overdue := list(t, g, `{"due_by":"2000-01-01"}`); overdue.Total != 0 {
		t.Errorf("due_by in the past: %+v", overdue)
	}
	if one := list(t, g, `{"limit":1}`); one.Total != 2 || one.Shown != 1 {
		t.Errorf("limit = %+v", one)
	}
}

func TestServeSpeaksMCP(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"tasks_list","arguments":{"limit":1}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"rm_rf"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
	}, "\n") // no newline at the end: the last message still counts
	var out bytes.Buffer
	var called []string
	err := Serve(strings.NewReader(input), &out, func(tool string, arguments json.RawMessage) (Result, error) {
		called = append(called, tool+" "+string(arguments))
		return Result{Text: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	answers := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(answers) != 5 {
		t.Fatalf("got %d answers (a notification gets none): %s", len(answers), out.String())
	}
	for i, want := range []string{`"protocolVersion":"2025-03-26"`, `"name":"tasks_add"`, `"id":"x"`, `"error"`, `-32601`} {
		if !strings.Contains(answers[i], want) {
			t.Errorf("answer %d lacks %s: %s", i+1, want, answers[i])
		}
	}
	if !strings.Contains(answers[2], `"text":"ok"`) || !strings.Contains(answers[2], `"isError":false`) {
		t.Errorf("tool answer = %s", answers[2])
	}
	if len(called) != 1 || called[0] != `tasks_list {"limit":1}` {
		t.Errorf("tools called: %q (only the known tool should be)", called)
	}
}

func TestForwardReachesTheGateway(t *testing.T) {
	g, _ := gatewayWithFakeTask(t, oneTask)
	socket := filepath.Join(t.TempDir(), "tasks.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	defer listener.Close()
	go g.Serve(listener)

	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tasks_list","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if err := Forward(socket, strings.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Buy milk") || strings.Contains(out.String(), `"isError":true`) {
		t.Errorf("answer through the socket: %s", out.String())
	}

	// With nothing listening the AI is told so, and the program keeps running.
	listener.Close()
	out.Reset()
	if err := Forward(socket, strings.NewReader(request), &out); err != nil || !strings.Contains(out.String(), `"isError":true`) {
		t.Errorf("without a gateway: %v, %s", err, out.String())
	}
}

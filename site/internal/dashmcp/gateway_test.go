package dashmcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dashboard/internal/config"
	"dashboard/internal/lifting"
	"dashboard/internal/store"
	"dashboard/internal/workspace"
)

// testGateway is a gateway on a new user's folder, starting from the example plan, at a fixed time.
func testGateway(t *testing.T) *Gateway {
	site, _ := filepath.Abs("../..")
	paths := config.NewPaths(site).ForUser(t.TempDir())
	ws := &workspace.Workspace{Paths: paths, Store: store.New(paths), Lifting: lifting.New(paths.DataFile("lifting.json"))}
	ws.Store.LoadPlan() // makes plan.json from the example
	now := time.Date(2026, 10, 27, 18, 30, 0, 0, time.Local)
	return &Gateway{ws: ws, now: func() time.Time { return now }}
}

func call(t *testing.T, g *Gateway, tool, arguments string) string {
	t.Helper()
	result := g.Call(tool, json.RawMessage(arguments))
	if result.IsError {
		t.Fatalf("%s %s: %s", tool, arguments, result.Text)
	}
	return result.Text
}

func TestChecklist(t *testing.T) {
	g := testGateway(t)
	item := g.ws.Store.PlanChecklist()[0]
	call(t, g, "checklist_set", `{"item":"`+strings.ToUpper(item)+`","state":"skip","reason":"sick"}`)
	if got := g.ws.Store.Checklist()["2026-10-27"][item]; got.(map[string]any)["note"] != "sick" {
		t.Errorf("skip with reason: %v", got)
	}
	call(t, g, "checklist_set", `{"item":"`+item+`","state":"done","day":"yesterday"}`)
	if g.ws.Store.Checklist()["2026-10-26"][item] != "done" {
		t.Error("yesterday's tick")
	}
	if overview := call(t, g, "dashboard_overview", `{}`); !strings.Contains(overview, `"reason": "sick"`) {
		t.Errorf("the overview should show the reason:\n%s", overview)
	}
	for _, bad := range []string{`{"item":"Nope","state":"done"}`, `{"item":"` + item + `","state":"done","day":"2027-01-01"}`, `{"item":"x","state":"done","typo":1}`} {
		if !g.Call("checklist_set", json.RawMessage(bad)).IsError {
			t.Errorf("%s should fail", bad)
		}
	}
}

func TestPlanTools(t *testing.T) {
	g := testGateway(t)
	call(t, g, "week_set", `{"schedule":{"monday":"Mountain bike","Sunday":"rest day"}}`)
	plan, _ := g.ws.Store.LoadPlan()
	if plan.Sessions[plan.Schedule["Monday"]].Title != "Mountain bike" || plan.Schedule["Sunday"] != "rest" {
		t.Errorf("week: %v", plan.Schedule)
	}
	call(t, g, "reminders_set", `{"reminders":["Stretch"," ",""]}`)
	call(t, g, "weight_plan", `{"target":"165-170"}`)
	call(t, g, "weight_log", `{"weight":75,"unit":"kg"}`)
	plan, _ = g.ws.Store.LoadPlan()
	weights := g.ws.Store.Weights()
	if len(plan.Notes) != 1 || plan.Weight.Target != "165-170" || weights[len(weights)-1].LB != 165.3 {
		t.Errorf("notes %q, target %q, weights %v", plan.Notes, plan.Weight.Target, weights)
	}
	if !store.TargetReached(165.3, plan.Weight) || store.ToGo(180, plan.Weight) != 10 {
		t.Error("the target range")
	}
}

func TestLiftingTools(t *testing.T) {
	g := testGateway(t)
	saved := call(t, g, "lifting_routine_save", `{"routine":{"name":"PPL","days":[{"name":"Push","exercises":[
		{"name":"Bench press","sets":3,"reps":"5","rest":180,"options":["DB press"]},{"name":"Plank","sets":2,"reps":"45","hold":true}]}]}}`)
	if !strings.Contains(saved, `"hold": true`) || !strings.Contains(saved, `"DB press"`) {
		t.Errorf("saved: %s", saved)
	}
	// "Just did 225 bench for 5": with nothing going, a Quick log.
	out := call(t, g, "lifting_log", `{"exercise":"bench press","sets":[{"weight":225,"reps":5}]}`)
	data := g.ws.Lifting.Load()
	if !strings.Contains(out, "Quick log") || len(data.Workouts) != 1 || data.Workouts[0].Exercises[0].Name != "Bench press" ||
		data.Workouts[0].Exercises[0].Sets[0].LB != 225 {
		t.Fatalf("%s\n%+v", out, data.Workouts)
	}
	// Another set soon after joins it.
	call(t, g, "lifting_log", `{"exercise":"Plank","sets":[{"weight":0,"seconds":60}]}`)
	if data = g.ws.Lifting.Load(); len(data.Workouts) != 1 || data.Workouts[0].Exercises[1].Sets[0].Secs != 60 {
		t.Errorf("joined: %+v", data.Workouts)
	}
	// In a workout that's going, it fills the sets still to do.
	routine := data.Routines[0]
	call(t, g, "lifting_start", `{"routine":"`+routine.ID+`","day":"`+routine.Days[0].ID+`"}`)
	call(t, g, "lifting_log", `{"exercise":"Bench press","sets":[{"weight":230,"reps":5}]}`)
	if sets := g.ws.Lifting.Load().Active.Exercises[0].Sets; len(sets) != 3 || !sets[0].Done || sets[0].LB != 230 || sets[1].Done {
		t.Errorf("in progress: %+v", sets)
	}
	if !g.Call("lifting_log", json.RawMessage(`{"exercise":"Bench press","sets":[{"weight":100}]}`)).IsError {
		t.Error("a set without reps should fail")
	}
	if history := call(t, g, "lifting_history", `{"exercise":"Bench press"}`); !strings.Contains(history, `"lb": 225`) {
		t.Errorf("history: %s", history)
	}
}

func TestToolsAreValid(t *testing.T) {
	for _, tool := range Tools {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("%s's input schema isn't JSON: %v", tool.Name, err)
			continue
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

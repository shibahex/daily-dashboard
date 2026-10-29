package dashmcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/lifting"
	"dashboard/internal/problem"
	"dashboard/internal/store"
	"dashboard/internal/taskmcp"
	"dashboard/internal/text"
	"dashboard/internal/timewarrior"
	"dashboard/internal/workspace"
)

// Gateway runs the tools on one user's own dashboard.
type Gateway struct {
	ws  *workspace.Workspace
	now func() time.Time
}

func NewGateway(ws *workspace.Workspace) *Gateway { return &Gateway{ws: ws, now: time.Now} }

// Serve answers the sandbox's MCP server on listener until it's closed.
func (g *Gateway) Serve(listener net.Listener) {
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	server.Serve(listener)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/call" {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	var request struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(data, &request) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reply, _ := json.Marshal(g.Call(request.Tool, request.Arguments))
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}

// Has says whether a tool is one of these.
func Has(tool string) bool {
	return slices.ContainsFunc(Tools, func(t taskmcp.Tool) bool { return t.Name == tool })
}

// Call runs one tool. Whatever goes wrong comes back as an error result for the AI to read.
func (g *Gateway) Call(tool string, arguments json.RawMessage) taskmcp.Result {
	if !Has(tool) {
		return taskmcp.Result{Text: "unknown tool " + tool, IsError: true}
	}
	out, err := g.run(tool, arguments)
	if err != nil {
		return taskmcp.Result{Text: problem.Public(err), IsError: true}
	}
	return taskmcp.Result{Text: out}
}

// decode reads a tool's arguments; unknown fields are mistakes (a misspelled field would
// otherwise be dropped without a word).
func decode(arguments json.RawMessage, into any) error {
	if trimmed := bytes.TrimSpace(arguments); len(trimmed) == 0 || string(trimmed) == "null" {
		arguments = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return problem.Invalidf("bad arguments: " + err.Error())
	}
	return nil
}

// asJSON is a read tool's answer: indented JSON, which the AI reads easily.
func asJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", " ")
	return string(data), err
}

func (g *Gateway) run(tool string, arguments json.RawMessage) (string, error) {
	switch tool {
	case "dashboard_overview":
		var in struct {
			Day string `json:"day"`
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.overview(in.Day)
	case "checklist_set":
		var in struct{ Item, State, Reason, Day string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.checklistSet(in.Item, in.State, in.Reason, in.Day)
	case "checklist_items":
		var in struct{ Items []string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		items := cleanList(in.Items, 80, 40)
		return "Saved the checklist: " + strings.Join(items, ", "), g.ws.Store.UpdatePlan(func(p *store.Plan) { p.Checklist = items })
	case "weight_log":
		var in struct {
			Weight       float64
			Unit, Day    string
			Remove       bool
			MyFitnessPal *bool
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.weightLog(in.Weight, in.Unit, in.Day, in.Remove, in.MyFitnessPal)
	case "weight_plan":
		var in struct {
			Start     *float64 `json:"start"`
			StartDate *string  `json:"start_date"`
			Target    *string  `json:"target"`
			Lowest    *float64 `json:"lowest"`
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.weightPlan(in.Start, in.StartDate, in.Target, in.Lowest)
	case "week_set":
		var in struct{ Schedule map[string]string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.weekSet(in.Schedule)
	case "reminders_set":
		var in struct{ Reminders []string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		notes := cleanList(in.Reminders, 300, 50)
		return fmt.Sprintf("Saved %d reminders.", len(notes)), g.ws.Store.UpdatePlan(func(p *store.Plan) { p.Notes = notes })
	case "goals_set":
		var in struct{ Goals []store.Goal }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		var goals []store.Goal
		for _, goal := range in.Goals {
			goal = store.Goal{Label: text.Truncate(text.Collapse(goal.Label), 60), Value: text.Truncate(text.Collapse(goal.Value), 30), Unit: text.Truncate(text.Collapse(goal.Unit), 20)}
			if goal.Label != "" && len(goals) < 20 {
				goals = append(goals, goal)
			}
		}
		return fmt.Sprintf("Saved %d goals.", len(goals)), g.ws.Store.UpdatePlan(func(p *store.Plan) { p.Goals = goals })
	case "lifting_overview":
		var in struct {
			Workouts *int `json:"workouts"`
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		count := 5
		if in.Workouts != nil {
			count = max(0, min(*in.Workouts, 50))
		}
		return g.liftingOverview(count)
	case "lifting_history":
		var in struct {
			Exercise string
			Limit    int
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.liftingHistory(in.Exercise, in.Limit)
	case "lifting_goal":
		var in struct {
			Exercise  string
			OneRepMax float64 `json:"one_rep_max"`
			Unit      string
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.liftingGoal(in.Exercise, in.OneRepMax, in.Unit)
	case "lifting_log":
		var in struct {
			Exercise string
			Sets     []struct {
				Weight  float64
				Reps    int
				Seconds int
				Note    string
			}
			Unit string
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.liftingLog(in.Exercise, in.Sets, in.Unit)
	case "lifting_routine_save":
		var in struct{ Routine lifting.Routine }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		var saved lifting.Routine
		err := g.ws.Lifting.Update(func(d *lifting.Data) error {
			var err error
			saved, err = d.SaveRoutine(in.Routine)
			return err
		})
		if err != nil {
			return "", err
		}
		return asJSON(map[string]any{"saved": true, "routine": saved})
	case "lifting_routine_delete":
		var in struct{ ID string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		name := ""
		err := g.ws.Lifting.Update(func(d *lifting.Data) error {
			routine, ok := d.Routine(in.ID)
			if !ok {
				return problem.NotFoundf("no routine has that id: see lifting_overview")
			}
			name = routine.Name
			d.DeleteRoutine(in.ID)
			return nil
		})
		return "Deleted the routine “" + name + "”.", err
	case "lifting_start":
		var in struct{ Routine, Day string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		err := g.ws.Lifting.Update(func(d *lifting.Data) error {
			if in.Routine == "" {
				return d.StartEmpty(g.now())
			}
			return d.Start(in.Routine, in.Day, g.now())
		})
		if err != nil {
			return "", err
		}
		return "Started the workout: it's on the Lifting tab.", nil
	case "time_list":
		var in struct{ From, To string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		return g.timeList(in.From, in.To)
	case "time_track":
		var in struct {
			Start, End string
			Tags       []string
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		if err := g.ws.Time.Track(in.Start, in.End, in.Tags); err != nil {
			return "", err
		}
		return fmt.Sprintf("Logged %s to %s as %s.", in.Start, in.End, strings.Join(in.Tags, " ")), nil
	case "time_start":
		var in struct{ Tags []string }
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		tags, err := timewarrior.CleanTags(in.Tags)
		if err != nil {
			return "", problem.Invalidf(err.Error())
		}
		if result := g.ws.Time.Run(append([]string{"start"}, tags...)...); !result.OK() {
			return "", problem.Conflictf(result.FirstErrorLine("timew error"))
		}
		return "Tracking " + strings.Join(tags, " ") + " from now.", nil
	case "time_stop":
		if err := decode(arguments, &struct{}{}); err != nil {
			return "", err
		}
		if result := g.ws.Time.Run("stop"); !result.OK() {
			return "", problem.Conflictf(result.FirstErrorLine("nothing is being tracked"))
		}
		return "Stopped tracking.", nil
	case "time_edit":
		var in struct {
			ID         int      `json:"id"`
			StartWas   string   `json:"start_was"`
			Tags       []string `json:"tags"`
			Start, End string
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		if err := g.ws.Time.EditInterval(in.ID, in.StartWas, timewarrior.IntervalEdit{Tags: in.Tags, Start: in.Start, End: in.End}); err != nil {
			return "", err
		}
		return "Changed the entry.", nil
	case "time_delete":
		var in struct {
			ID       int    `json:"id"`
			StartWas string `json:"start_was"`
		}
		if err := decode(arguments, &in); err != nil {
			return "", err
		}
		if err := g.ws.Time.DeleteInterval(in.ID, in.StartWas); err != nil {
			return "", err
		}
		return "Deleted the entry.", nil
	}
	return "", problem.Invalidf("unknown tool " + tool)
}

// ---------- days ----------

var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// day reads a day as the tools take it: "", "today", "yesterday" or YYYY-MM-DD, not in the future.
func (g *Gateway) day(typed string) (string, error) {
	now := g.now()
	switch strings.ToLower(strings.TrimSpace(typed)) {
	case "", "today":
		return now.Format("2006-01-02"), nil
	case "yesterday":
		return now.AddDate(0, 0, -1).Format("2006-01-02"), nil
	}
	if !dayPattern.MatchString(typed) {
		return "", problem.Invalidf(`a day is "today", "yesterday" or YYYY-MM-DD`)
	}
	if t, err := time.ParseInLocation("2006-01-02", typed, time.Local); err != nil || t.After(now) {
		return "", problem.Invalidf("that day is in the future or isn't a date")
	}
	return typed, nil
}

func cleanList(items []string, length, count int) []string {
	cleaned := []string{}
	for _, item := range items {
		if item = text.Truncate(text.Collapse(item), length); item != "" && len(cleaned) < count {
			cleaned = append(cleaned, item)
		}
	}
	return cleaned
}

// ---------- Today ----------

func (g *Gateway) overview(typed string) (string, error) {
	day, err := g.day(typed)
	if err != nil {
		return "", err
	}
	plan, err := g.ws.Store.LoadPlan()
	if err != nil {
		return "", err
	}
	type item struct {
		Name   string `json:"name"`
		State  string `json:"state"` // done, skipped, not done
		Reason string `json:"reason,omitempty"`
	}
	states := g.ws.Store.Checklist()[day]
	var checklist []item
	for _, name := range plan.Checklist {
		entry := item{Name: name, State: "not done"}
		switch state := states[name].(type) {
		case string:
			entry.State = map[string]string{"done": "done", "skip": "skipped"}[state]
		case map[string]any:
			entry.State, entry.Reason = "skipped", fmt.Sprint(state["note"])
		}
		checklist = append(checklist, entry)
	}
	weights := g.ws.Store.Weights()
	recent := weights[max(0, len(weights)-14):]
	week := map[string]string{}
	for _, weekday := range store.Weekdays {
		week[weekday] = plan.Sessions[plan.Schedule[weekday]].Title
	}
	t, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	answer := map[string]any{
		"day": day, "weekday": t.Weekday().String(), "checklist": checklist,
		"weight":    map[string]any{"plan_lb": plan.Weight, "recent_weigh_ins_lb": recent},
		"this_week": week, "todays_training": week[t.Weekday().String()],
		"reminders": plan.Notes, "goals": plan.Goals,
	}
	if len(weights) > 0 {
		answer["weight"].(map[string]any)["target_reached"] = store.TargetReached(weights[len(weights)-1].LB, plan.Weight.WithStart(weights))
	}
	if active := g.ws.Lifting.Load().Active; active != nil {
		answer["workout_in_progress"] = active.Title
	}
	return asJSON(answer)
}

func (g *Gateway) checklistSet(item, state, reason, typedDay string) (string, error) {
	day, err := g.day(typedDay)
	if err != nil {
		return "", err
	}
	name := ""
	for _, known := range g.ws.Store.PlanChecklist() {
		if strings.EqualFold(strings.TrimSpace(known), strings.TrimSpace(item)) {
			name = known
		}
	}
	if name == "" {
		return "", problem.Invalidf("the checklist has no item called “" + item + "”: see dashboard_overview")
	}
	items := g.ws.Store.Checklist()[day]
	if items == nil {
		items = map[string]any{}
	}
	reason = text.Truncate(text.Collapse(reason), 300)
	switch state {
	case "done":
		items[name] = "done"
	case "skip":
		items[name] = "skip"
		if reason != "" {
			items[name] = map[string]any{"state": "skip", "note": reason}
		}
	case "clear":
		delete(items, name)
	default:
		return "", problem.Invalidf("state is done, skip or clear")
	}
	if err := g.ws.Store.SaveChecklistDay(day, items); err != nil {
		return "", err
	}
	done := map[string]string{"done": "Ticked", "skip": "Skipped", "clear": "Cleared"}[state]
	if reason != "" && state == "skip" {
		return fmt.Sprintf("%s “%s” on %s: %s.", done, name, day, reason), nil
	}
	return fmt.Sprintf("%s “%s” on %s.", done, name, day), nil
}

func (g *Gateway) weightLog(weight float64, unit, typedDay string, remove bool, toMyFitnessPal *bool) (string, error) {
	day, err := g.day(typedDay)
	if err != nil {
		return "", err
	}
	if remove {
		if err := g.ws.Store.SetWeight(day, nil); err != nil {
			return "", err
		}
		return "Removed the weigh-in on " + day + " (remove it in MyFitnessPal too, or its next sync brings it back).", nil
	}
	lb := weight
	if unit == "kg" {
		lb = weight / 0.45359237
	}
	if lb <= 50 || lb >= 700 {
		return "", problem.Invalidf("that isn't a body weight")
	}
	lb = math.Round(lb*10) / 10
	if err := g.ws.Store.SetWeight(day, &lb); err != nil {
		return "", err
	}
	g.ws.Store.TickWeighIn(day)
	message := fmt.Sprintf("Logged %s lb on %s.", store.FormatPounds(lb), day)
	if g.ws.MyFitnessPal == nil || !g.ws.MyFitnessPal.Client().Setup().SignedIn {
		return message, nil
	}
	if toMyFitnessPal == nil {
		plan, err := g.ws.Store.LoadPlan()
		if err != nil {
			return "", err
		}
		send := !plan.Weight.NoMyFitnessPal
		toMyFitnessPal = &send
	}
	if !*toMyFitnessPal {
		return message + " Not sent to MyFitnessPal.", nil
	}
	if err := g.ws.MyFitnessPal.LogWeight(day, lb); err != nil {
		message += " It wasn't sent to MyFitnessPal: " + problem.Public(err)
	} else {
		message += " Sent to MyFitnessPal."
	}
	return message, nil
}

func (g *Gateway) weightPlan(start *float64, startDate, target *string, lowest *float64) (string, error) {
	if startDate != nil && *startDate != "" && !dayPattern.MatchString(*startDate) {
		return "", problem.Invalidf("start_date is YYYY-MM-DD")
	}
	for _, v := range []*float64{start, lowest} {
		if v != nil && (*v <= 50 || *v >= 700) {
			return "", problem.Invalidf("weights are in lb, between 50 and 700")
		}
	}
	err := g.ws.Store.UpdatePlan(func(p *store.Plan) {
		if start != nil {
			p.Weight.Start = *start
		}
		if startDate != nil && *startDate != "" {
			p.Weight.StartDate = *startDate
		}
		if target != nil {
			p.Weight.Target = store.FlexText(text.Truncate(text.Collapse(*target), 30))
		}
		if lowest != nil {
			p.Weight.Best = *lowest
		}
	})
	return "Saved the weight plan.", err
}

func (g *Gateway) weekSet(schedule map[string]string) (string, error) {
	var done []string
	err := g.ws.Store.UpdatePlan(func(p *store.Plan) {
		for typedDay, session := range schedule {
			weekday := ""
			for _, d := range store.Weekdays {
				if strings.EqualFold(d, strings.TrimSpace(typedDay)) {
					weekday = d
				}
			}
			session = text.Truncate(text.Collapse(session), 80)
			if weekday == "" || session == "" {
				continue
			}
			p.Schedule[weekday] = p.SessionKey(session)
			done = append(done, weekday+": "+p.Sessions[p.Schedule[weekday]].Title)
		}
	})
	if err != nil {
		return "", err
	}
	if len(done) == 0 {
		return "", problem.Invalidf("name weekdays (Monday … Sunday) and sessions")
	}
	slices.SortFunc(done, func(a, b string) int {
		return slices.Index(store.Weekdays, strings.Split(a, ":")[0]) - slices.Index(store.Weekdays, strings.Split(b, ":")[0])
	})
	return "Saved this week: " + strings.Join(done, ", ") + ".", nil
}

// ---------- Lifting ----------

func (g *Gateway) liftingOverview(count int) (string, error) {
	data := g.ws.Lifting.Load()
	recent := data.Workouts[max(0, len(data.Workouts)-count):]
	slices.Reverse(recent)
	type goal struct {
		Exercise string  `json:"exercise"`
		Goal     float64 `json:"one_rep_max_goal_lb"`
		OneRM    float64 `json:"actual_one_rep_max_lb,omitempty"`
		Reached  bool    `json:"reached"`
	}
	goals := []goal{}
	for _, g := range data.Goals {
		oneRM, _ := data.OneRM(g.Name, time.Time{})
		goals = append(goals, goal{g.Name, g.LB, oneRM, oneRM >= g.LB})
	}
	return asJSON(map[string]any{"unit": data.Unit, "weights_are_in": "lb", "routines": data.Routines,
		"workout_in_progress": data.Active, "recent_workouts": recent, "goals": goals})
}

func (g *Gateway) liftingHistory(name string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}
	history := g.ws.Lifting.Load().History(name)
	if len(history) == 0 {
		return "No finished workout has “" + name + "”.", nil
	}
	type session struct {
		Day      string        `json:"day"`
		Workout  string        `json:"workout"`
		Sets     []lifting.Set `json:"sets_lb"`
		BestE1RM float64       `json:"best_e1rm_lb,omitempty"`
		Single   float64       `json:"actual_one_rep_max_lb,omitempty"`
		Record   bool          `json:"record,omitempty"`
	}
	var sessions []session
	for i := len(history) - 1; i >= 0 && len(sessions) < limit; i-- {
		h := history[i]
		sessions = append(sessions, session{Day: h.Day.Format("2006-01-02"), Workout: h.Title, Sets: h.Sets, BestE1RM: math.Round(h.Best*10) / 10, Single: h.Single, Record: h.Record})
	}
	return asJSON(sessions)
}

func (g *Gateway) liftingLog(name string, typed []struct {
	Weight  float64
	Reps    int
	Seconds int
	Note    string
}, unit string) (string, error) {
	where := ""
	var logged []lifting.Set
	err := g.ws.Lifting.Update(func(d *lifting.Data) error {
		if unit == "" {
			unit = d.Unit
		}
		hold := false
		for _, t := range typed {
			hold = hold || t.Seconds > 0
			logged = append(logged, lifting.Set{LB: lifting.FromShown(t.Weight, unit), Reps: t.Reps, Secs: t.Seconds, Note: t.Note})
		}
		var err error
		where, err = d.LogSets(name, logged, hold, g.now())
		return err
	})
	if err != nil {
		return "", err
	}
	var parts []string
	for _, set := range typed {
		weight := "bodyweight"
		if set.Weight > 0 {
			weight = strconv.FormatFloat(set.Weight, 'f', -1, 64) + " " + unit
		}
		if set.Seconds > 0 {
			parts = append(parts, fmt.Sprintf("%s for %d s", weight, set.Seconds))
		} else {
			parts = append(parts, fmt.Sprintf("%s × %d", weight, set.Reps))
		}
	}
	return fmt.Sprintf("Logged %s: %s, in %s.", strings.TrimSpace(name), strings.Join(parts, ", "), where), nil
}

func (g *Gateway) liftingGoal(name string, oneRM float64, unit string) (string, error) {
	var current float64
	err := g.ws.Lifting.Update(func(d *lifting.Data) error {
		if unit != "kg" && unit != "lb" {
			unit = d.Unit
		}
		current, _ = d.OneRM(name, time.Time{})
		return d.SetGoal(name, lifting.FromShown(oneRM, unit))
	})
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if oneRM == 0 {
		return "Cleared the one-rep max goal for " + name + ".", nil
	}
	message := fmt.Sprintf("%s one-rep max goal: %s %s.", name, strconv.FormatFloat(oneRM, 'f', -1, 64), unit)
	if current == 0 {
		return message + " There's no single logged for it yet.", nil
	}
	return message + fmt.Sprintf(" Actual one-rep max so far: %s %s.", lifting.FormatShown(current, unit), unit), nil
}

// ---------- Timewarrior ----------

func (g *Gateway) timeList(typedFrom, typedTo string) (string, error) {
	from, err := g.day(typedFrom)
	if err != nil {
		return "", err
	}
	to := from
	if typedTo != "" {
		if to, err = g.day(typedTo); err != nil {
			return "", err
		}
	}
	start, _ := time.ParseInLocation("2006-01-02", from, time.Local)
	end, _ := time.ParseInLocation("2006-01-02", to, time.Local)
	if end.Before(start) || end.Sub(start) > 92*24*time.Hour {
		return "", problem.Invalidf("from must come before to, at most three months apart")
	}
	intervals, err := g.ws.Time.IntervalsBetween(start, end.AddDate(0, 0, 1))
	if err != nil {
		return "", err
	}
	type entry struct {
		ID       int      `json:"id"`
		StartWas string   `json:"start_was"`
		Start    string   `json:"start"`
		End      string   `json:"end,omitempty"`
		Running  bool     `json:"running,omitempty"`
		Tags     []string `json:"tags"`
		Minutes  int      `json:"minutes"`
	}
	entries := []entry{}
	now := g.now()
	for _, i := range intervals {
		e := entry{ID: i.ID, StartWas: i.Start, Start: i.StartTime().Format("2006-01-02T15:04"), Tags: i.Tags,
			Minutes: int(math.Round(i.Duration(now).Minutes()))}
		if t, running := i.EndTime(); running {
			e.Running = true
		} else {
			e.End = t.Format("2006-01-02T15:04")
		}
		entries = append(entries, e)
	}
	return asJSON(map[string]any{"from": from, "to": to, "entries": entries})
}

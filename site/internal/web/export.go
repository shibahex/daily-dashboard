package web

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/lifting"
	"dashboard/internal/problem"
)

// The Data tab's "Download your data": one zip with what the dashboard keeps for the user that
// the server can read, in open formats (JSON as the dashboard and Taskwarrior keep it, plus CSV
// for the lifting log and weigh-ins), and a README saying what each file is. The page ticks which
// dataParts go in (?part=, repeated; none means all). Notes, chat and AI keys are encrypted in the
// browser, which adds them (web/js/backup.js); logins aren't data, so they stay out. Import data
// reads the same zip (import.go).

// dataPart is one thing the Data tab can download or import on its own.
type dataPart struct {
	ID, Label, Hint string
	Browser         bool // the browser adds and imports it; the server can't read it
	Off             bool // not ticked for download at first
	Readme          string
}

var dataParts = []dataPart{
	{ID: "plan", Label: "Plan", Hint: "Goals, checklist items, training week, reminders, Today's layout. Importing it replaces yours.",
		Readme: "| plan.json | Your plan: goals, the daily checklist items, the training week, reminders, the Today layout |\n"},
	{ID: "checklist", Label: "Checklist history", Hint: "Each day's ticks and skips",
		Readme: "| checklist.json | Each day's checklist: \"done\", \"skip\", or {\"state\": \"skip\", \"note\": why} |\n"},
	{ID: "weights", Label: "Weigh-ins", Hint: "JSON and CSV",
		Readme: "| weights.json, weights.csv | Weigh-ins by day |\n"},
	{ID: "lifting", Label: "Lifting", Hint: "Routines and workouts, and every set as a spreadsheet",
		Readme: "| lifting.json | The Lifting tab as the dashboard keeps it: routines (days of planned exercises), finished workouts (sets with weight in lb, reps, notes, skips and why), the workout in progress, and the Strength chart's settings |\n" +
			"| lifting-sets.csv | Every finished set, one row each, for a spreadsheet or another lifting app. est_1rm_lb is Epley's estimate: weight × (1 + reps / 30) |\n"},
	{ID: "tasks", Label: "Tasks", Hint: "Every Taskwarrior task",
		Readme: "| tasks.json | Every Taskwarrior task (pending, completed, deleted, repeating), from \"task export\". Load it with \"task import tasks.json\" |\n"},
	{ID: "time", Label: "Time", Hint: "Every Timewarrior entry",
		Readme: "| time.json | Every Timewarrior entry, from \"timew export\" |\n"},
	{ID: "anki", Label: "Anki history", Hint: "Reviews per day",
		Readme: "| anki-history.json | Anki reviews per day, as last seen (if you use Anki) |\n"},
	{ID: "notes", Label: "Notes", Hint: "One Markdown file each", Browser: true},
	{ID: "chat", Label: "AI chat", Hint: "The MCPs tab's conversation", Browser: true},
	{ID: "ai-settings", Label: "AI keys and local model", Hint: "Anyone with the file can read them", Browser: true, Off: true},
}

const exportReadme = `# Your Daily Dashboard data

Exported %s. Weights are in pounds unless a column says kg; times are local.

| File | What it is |
|---|---|
%s%s
Not included: your notes, AI chat and AI keys (encrypted in your browser: the server can't read them), your
logins (MyFitnessPal, AnkiConnect, sessions), and your food diary (it lives in MyFitnessPal, which has its own export).
`

// exportWanted says whether the request asked for a part: all of them when it names none.
func exportWanted(r *http.Request) func(string) bool {
	parts := r.URL.Query()["part"]
	return func(id string) bool { return len(parts) == 0 || slices.Contains(parts, id) }
}

// exportData answers with the zip.
func (ui *UI) exportData(w http.ResponseWriter, r *http.Request) {
	ws := ui.ws(r)
	want := exportWanted(r)
	now := time.Now()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="dashboard-export-%s.zip"`, now.Format(dayLayout)))
	w.Header().Set("Cache-Control", "no-store")
	archive := zip.NewWriter(w)
	var missing []string
	add := func(name string, data []byte) {
		if f, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now}); err == nil {
			f.Write(data)
		}
	}
	addFile := func(name, path string) {
		if data, err := os.ReadFile(path); err == nil {
			add(name, data)
		} else if !os.IsNotExist(err) {
			missing = append(missing, name+" (couldn't be read)")
		}
	}

	if want("plan") {
		addFile("plan.json", ws.Paths.Plan())
	}
	if want("checklist") {
		addFile("checklist.json", ws.Paths.DataFile("checklist.json"))
	}
	if want("weights") {
		addFile("weights.json", ws.Paths.DataFile("weights.json"))
		var weights strings.Builder
		csvWeights := csv.NewWriter(&weights)
		csvWeights.Write([]string{"date", "lb", "kg"})
		for _, weight := range ws.Store.Weights() {
			csvWeights.Write([]string{weight.Date, strconv.FormatFloat(weight.LB, 'f', -1, 64), lifting.FormatShown(weight.LB, "kg")})
		}
		csvWeights.Flush()
		add("weights.csv", []byte(weights.String()))
	}
	if want("lifting") {
		addFile("lifting.json", ws.Paths.DataFile("lifting.json"))
		var sets strings.Builder
		ws.Lifting.Load().WriteCSV(&sets)
		add("lifting-sets.csv", []byte(sets.String()))
	}
	if want("tasks") {
		if tasks, err := ws.Tasks.Export(); err == nil {
			add("tasks.json", tasks)
		} else {
			missing = append(missing, "tasks.json ("+problem.Public(err)+")")
		}
	}
	if want("time") {
		if intervals, result := ws.Time.Export(); result.OK() {
			add("time.json", intervals)
		} else {
			missing = append(missing, "time.json ("+result.FirstErrorLine("timew export failed")+")")
		}
	}
	if want("anki") {
		addFile("anki-history.json", ws.Paths.DataFile("anki_history.json"))
	}

	var rows strings.Builder
	for _, part := range dataParts {
		if want(part.ID) {
			rows.WriteString(part.Readme)
		}
	}
	problems := ""
	if len(missing) > 0 {
		problems = "\nLeft out this time, because of an error: " + strings.Join(missing, ", ") + ".\n"
	}
	add("README.md", []byte(fmt.Sprintf(exportReadme, now.Format("2006-01-02 15:04"), rows.String(), problems)))
	if err := archive.Close(); err != nil {
		log.Printf("export: %v", err)
	}
}

// exportLifting answers with lifting-sets.csv alone (the Lifting tab's Export link).
func (ui *UI) exportLifting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="lifting-%s.csv"`, time.Now().Format(dayLayout)))
	w.Header().Set("Cache-Control", "no-store")
	ui.ws(r).Lifting.Load().WriteCSV(w)
}

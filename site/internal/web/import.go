package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"dashboard/internal/jsonfile"
	"dashboard/internal/problem"
)

// The Data tab's "Import data": the files of a "Download your data" zip, from this dashboard or
// another one, so moving to a new server keeps everything. The browser opens the zip
// (web/js/backup.js), keeps the notes, chat and AI settings for itself (it seals them again
// there; they're never sent here) and posts the parts the user ticked, one form file each. Nothing is deleted:
// the plan is replaced, the rest is added (see each Import).

const maxImport = 64 << 20

// importFiles are the form files, in the order they're imported.
var importFiles = []string{"plan.json", "checklist.json", "weights.json", "lifting.json", "tasks.json", "time.json", "anki-history.json"}

func (ui *UI) importData(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImport)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, problem.Invalidf("That's too big, or not an upload (at most 64 MB)"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	ws := ui.ws(r)
	done, problems := []string{}, []string{} // never null in the JSON: the browser spreads them
	note := func(err error, format string, args ...any) {
		if err != nil {
			problems = append(problems, problem.Public(err))
		} else if format != "" {
			done = append(done, fmt.Sprintf(format, args...))
		}
	}
	for _, name := range importFiles {
		data, ok, err := formFile(r, name)
		if err != nil {
			note(problem.Invalidf("Couldn't read "+name), "")
			continue
		}
		if !ok {
			continue
		}
		switch name {
		case "plan.json":
			note(ws.Store.ImportPlan(data), "your plan")
		case "checklist.json":
			n, err := ws.Store.ImportChecklist(data)
			note(err, "%d checklist %s", n, plural(n, "day", "days"))
		case "weights.json":
			n, err := ws.Store.ImportWeights(data)
			note(err, "%d %s", n, plural(n, "weigh-in", "weigh-ins"))
		case "lifting.json":
			routines, workouts, err := ws.Lifting.Import(data)
			note(err, "%d %s and %d %s", routines, plural(routines, "routine", "routines"), workouts, plural(workouts, "workout", "workouts"))
		case "tasks.json":
			n, err := ws.Tasks.Import(data)
			note(err, "%d %s", n, plural(n, "task", "tasks"))
		case "time.json":
			added, skipped, err := ws.Time.Import(data)
			if err == nil && skipped > 0 {
				note(nil, "%d time %s (%d already here or overlapping, skipped)", added, plural(added, "entry", "entries"), skipped)
			} else {
				note(err, "%d time %s", added, plural(added, "entry", "entries"))
			}
		case "anki-history.json":
			n, err := importAnkiHistory(ws.Paths.DataFile("anki_history.json"), data)
			note(err, "%d days of Anki reviews", n)
		}
	}
	if len(done) > 0 {
		w.Header().Set("HX-Trigger", "tasks-changed")
	}
	writeNoStoreJSON(w, map[string]any{"done": done, "problems": problems})
}

// formFile is the uploaded file called name, if there is one.
func formFile(r *http.Request, name string) ([]byte, bool, error) {
	file, _, err := r.FormFile(name)
	if err == http.ErrMissingFile {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	return data, err == nil, err
}

// importAnkiHistory adds the days of an exported Anki history that the saved copy doesn't have
// (Anki's own count wins: it's refreshed whenever Anki is open).
func importAnkiHistory(path string, data []byte) (int, error) {
	type history struct {
		ByDay [][2]json.RawMessage `json:"by_day"`
		At    float64              `json:"at"`
	}
	var imported, saved history
	if err := json.Unmarshal(data, &imported); err != nil {
		return 0, problem.Invalidf("anki-history.json isn't an Anki history")
	}
	if _, err := os.Stat(path); err == nil {
		jsonfile.Read(path, &saved)
	}
	have := map[string]bool{}
	for _, pair := range saved.ByDay {
		var day string
		if json.Unmarshal(pair[0], &day) == nil {
			have[day] = true
		}
	}
	added := 0
	for _, pair := range imported.ByDay {
		var day string
		var count int
		if json.Unmarshal(pair[0], &day) != nil || json.Unmarshal(pair[1], &count) != nil || have[day] {
			continue
		}
		saved.ByDay = append(saved.ByDay, pair)
		have[day] = true
		added++
	}
	if saved.At == 0 {
		saved.At = imported.At
	}
	return added, jsonfile.Write(path, saved)
}

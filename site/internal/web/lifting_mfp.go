package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"dashboard/internal/lifting"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
)

// The Lifting tab's link to MyFitnessPal: a finished workout goes to its exercise diary (each
// lift as one strength row, the time as strength-training cardio), with each lift matched to one
// of MyFitnessPal's exercises once and remembered.

func (ui *UI) myFitnessPalConnected(r *http.Request) bool {
	return ui.ws(r).MyFitnessPal.Client().Setup().SignedIn
}

// sendWorkout logs a finished workout in MyFitnessPal. Lifts without a match yet are looked up
// first (the exact name, else MyFitnessPal's top result) and the matches kept. It answers the
// lifts that had no match and so were left out.
func (ui *UI) sendWorkout(r *http.Request, workout lifting.Workout) (left []string, err error) {
	ws := ui.ws(r)
	rows := workout.Summaries()
	if len(rows) == 0 && workout.Minutes == 0 {
		return nil, problem.Invalidf("this workout has no sets with reps or time to send")
	}
	matches := ws.Lifting.Load().MyFitnessPal.Matches
	found := make([]*lifting.Match, len(rows))
	errs := make([]error, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		if match, ok := matches[lifting.MatchKey(row.Name)]; ok {
			found[i] = &match
			continue
		}
		wg.Go(func() {
			results, err := ws.MyFitnessPal.Client().SearchStrength(row.Name)
			if errs[i] = err; err == nil && len(results) > 0 {
				found[i] = &lifting.Match{ID: results[0].ID, Name: results[0].Name}
				for _, result := range results {
					if strings.EqualFold(result.Name, row.Name) {
						found[i].ID, found[i].Name = result.ID, result.Name
					}
				}
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	err = ws.Lifting.Update(func(d *lifting.Data) error {
		for i, row := range rows {
			if found[i] != nil {
				if _, known := d.MyFitnessPal.Matches[lifting.MatchKey(row.Name)]; !known {
					d.SetMatch(row.Name, *found[i])
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var entries []myfitnesspal.StrengthEntry
	for i, row := range rows {
		if found[i] == nil {
			left = append(left, row.Name)
			continue
		}
		entries = append(entries, myfitnesspal.StrengthEntry{ExerciseID: found[i].ID, Sets: row.Sets, Reps: row.Reps, LB: row.LB})
	}
	if err := ws.MyFitnessPal.LogWorkout(workout.Started.Format("2006-01-02"), entries, workout.Minutes); err != nil {
		return nil, err
	}
	return left, ws.Lifting.Update(func(d *lifting.Data) error { d.MarkSent(workout.ID); return nil })
}

// leftOutText names the lifts a sent workout went without, for the toast.
func leftOutText(left []string) string {
	if len(left) == 0 {
		return ""
	}
	return fmt.Sprintf(" without %s (no match there: pick one under MyFitnessPal matches and send it by hand)", strings.Join(left, ", "))
}

// sendPastWorkout sends a finished workout that isn't in MyFitnessPal yet.
func (ui *UI) sendPastWorkout(w http.ResponseWriter, r *http.Request) {
	workout, ok := ui.ws(r).Lifting.Load().Workout(r.PathValue("id"))
	if !ok {
		fail(w, problem.NotFoundf("that workout is gone"))
		return
	}
	if workout.Sent {
		fail(w, problem.Conflictf("that workout is already in MyFitnessPal"))
		return
	}
	left, err := ui.sendWorkout(r, workout)
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Sent to MyFitnessPal"+leftOutText(left)), liftingChanged, nutritionChanged)
	nothing(w)
}

// ---------- matches ----------

type mfpMatchesView struct {
	Lifts []mfpMatchRow
}

type mfpMatchRow struct {
	Name  string
	Match string // MyFitnessPal's name for it; empty until it's matched
}

func (ui *UI) mfpMatchesView(r *http.Request) mfpMatchesView {
	data := ui.ws(r).Lifting.Load()
	var view mfpMatchesView
	for _, name := range data.ExerciseNames() {
		view.Lifts = append(view.Lifts, mfpMatchRow{Name: name, Match: data.MyFitnessPal.Matches[lifting.MatchKey(name)].Name})
	}
	return view
}

// mfpMatches is the panel of lifts and the MyFitnessPal exercises they're logged as.
func (ui *UI) mfpMatches(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "lift-mfp", ui.mfpMatchesView(r))
}

type mfpSearchView struct {
	Name    string
	Results []myfitnesspal.Exercise
}

// mfpSearch looks up MyFitnessPal's exercises for one lift.
func (ui *UI) mfpSearch(w http.ResponseWriter, r *http.Request) {
	results, err := ui.ws(r).MyFitnessPal.Client().SearchStrength(r.FormValue("q"))
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "lift-mfp-results", mfpSearchView{Name: r.FormValue("name"), Results: results})
}

// setMfpMatch keeps the MyFitnessPal exercise picked for a lift.
func (ui *UI) setMfpMatch(w http.ResponseWriter, r *http.Request) {
	name, id, label := formText(r, "name"), formText(r, "id"), formText(r, "label")
	if lifting.MatchKey(name) == "" || !isDigits(id) || label == "" {
		fail(w, problem.Invalidf("pick an exercise"))
		return
	}
	if err := ui.ws(r).Lifting.Update(func(d *lifting.Data) error {
		d.SetMatch(name, lifting.Match{ID: id, Name: label})
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	announce(w, say(name+" → "+label))
	ui.render(w, "lift-mfp", ui.mfpMatchesView(r))
}

func isDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

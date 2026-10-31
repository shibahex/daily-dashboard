package web

import (
	"fmt"
	"net/http"
	"strings"

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
	left, err := ui.ws(r).SendWorkout(workout)
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

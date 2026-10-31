package workspace

import (
	"strings"
	"sync"
	"time"

	"dashboard/internal/lifting"
	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
	"dashboard/internal/timewarrior"
)

// Finished is what finishing a workout did, for the message the user sees.
type Finished struct {
	Workout lifting.Workout
	Goals   []string // lifts whose one-rep max goal it reached
	Ticked  bool     // its task was due and is now done
	Logged  bool     // its minutes are in Timewarrior (with the task's tags, or on their own)
	Sent    bool     // it's in MyFitnessPal
	LeftOut []string // lifts it went to MyFitnessPal without (no match there)
	Warning string   // what didn't work after the workout was saved
}

// FinishWorkout saves the workout in progress as ending at end. Then it ticks its task when one
// by that name is due (which logs the time with the task's tags) or, without one, logs the time
// as "Lifting" plus the workout's name. send, when set, says whether it also goes to
// MyFitnessPal, and is kept as the user's choice for next time.
func (w *Workspace) FinishWorkout(end time.Time, minutes int, note string, send *bool) (Finished, error) {
	var done Finished
	err := w.Lifting.Update(func(d *lifting.Data) error {
		var err error
		if done.Workout, err = d.Finish(end, minutes, note); err == nil && send != nil {
			d.MyFitnessPal.Off = !*send
		}
		return err
	})
	if err != nil {
		return Finished{}, err
	}
	finished := done.Workout
	done.Goals = w.Lifting.Load().GoalsReached(finished)
	var warnings []string
	uuid, due := "", false
	if finished.Task != "" {
		uuid, due = w.Tasks.DueNamed(finished.Task, end)
	}
	if due {
		if warning, err := w.Tasks.CompleteAt(uuid, end, minutes); err != nil {
			warnings = append(warnings, "Saved, but the task wasn't ticked: "+problem.Public(err))
		} else {
			done.Ticked = true
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}
	} else if minutes > 0 {
		start := end.Add(-time.Duration(minutes) * time.Minute)
		if err := w.Time.Track(start.Format(localMinute), end.Format(localMinute),
			timewarrior.TaskTags([]string{"Lifting", finished.Title})); err != nil {
			warnings = append(warnings, "Saved, but the time wasn't logged: "+problem.Public(err))
		}
	}
	done.Logged = minutes > 0 && len(warnings) == 0
	if send != nil && *send {
		if left, err := w.SendWorkout(finished); err != nil {
			warnings = append(warnings, "Saved, but not in MyFitnessPal: "+problem.Public(err)+" (send it from Workouts).")
		} else {
			done.Sent, done.LeftOut = true, left
		}
	}
	done.Warning = strings.Join(warnings, " ")
	return done, nil
}

const localMinute = "2006-01-02T15:04"

// SendWorkout logs a finished workout in MyFitnessPal. Lifts without a match yet are looked up
// first (the exact name, else MyFitnessPal's top result) and the matches kept. It answers the
// lifts that had no match and so were left out.
func (w *Workspace) SendWorkout(workout lifting.Workout) (left []string, err error) {
	rows := workout.Summaries()
	if len(rows) == 0 && workout.Minutes == 0 {
		return nil, problem.Invalidf("this workout has no sets with reps or time to send")
	}
	matches := w.Lifting.Load().MyFitnessPal.Matches
	found := make([]*lifting.Match, len(rows))
	errs := make([]error, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		if match, ok := matches[lifting.MatchKey(row.Name)]; ok {
			found[i] = &match
			continue
		}
		wg.Go(func() {
			results, err := w.MyFitnessPal.Client().SearchStrength(row.Name)
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
	err = w.Lifting.Update(func(d *lifting.Data) error {
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
	if err := w.MyFitnessPal.LogWorkout(workout.Started.Format("2006-01-02"), entries, workout.Minutes); err != nil {
		return nil, err
	}
	return left, w.Lifting.Update(func(d *lifting.Data) error { d.MarkSent(workout.ID); return nil })
}

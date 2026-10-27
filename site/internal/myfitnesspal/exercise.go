package myfitnesspal

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"dashboard/internal/problem"
	"dashboard/internal/text"
)

// The exercise diary has no API either: these use the classic site's "add exercise" pages. A
// strength entry is one row per exercise (sets, reps per set, weight per set) and burns no
// calories in MyFitnessPal, so a workout's time goes in as a cardio entry of its own.

// Exercise is one of MyFitnessPal's exercises.
type Exercise struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StrengthEntry is one exercise of a workout as MyFitnessPal keeps it: every set the same.
type StrengthEntry struct {
	ExerciseID string
	Sets, Reps int
	LB         float64 // 0 for bodyweight
}

// LiftingCardioID is MyFitnessPal's "Strength training (weight lifting, weight training)".
const LiftingCardioID = "209"

const (
	cardioType   = "0"
	strengthType = "1"
)

// SearchStrength looks a strength exercise up in MyFitnessPal's database.
func (c Client) SearchStrength(query string) ([]Exercise, error) {
	query = text.Truncate(text.Collapse(query), 80)
	if query == "" {
		return nil, problem.Invalidf("type an exercise to search for")
	}
	var found []Exercise
	err := c.call(lookupTimeout, func(ctx context.Context) (err error) {
		found, err = c.searchExercises(ctx, today(), "strength", query)
		return err
	})
	if err != nil {
		return nil, problem.Unavailablef(err.Error())
	}
	return found, nil
}

// LogWorkout adds a workout's exercises to date's exercise diary, and its minutes (when there
// are any) as strength-training cardio, with the calories MyFitnessPal's own form works out.
func (s *Syncer) LogWorkout(date string, entries []StrengthEntry, minutes int) error {
	for _, entry := range entries {
		if err := checkIDs(entry.ExerciseID); err != nil {
			return err
		}
		if entry.Sets < 1 || entry.Reps < 1 || entry.LB < 0 {
			return problem.Invalidf("bad sets")
		}
	}
	_, err := s.change(date, func(ctx context.Context) error {
		for _, entry := range entries {
			if err := s.client.addStrength(ctx, date, entry); err != nil {
				return err
			}
		}
		if minutes > 0 {
			return s.client.addLiftingCardio(ctx, date, minutes)
		}
		return nil
	})
	return err
}

func addPagePath(date, kind string) string {
	return "exercise/add_to_diary?date=" + date + "&type=" + kind
}

func (c Client) searchExercises(ctx context.Context, date, kind, query string) ([]Exercise, error) {
	s := c.session()
	page, err := s.getPage(ctx, addPagePath(date, kind))
	if err != nil {
		return nil, err
	}
	token, err := formToken(page, "/exercise/search")
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, request{method: http.MethodPost, url: siteURL + "exercise/search", api: true, form: url.Values{
		"authenticity_token": {token}, "type": {kind}, "date": {date}, "search": {query},
	}, headers: legacyFormHeaders(siteURL + addPagePath(date, kind))})
	if err != nil {
		return nil, err
	}
	if err := accepted(resp.status, "/exercise/search", http.StatusOK); err != nil {
		return nil, err
	}
	return parseExerciseSearch(resp.body)
}

func (c Client) addStrength(ctx context.Context, date string, entry StrengthEntry) error {
	form, err := c.exerciseForm(ctx, date, entry.ExerciseID)
	if err != nil {
		return err
	}
	weight := ""
	if entry.LB > 0 {
		value := entry.LB
		if form.kg {
			value *= 0.45359237
		}
		weight = strconv.FormatFloat(math.Round(value*10)/10, 'f', -1, 64)
	}
	return c.postExercise(ctx, date, url.Values{
		"authenticity_token":            {form.token},
		"exercise_entry[exercise_id]":   {entry.ExerciseID},
		"exercise_entry[date]":          {date},
		"exercise_entry[exercise_type]": {strengthType},
		"exercise_entry[sets]":          {strconv.Itoa(entry.Sets)},
		"exercise_entry[quantity]":      {strconv.Itoa(entry.Reps)},
		"exercise_entry[weight]":        {weight},
	}, "strength")
}

// addLiftingCardio logs minutes of strength training. Its calories are worked out as the form's
// own script does: METs × hours × the account's weight in kg × the page's calorie multiplier.
func (c Client) addLiftingCardio(ctx context.Context, date string, minutes int) error {
	page, err := c.session().getPage(ctx, addPagePath(date, "cardio"))
	if err != nil {
		return err
	}
	doc, err := parseHTML(page)
	if err != nil {
		return err
	}
	weightKG, _ := number(hiddenValue(doc, "current_weight"))
	multiplier, ok := number(hiddenValue(doc, "calorie_multiplier"))
	if !ok {
		multiplier = 1
	}
	form, err := c.exerciseForm(ctx, date, LiftingCardioID)
	if err != nil {
		return err
	}
	if form.mets == 0 || weightKG == 0 {
		return problem.Failedf("MyFitnessPal didn't say how many calories strength training burns")
	}
	calories := math.Round(form.mets * float64(minutes) / 60 * weightKG * multiplier)
	year, month, day := date[:4], strings.TrimPrefix(date[5:7], "0"), strings.TrimPrefix(date[8:10], "0")
	return c.postExercise(ctx, date, url.Values{
		"authenticity_token":             {form.token},
		"calorie_multiplier":             {formatG(multiplier)},
		"exercise_entry[exercise_id]":    {LiftingCardioID},
		"exercise_entry[date]":           {date},
		"exercise_entry[exercise_type]":  {cardioType},
		"exercise_entry[quantity]":       {strconv.Itoa(minutes)},
		"exercise_entry[start_time(1i)]": {year},
		"exercise_entry[start_time(2i)]": {month},
		"exercise_entry[start_time(3i)]": {day},
		"exercise_entry[start_time(4i)]": {"12"},
		"exercise_entry[start_time(5i)]": {"00"},
		"exercise_entry[calories]":       {strconv.Itoa(int(calories))},
	}, "cardio")
}

func (c Client) postExercise(ctx context.Context, date string, form url.Values, kind string) error {
	resp, err := c.session().do(ctx, request{method: http.MethodPost, url: siteURL + "exercise/add", api: true, form: form,
		headers: legacyFormHeaders(siteURL + addPagePath(date, kind))})
	if err != nil {
		return err
	}
	return accepted(resp.status, "/exercise/add", http.StatusOK, http.StatusFound)
}

// exerciseEntryForm is what adding an exercise needs from its entry form.
type exerciseEntryForm struct {
	token string
	mets  float64 // a cardio exercise's
	kg    bool    // weights are typed in kg (the account's unit)
}

func (c Client) exerciseForm(ctx context.Context, date, exerciseID string) (exerciseEntryForm, error) {
	page, err := c.session().getPage(ctx, "exercise/update_exercise/"+exerciseID+"?date="+date)
	if err != nil {
		return exerciseEntryForm{}, err
	}
	return parseExerciseForm(page)
}

// ---------- the pages ----------

// parseExerciseSearch reads the results of the add-exercise search, which link to each
// exercise's entry form.
func parseExerciseSearch(page []byte) ([]Exercise, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return nil, err
	}
	var found []Exercise
	for _, list := range find(doc, func(n *html.Node) bool { id, _ := attr(n, "id"); return n.Data == "ul" && id == "matching" }) {
		for _, anchor := range find(list, tag("a")) {
			href, _ := attr(anchor, "href")
			path, _, _ := strings.Cut(href, "?")
			id, ok := strings.CutPrefix(path, "/exercise/update_exercise/")
			if !ok || !numericID.MatchString(id) {
				continue
			}
			found = append(found, Exercise{ID: id, Name: textOf(anchor)})
		}
	}
	return found, nil
}

func parseExerciseForm(page []byte) (exerciseEntryForm, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return exerciseEntryForm{}, err
	}
	var form exerciseEntryForm
	if tokens := find(doc, named("input", "authenticity_token")); len(tokens) > 0 {
		form.token, _ = attr(tokens[0], "value")
	}
	if form.token == "" || len(find(doc, named("input", "exercise_entry[exercise_id]"))) == 0 {
		return exerciseEntryForm{}, problem.Failedf("Couldn't open that exercise in MyFitnessPal")
	}
	form.mets, _ = number(hiddenValue(doc, "mets"))
	// "Weight per set: lbs" or "kg" follows the weight box.
	for _, input := range find(doc, named("input", "exercise_entry[weight]")) {
		for next := input.NextSibling; next != nil; next = next.NextSibling {
			if unit := strings.ToLower(textOf(next)); unit != "" {
				form.kg = strings.HasPrefix(unit, "kg")
				break
			}
		}
	}
	return form, nil
}

// formToken is the token of the page's form that posts to action.
func formToken(page []byte, action string) (string, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return "", err
	}
	for _, form := range find(doc, tag("form")) {
		if a, _ := attr(form, "action"); a != action {
			continue
		}
		if tokens := find(form, named("input", "authenticity_token")); len(tokens) > 0 {
			token, _ := attr(tokens[0], "value")
			return token, nil
		}
	}
	return "", problem.Failedf("Couldn't read MyFitnessPal's exercise page")
}

func hiddenValue(doc *html.Node, id string) string {
	for _, input := range find(doc, func(n *html.Node) bool { v, _ := attr(n, "id"); return n.Data == "input" && v == id }) {
		value, _ := attr(input, "value")
		return value
	}
	return ""
}

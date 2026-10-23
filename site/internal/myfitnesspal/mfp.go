package myfitnesspal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/store"
)

// What the dashboard reads and changes in MyFitnessPal, straight over HTTP. A day is four
// requests sent at once: the diary page (entry ids), the diary API (every nutrient, and
// exercise), goals and weigh-ins.

const (
	weightLookback = 90 // days of weigh-ins a sync brings in
	pageReuse      = 10 * time.Minute
)

// ---------- reading a day ----------

type nutrition struct {
	Energy        struct{ Value float64 } `json:"energy"`
	Carbohydrates float64                 `json:"carbohydrates"`
	Protein       float64                 `json:"protein"`
	Fat           float64                 `json:"fat"`
	SaturatedFat  float64                 `json:"saturated_fat"`
	Fiber         float64                 `json:"fiber"`
	Sugar         float64                 `json:"sugar"`
	Sodium        float64                 `json:"sodium"`
	Cholesterol   float64                 `json:"cholesterol"`
	Potassium     float64                 `json:"potassium"`
}

func (n nutrition) nutrients() Nutrients {
	return Nutrients{Calories: n.Energy.Value, Carbohydrates: n.Carbohydrates, Protein: n.Protein, Fat: n.Fat,
		SaturatedFat: n.SaturatedFat, Fiber: n.Fiber, Sugar: n.Sugar, Sodium: n.Sodium, Cholesterol: n.Cholesterol, Potassium: n.Potassium}
}

// add adds part to the running total, to a tenth.
func (n *Nutrients) add(part Nutrients) {
	r := func(total *float64, value float64) { *total = math.Round((*total+value)*10) / 10 }
	r(&n.Calories, part.Calories)
	r(&n.Carbohydrates, part.Carbohydrates)
	r(&n.Protein, part.Protein)
	r(&n.Fat, part.Fat)
	r(&n.SaturatedFat, part.SaturatedFat)
	r(&n.Fiber, part.Fiber)
	r(&n.Sugar, part.Sugar)
	r(&n.Sodium, part.Sodium)
	r(&n.Cholesterol, part.Cholesterol)
	r(&n.Potassium, part.Potassium)
}

type diaryItem struct {
	Type         string    `json:"type"`
	MealName     string    `json:"meal_name"`
	MealPosition *int      `json:"meal_position"`
	CreatedAt    string    `json:"created_at"`
	Servings     float64   `json:"servings"`
	Nutrition    nutrition `json:"nutritional_contents"`
	Energy       struct{ Value float64 }
	ServingSize  struct {
		Value float64 `json:"value"`
		Unit  string  `json:"unit"`
	} `json:"serving_size"`
	Food struct {
		Description string `json:"description"`
	} `json:"food"`
}

// parallel runs the jobs at once and returns the first error.
func parallel(jobs ...func() error) error {
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Go(func() { errs[i] = job() })
	}
	wg.Wait()
	// A signed-out answer explains the others.
	for _, err := range errs {
		if errors.Is(err, errSignedOut) {
			return err
		}
	}
	return errors.Join(slices.DeleteFunc(errs, func(err error) bool { return err == nil })...)
}

// fetch reads the day (YYYY-MM-DD).
func (c Client) fetch(ctx context.Context, date string) (Day, error) {
	s := c.session()
	var (
		page  diaryPage
		items []diaryItem
		goals Nutrients
		lbs   []store.Weight
	)
	err := parallel(
		func() (err error) { page, err = s.diaryPage(ctx, date, false); return err },
		func() error {
			var answer struct{ Items []diaryItem }
			err := s.getJSON(ctx, "v2/diary?entry_date="+date+"&types=food_entry,exercise_entry", &answer)
			items = answer.Items
			return err
		},
		func() (err error) { goals, err = s.goals(ctx, date); return err },
		func() (err error) {
			from, _ := time.Parse("2006-01-02", date)
			lbs, err = s.weighIns(ctx, from.AddDate(0, 0, -weightLookback).Format("2006-01-02"))
			return err
		},
	)
	if err != nil {
		return Day{}, err
	}
	return buildDay(date, page, items, goals, lbs), nil
}

func buildDay(date string, page diaryPage, items []diaryItem, goals Nutrients, lbs []store.Weight) Day {
	type meal struct {
		Meal
		id int
	}
	meals := map[string]*meal{}
	for i, name := range page.meals {
		meals[normalizeMeal(name)] = &meal{Meal: Meal{Name: name, Entries: []Entry{}}, id: i}
	}
	day := Day{Date: date, Goals: goals}
	position := func(item diaryItem) int {
		if item.MealPosition == nil {
			return 9
		}
		return *item.MealPosition
	}
	slices.SortStableFunc(items, func(a, b diaryItem) int {
		if pa, pb := position(a), position(b); pa != pb {
			return pa - pb
		}
		return strings.Compare(a.CreatedAt, b.CreatedAt)
	})
	exercise := 0.0
	for _, item := range items {
		if item.Type == "exercise_entry" {
			exercise += item.Energy.Value
			continue
		}
		n := item.Nutrition.nutrients()
		servings := item.Servings
		if servings == 0 {
			servings = 1
		}
		size := item.ServingSize.Value
		if size == 0 {
			size = 1
		}
		name := item.MealName
		if name == "" {
			name = "Other"
		}
		key := normalizeMeal(name)
		m := meals[key]
		if m == nil {
			id := 1000
			if item.MealPosition != nil {
				id = *item.MealPosition
			}
			m = &meal{Meal: Meal{Name: name, Entries: []Entry{}}, id: id}
			meals[key] = m
		}
		food := item.Food.Description
		if food == "" {
			food = "Food"
		}
		m.Entries = append(m.Entries, Entry{
			Name:    food,
			Amount:  strings.TrimSpace(formatG(servings*size) + " " + item.ServingSize.Unit),
			Totals:  n,
			EntryID: page.match(name, item.Food.Description, n.Calories),
		})
		m.Totals.add(n)
		day.Totals.add(n)
	}
	sorted := make([]*meal, 0, len(meals))
	for _, m := range meals {
		sorted = append(sorted, m)
	}
	slices.SortFunc(sorted, func(a, b *meal) int { return a.id - b.id })
	for _, m := range sorted {
		day.Meals = append(day.Meals, m.Meal)
	}
	day.ExerciseCalories = math.Round(exercise)
	day.Weights = lbs
	return day
}

// diaryPage reads the diary page, or reuses the one read a moment ago when only its meal ids and
// form token are needed.
func (s *session) diaryPage(ctx context.Context, date string, reuse bool) (diaryPage, error) {
	if reuse {
		s.mu.Lock()
		page, known := s.pages[date]
		s.mu.Unlock()
		if known && time.Since(page.read) < pageReuse {
			return page, nil
		}
	}
	body, err := s.getPage(ctx, "food/diary?date="+date)
	if err != nil {
		return diaryPage{}, err
	}
	page, err := parseDiary(body)
	if err != nil {
		return page, err
	}
	s.mu.Lock()
	if s.pages == nil {
		s.pages = map[string]diaryPage{}
	}
	s.pages[date] = page
	s.mu.Unlock()
	return page, nil
}

// forgetPage drops a reused diary page that MyFitnessPal turned down.
func (s *session) forgetPage(date string) {
	s.mu.Lock()
	delete(s.pages, date)
	s.mu.Unlock()
}

func (s *session) goals(ctx context.Context, date string) (Nutrients, error) {
	var answer struct {
		Items []struct {
			DefaultGoal nutrition `json:"default_goal"`
		}
	}
	if err := s.getJSON(ctx, "v2/nutrient-goals?date="+date, &answer); err != nil {
		return Nutrients{}, err
	}
	if len(answer.Items) == 0 {
		return Nutrients{}, problem.Failedf("MyFitnessPal returned no nutrient goals")
	}
	return answer.Items[0].DefaultGoal.nutrients(), nil
}

// weighIns are the weigh-ins since from, oldest first, in pounds.
func (s *session) weighIns(ctx context.Context, from string) ([]store.Weight, error) {
	var answer struct {
		Items []struct {
			Date  string  `json:"date"`
			Value float64 `json:"value"`
			Unit  string  `json:"unit"`
		}
	}
	if err := s.getJSON(ctx, "v2/measurements?type=weight&from="+from+"&to="+time.Now().Format("2006-01-02"), &answer); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var weights []store.Weight
	for _, item := range answer.Items { // newest first: the latest of a day wins
		if seen[item.Date] {
			continue
		}
		seen[item.Date] = true
		lb := item.Value
		if strings.HasPrefix(item.Unit, "kilogram") {
			lb *= 2.20462
		}
		weights = append(weights, store.Weight{Date: item.Date, LB: math.Round(lb*10) / 10})
	}
	slices.SortFunc(weights, func(a, b store.Weight) int { return strings.Compare(a.Date, b.Date) })
	return weights, nil
}

// ---------- searching ----------

func (c Client) search(ctx context.Context, query string) ([]Food, error) {
	s := c.session()
	page, err := s.getPage(ctx, "food/search?search="+queryEscape(query)+"&page=1")
	if err != nil {
		return nil, err
	}
	results, err := parseSearch(page)
	if err != nil {
		return nil, err
	}
	results = results[:min(len(results), 10)]
	foods := make([]Food, len(results))
	var wg sync.WaitGroup
	for i, result := range results {
		foods[i] = Food{FoodID: result.foodID, Name: result.name, Brand: result.brand, DefaultWeightID: result.weightIDs[0],
			Nutrition: FoodNutrition{Calories: result.calories}}
		wg.Go(func() { s.foodDetails(ctx, &foods[i], result) })
	}
	wg.Wait()
	return foods, nil
}

// foodDetails adds a search result's macros and serving sizes, which only the API has.
func (s *session) foodDetails(ctx context.Context, food *Food, result searchResult) {
	var answer struct {
		Item struct {
			Verified     bool      `json:"verified"`
			Nutrition    nutrition `json:"nutritional_contents"`
			ServingSizes []struct {
				Value      *float64 `json:"value"`
				Unit       string   `json:"unit"`
				Multiplier *float64 `json:"nutrition_multiplier"`
			} `json:"serving_sizes"`
		}
	}
	if result.externalID != "" {
		query := "?fields%5B%5D=nutritional_contents&fields%5B%5D=serving_sizes&fields%5B%5D=confirmations"
		if s.getJSON(ctx, "v2/foods/"+url.PathEscape(result.externalID)+query, &answer) == nil &&
			len(answer.Item.ServingSizes) == len(result.weightIDs) {
			food.Verified = answer.Item.Verified
			n := answer.Item.Nutrition
			food.Nutrition = FoodNutrition{Calories: n.Energy.Value, Protein: n.Protein, Carbs: n.Carbohydrates, Fat: n.Fat}
			for i, size := range answer.Item.ServingSizes {
				label := strings.TrimSpace(strings.TrimSpace(formatPointer(size.Value)) + " " + size.Unit)
				if size.Multiplier == nil || label == "" {
					continue
				}
				food.Servings = append(food.Servings, Serving{WeightID: result.weightIDs[i], Label: label, Multiplier: *size.Multiplier})
			}
		}
	}
	if len(food.Servings) == 0 {
		label := result.serving
		if label == "" {
			label = "default serving"
		}
		food.Nutrition = FoodNutrition{Calories: result.calories}
		food.Servings = []Serving{{WeightID: result.weightIDs[0], Label: label, Multiplier: 1}}
	}
}

// ---------- changing the diary ----------

// entryForm reads an entry's edit form, keeping its token for the edit that follows.
func (c Client) entryForm(ctx context.Context, entryID string) (EntryForm, error) {
	s := c.session()
	page, err := s.getPage(ctx, "food/edit_entry/"+entryID)
	if err != nil {
		return EntryForm{}, err
	}
	form, token, err := parseEntryForm(page, entryID)
	if err == nil {
		s.mu.Lock()
		s.formToken = token
		s.mu.Unlock()
	}
	return form, err
}

func (c Client) addFood(ctx context.Context, date, foodID, weightID string, quantity float64, meal string) error {
	s := c.session()
	return s.withPage(ctx, date, func(page diaryPage) (int, error) {
		mealID, err := page.mealID(meal)
		if err != nil {
			return 0, err
		}
		resp, err := s.do(ctx, request{method: http.MethodPost, url: siteURL + "food/add", api: true, form: url.Values{
			"food_entry[food_id]":   {foodID},
			"food_entry[date]":      {date},
			"food_entry[quantity]":  {formatG(quantity)},
			"food_entry[weight_id]": {weightID},
			"food_entry[meal_id]":   {mealID},
			"ajax":                  {"true"},
		}, headers: map[string]string{"X-CSRF-Token": page.csrf, "Origin": strings.TrimSuffix(siteURL, "/"),
			"Referer": siteURL + "food/search", "Accept": "application/json"}})
		return resp.status, err
	}, "/food/add", http.StatusOK, http.StatusNoContent)
}

// editFood posts the edit form with the token from opening it, or reads the form first.
func (c Client) editFood(ctx context.Context, date, entryID, weightID string, quantity float64, mealID string) error {
	s := c.session()
	s.mu.Lock()
	token := s.formToken
	s.mu.Unlock()
	for _, reuse := range []bool{token != "", false} {
		if !reuse {
			if _, err := c.entryForm(ctx, entryID); err != nil {
				return err
			}
			s.mu.Lock()
			token = s.formToken
			s.mu.Unlock()
		}
		resp, err := s.do(ctx, request{method: http.MethodPost, url: siteURL + "food/edit_entry/" + entryID, form: url.Values{
			"authenticity_token":    {token},
			"food_entry[id]":        {entryID},
			"food_entry[date]":      {date},
			"food_entry[quantity]":  {formatG(quantity)},
			"food_entry[weight_id]": {weightID},
			"food_entry[meal_id]":   {mealID},
		}, headers: legacyFormHeaders(siteURL + "food/diary?date=" + date)})
		if reuse && tokenRefused(resp.status, err) {
			continue
		}
		if err != nil {
			return err
		}
		return accepted(resp.status, "/food/edit_entry", http.StatusOK, http.StatusNoContent, http.StatusFound)
	}
	return nil
}

func (c Client) deleteFood(ctx context.Context, date, entryID string) error {
	s := c.session()
	return s.withPage(ctx, date, func(page diaryPage) (int, error) {
		resp, err := s.do(ctx, request{method: http.MethodPost, url: siteURL + "food/remove/" + entryID,
			form: url.Values{"_method": {"delete"}, "authenticity_token": {page.csrf}}, headers: legacyFormHeaders(siteURL + "food/diary")})
		return resp.status, err
	}, "/food/remove", http.StatusOK, http.StatusNoContent, http.StatusFound)
}

// withPage posts a diary form with the diary page's token, reusing the page a sync just read.
// When MyFitnessPal turns a reused token down (so nothing changed), it reads the page again and
// tries once more.
func (s *session) withPage(ctx context.Context, date string, post func(diaryPage) (int, error), what string, ok ...int) error {
	for _, reuse := range []bool{true, false} {
		page, err := s.diaryPage(ctx, date, reuse)
		if err != nil {
			return err
		}
		status, err := post(page)
		if reuse && tokenRefused(status, err) {
			s.forgetPage(date)
			continue
		}
		if err != nil {
			return err
		}
		return accepted(status, what, ok...)
	}
	return nil
}

// tokenRefused means a form was turned down for its token (Rails answers 422), not done.
func tokenRefused(status int, err error) bool {
	return errors.Is(err, errSignedOut) || err == nil && status == http.StatusUnprocessableEntity
}

// logWeight sends a weigh-in in the account's own unit (pounds for the dashboard's users).
func (c Client) logWeight(ctx context.Context, date string, lb float64) error {
	resp, err := c.session().do(ctx, request{method: http.MethodPost, url: apiURL + "v2/measurements", api: true,
		json:    map[string]any{"items": []map[string]any{{"type": "Weight", "value": lb, "date": date}}},
		headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return err
	}
	return accepted(resp.status, "/v2/measurements", http.StatusOK, http.StatusCreated)
}

func accepted(status int, what string, ok ...int) error {
	if slices.Contains(ok, status) {
		return nil
	}
	return problem.Failedf(fmt.Sprintf("MyFitnessPal %s returned HTTP %d", what, status))
}

func legacyFormHeaders(referer string) map[string]string {
	return map[string]string{"Origin": strings.TrimSuffix(siteURL, "/"), "Referer": referer}
}

// ---------- small helpers ----------

// formatG writes a number like Python's "%g": 2, 0.5, 1.33333.
func formatG(x float64) string { return strconv.FormatFloat(x, 'g', 6, 64) }

func formatPointer(x *float64) string {
	if x == nil {
		return ""
	}
	return formatG(*x)
}

// roundHalfEven rounds like Python's round().
func roundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// queryEscape encodes a search like MyFitnessPal's own links: %20 for spaces, "/" left as is.
func queryEscape(query string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(query), "+", "%20"), "%2F", "/")
}

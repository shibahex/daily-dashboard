package myfitnesspal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"dashboard/internal/problem"
	"dashboard/internal/text"
)

// Saved meals are MyFitnessPal's own "My Meals", so the phone app has them too. A saved meal logs
// like a food: the classic My Meals tab lists each one with its meal id as the food id and a
// "1 meal" serving, which /food/add takes.

// SavedMeals lists the user's saved meals as foods (SavedMeal set), kept for a few minutes.
func (s *Syncer) SavedMeals() ([]Food, error) {
	return s.meals.get(func() (meals []Food, err error) {
		err = s.client.call(lookupTimeout, func(ctx context.Context) (err error) { meals, err = s.client.savedMeals(ctx, today()); return err })
		return meals, err
	})
}

// SaveMeal saves what's logged in a diary meal on date as a saved meal called name.
func (s *Syncer) SaveMeal(date, meal, name string) error {
	name = text.Collapse(name)
	if name == "" || len([]rune(name)) > 100 {
		return problem.Invalidf("Give the meal a name (up to 100 characters)")
	}
	if !validDate(date) {
		return problem.Invalidf("bad day")
	}
	err := s.client.call(writeTimeout, func(ctx context.Context) error { return s.client.saveMeal(ctx, date, meal, name) })
	s.meals.forget()
	if err != nil {
		return problem.Unavailablef(err.Error())
	}
	return nil
}

func (c Client) savedMeals(ctx context.Context, date string) ([]Food, error) {
	s := c.session()
	var (
		page    []byte
		details []struct {
			ID          json.Number `json:"meal_id"`
			Description string      `json:"description"`
			Foods       []struct {
				Description                   string
				Calories, Protein, Carbs, Fat float64
			} `json:"foods"`
		}
	)
	err := parallel(
		func() (err error) { page, err = s.getPage(ctx, "food/load_meals?date="+date+"&meal=0"); return err },
		func() error {
			// Only for each meal's calories and foods: the meals are listed without them if this fails.
			s.getSiteJSON(ctx, "api/services/users/meals/mine?limit=100&search=", &details)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	meals, err := parseLoadMeals(page)
	if err != nil {
		return nil, err
	}
	for i := range meals {
		for _, d := range details {
			if d.ID.String() != meals[i].FoodID {
				continue
			}
			for _, food := range d.Foods {
				meals[i].Nutrition = FoodNutrition{Calories: meals[i].Nutrition.Calories + food.Calories, Protein: meals[i].Nutrition.Protein + food.Protein,
					Carbs: meals[i].Nutrition.Carbs + food.Carbs, Fat: meals[i].Nutrition.Fat + food.Fat}
				meals[i].Items = append(meals[i].Items, food.Description)
			}
		}
	}
	return meals, nil
}

// parseLoadMeals reads the classic My Meals tab: each row has the meal's id, name and serving.
func parseLoadMeals(page []byte) ([]Food, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return nil, err
	}
	var meals []Food
	for _, row := range find(doc, tag("tr")) {
		var id, weightID, name string
		for _, input := range find(row, tag("input")) {
			if field, _ := attr(input, "name"); strings.HasSuffix(field, "[food_id]") {
				id, _ = attr(input, "value")
			}
		}
		for _, sel := range find(row, tag("select")) {
			if field, _ := attr(sel, "name"); strings.HasSuffix(field, "[weight_id]") {
				if options := find(sel, tag("option")); len(options) > 0 {
					weightID, _ = attr(options[0], "value")
				}
			}
		}
		for _, cell := range childCells(row) {
			if len(find(cell, tag("select"))) == 0 && textOf(cell) != "" {
				name = textOf(cell)
				break
			}
		}
		if !numericID.MatchString(id) || !numericID.MatchString(weightID) || name == "" {
			continue
		}
		meals = append(meals, Food{FoodID: id, Name: name, SavedMeal: true, DefaultWeightID: weightID,
			Servings: []Serving{{WeightID: weightID, Label: "1 meal", Multiplier: 1}}})
	}
	return meals, nil
}

// saveMeal posts the diary's "Remember Meal" form. MyFitnessPal takes the foods from the diary
// meal the form was opened for (the meal and date in its address), so the form is read first.
func (c Client) saveMeal(ctx context.Context, date, meal, name string) error {
	s := c.session()
	page, err := s.diaryPage(ctx, date, true)
	if err != nil {
		return err
	}
	mealID, err := page.mealID(meal)
	if err != nil {
		return err
	}
	formPath := "meal/new?meal=" + mealID + "&date=" + date
	body, err := s.getPage(ctx, formPath)
	if err != nil {
		return err
	}
	token, items, err := parseMealForm(body)
	if err != nil {
		return err
	}
	if items == 0 {
		return problem.Invalidf("Nothing is logged in " + meal + " that day, so there's no meal to save")
	}
	resp, err := s.do(ctx, request{method: http.MethodPost, url: siteURL + "meal/create", form: url.Values{
		"authenticity_token": {token},
		"meal_id":            {mealID},
		"meal[description]":  {name},
	}, headers: legacyFormHeaders(siteURL + formPath)})
	if err != nil {
		return err
	}
	return accepted(resp.status, "/meal/create", http.StatusOK, http.StatusFound)
}

// parseMealForm reads the Remember Meal form's token and how many foods it would save.
func parseMealForm(page []byte) (token string, items int, err error) {
	doc, err := parseHTML(page)
	if err != nil {
		return "", 0, err
	}
	for _, form := range find(doc, tag("form")) {
		if action, _ := attr(form, "action"); action != "/meal/create" {
			continue
		}
		for _, input := range find(form, named("input", "authenticity_token")) {
			token, _ = attr(input, "value")
		}
	}
	if token == "" {
		return "", 0, problem.Failedf("Couldn't open MyFitnessPal's save-meal form")
	}
	for _, table := range find(doc, func(n *html.Node) bool { id, _ := attr(n, "id"); return n.Data == "table" && id == "meal-table" }) {
		for _, body := range find(table, tag("tbody")) {
			for _, row := range find(body, tag("tr")) {
				if len(childCells(row)) > 0 {
					items++
				}
			}
		}
	}
	return token, items, nil
}

package myfitnesspal

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"dashboard/internal/problem"
	"dashboard/internal/store"
	"dashboard/internal/text"
)

// Nutrients are amounts in grams, except calories, and cholesterol, sodium and potassium (mg).
// The same shape holds a day's totals and its goals.
type Nutrients struct {
	Calories      float64 `json:"calories"`
	Carbohydrates float64 `json:"carbohydrates"`
	Protein       float64 `json:"protein"`
	Fat           float64 `json:"fat"`
	SaturatedFat  float64 `json:"saturated_fat"`
	Fiber         float64 `json:"fiber"`
	Sugar         float64 `json:"sugar"`
	Sodium        float64 `json:"sodium"`
	Cholesterol   float64 `json:"cholesterol"`
	Potassium     float64 `json:"potassium"`
}

// Entry is one food in the diary. EntryID is empty when it couldn't be matched to MyFitnessPal's
// classic diary (then it can't be edited from here).
type Entry struct {
	Name    string    `json:"name"`
	Amount  string    `json:"amount"`
	Totals  Nutrients `json:"totals"`
	EntryID string    `json:"entry_id"`
}

type Meal struct {
	Name    string    `json:"name"`
	Totals  Nutrients `json:"totals"`
	Entries []Entry   `json:"entries"`
}

// Day is one synced diary day.
type Day struct {
	Date             string         `json:"day"`
	Totals           Nutrients      `json:"totals"`
	Goals            Nutrients      `json:"goals"`
	Meals            []Meal         `json:"meals"`
	ExerciseCalories float64        `json:"exercise_calories"`
	Weights          []store.Weight `json:"weights"`
}

// Day decodes the cached diary, if there is one.
func (c Cache) Day() (Day, bool) {
	if c.Data == nil {
		return Day{}, false
	}
	var day Day
	return day, json.Unmarshal(c.Data, &day) == nil
}

// Age is how long ago the cache was synced.
func (c Cache) Age() time.Duration {
	return time.Since(time.Unix(int64(c.FetchedAt), 0))
}

// ErrorText is the last sync's error, if it failed.
func (c Cache) ErrorText() string {
	if c.Error == nil {
		return ""
	}
	return *c.Error
}

// ---------- searching ----------

// Serving is one serving size of a food. Multiplier scales the food's nutrition (which is for
// the serving with multiplier 1).
type Serving struct {
	WeightID   string  `json:"weight_id"`
	Label      string  `json:"label"`
	Multiplier float64 `json:"nutrition_multiplier"`
}

type FoodNutrition struct {
	Calories float64 `json:"calories"`
	Protein  float64 `json:"protein"`
	Carbs    float64 `json:"carbs"`
	Fat      float64 `json:"fat"`
}

// Scaled is the nutrition for this many of a serving.
func (n FoodNutrition) Scaled(factor float64) FoodNutrition {
	return FoodNutrition{Calories: n.Calories * factor, Protein: n.Protein * factor, Carbs: n.Carbs * factor, Fat: n.Fat * factor}
}

// Food is a search result.
type Food struct {
	FoodID          string        `json:"food_id"`
	Name            string        `json:"name"`
	Brand           string        `json:"brand"`
	Verified        bool          `json:"verified"`
	Nutrition       FoodNutrition `json:"nutrition"`
	DefaultWeightID string        `json:"default_weight_id"`
	Servings        []Serving     `json:"servings"`
}

// ServingOptions are the food's servings, or one plain serving when it has none listed.
func (f Food) ServingOptions() []Serving {
	if len(f.Servings) > 0 {
		return f.Servings
	}
	return []Serving{{WeightID: f.DefaultWeightID, Label: "1 serving", Multiplier: 1}}
}

// A lookup or change normally takes a second; the rest is for renewing the login.
const (
	lookupTimeout = 90 * time.Second
	writeTimeout  = 90 * time.Second
)

// Search looks a food up in MyFitnessPal's database.
func (c Client) Search(query string) ([]Food, error) {
	query, ok := SearchQuery(query)
	if !ok {
		return nil, problem.Invalidf("type what to search for")
	}
	var foods []Food
	err := c.call(lookupTimeout, func(ctx context.Context) (err error) { foods, err = c.search(ctx, query); return err })
	if err != nil {
		return nil, problem.Unavailablef(err.Error())
	}
	return foods, nil
}

// ---------- editing an entry ----------

// Option is one choice in an entry's edit form.
type Option struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

// EntryForm is MyFitnessPal's own edit form for an entry: amount, serving size and meal.
type EntryForm struct {
	EntryID  string   `json:"entry_id"`
	Name     string   `json:"name"`
	Quantity float64  `json:"quantity"`
	Servings []Option `json:"servings"`
	Meals    []Option `json:"meals"`
}

var (
	numericID    = regexp.MustCompile(`^\d{1,20}$`)
	mealPosition = regexp.MustCompile(`^\d{1,2}$`)
)

func (c Client) EntryForm(entryID string) (EntryForm, error) {
	if !numericID.MatchString(entryID) {
		return EntryForm{}, problem.Invalidf("bad entry")
	}
	var form EntryForm
	err := c.call(lookupTimeout, func(ctx context.Context) (err error) { form, err = c.entryForm(ctx, entryID); return err })
	if err != nil {
		return EntryForm{}, problem.Unavailablef(err.Error())
	}
	return form, nil
}

// ---------- changing the diary ----------

// AddFood logs quantity servings of a food to a meal on date (YYYY-MM-DD).
func (s *Syncer) AddFood(date, foodID, weightID string, quantity float64, meal string) (Cache, error) {
	meal = text.Collapse(meal)
	if err := checkIDs(foodID, weightID); err != nil {
		return Cache{}, err
	}
	if err := checkQuantity(quantity); err != nil {
		return Cache{}, err
	}
	if meal == "" || len([]rune(meal)) > 40 {
		return Cache{}, problem.Invalidf("pick a meal")
	}
	return s.change(date, func(ctx context.Context) error { return s.client.addFood(ctx, date, foodID, weightID, quantity, meal) })
}

// EditFood changes an entry's amount, serving size or meal (mealID is the meal's position).
func (s *Syncer) EditFood(date, entryID, weightID string, quantity float64, mealID string) (Cache, error) {
	if err := checkIDs(entryID, weightID); err != nil {
		return Cache{}, err
	}
	if err := checkQuantity(quantity); err != nil {
		return Cache{}, err
	}
	if !mealPosition.MatchString(mealID) {
		return Cache{}, problem.Invalidf("pick a meal")
	}
	return s.change(date, func(ctx context.Context) error {
		return s.client.editFood(ctx, date, entryID, weightID, quantity, mealID)
	})
}

func (s *Syncer) DeleteFood(date, entryID string) (Cache, error) {
	if err := checkIDs(entryID); err != nil {
		return Cache{}, err
	}
	return s.change(date, func(ctx context.Context) error { return s.client.deleteFood(ctx, date, entryID) })
}

// LogWeight sends a weigh-in (lb) to MyFitnessPal.
func (s *Syncer) LogWeight(day string, lb float64) error {
	_, err := s.change(day, func(ctx context.Context) error { return s.client.logWeight(ctx, day, lb) })
	return err
}

func (s *Syncer) change(date string, op func(ctx context.Context) error) (Cache, error) {
	if !validDate(date) {
		return Cache{}, problem.Invalidf("bad day")
	}
	cache, err := s.Change(date, op)
	if err != nil {
		return cache, problem.Unavailablef(err.Error())
	}
	return cache, nil
}

func validDate(date string) bool { _, err := time.Parse("2006-01-02", date); return err == nil }

func checkIDs(ids ...string) error {
	for _, id := range ids {
		if !numericID.MatchString(strings.TrimSpace(id)) {
			return problem.Invalidf("bad food or entry")
		}
	}
	return nil
}

func checkQuantity(quantity float64) error {
	if quantity <= 0 || quantity > 1000 {
		return problem.Invalidf("amount must be more than 0")
	}
	return nil
}

// MealForNow is the meal a food eaten now most likely belongs to.
func MealForNow(now time.Time) string {
	switch hour := now.Hour(); {
	case hour < 11:
		return "Breakfast"
	case hour < 15:
		return "Lunch"
	case hour < 21:
		return "Dinner"
	}
	return "Snacks"
}

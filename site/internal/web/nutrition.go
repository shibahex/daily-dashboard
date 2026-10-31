package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/myfitnesspal"
	"dashboard/internal/problem"
	"dashboard/internal/store"
)

// The goals card shows today's MyFitnessPal diary when it's synced, and the plan's goals when
// it isn't.

type macroBar struct {
	Label        string
	Eaten, Goal  float64
	Unit, Colour string
}

type calorieShare struct {
	Key, Label string
	Percent    int
}

// limitTile is a nutrient to stay under (sat fat, sodium...) or to reach (fiber, potassium).
type limitTile struct {
	Label       string
	Amount      float64
	Goal        float64
	Unit        string
	AtLeast     bool
	Over, Ample bool // past a limit / reached a minimum
}

type nutritionView struct {
	Day       myfitnesspal.Day
	Syncing   bool
	SyncedAgo string
	LastError string
	Budget    float64 // goal + exercise
	Left      float64
	Macros    []macroBar
	Split     []calorieShare
	Limits    []limitTile
	Meals     []myfitnesspal.Meal
	MealNow   string // where "+ Log food" adds to
	DayName   string // "today", "yesterday", "Mon, Oct 19"
}

type goalsCardView struct {
	Editing   bool
	Goals     []store.Goal
	Editor    rowEditor
	Nutrition *nutritionView
	Syncing   bool
	LastError string
	DayName   string // "today", "yesterday", "Mon, Oct 19"
}

const goalsURL = "/cards/goals"

func (ui *UI) renderGoalsCard(r *http.Request) (template.HTML, error) {
	view, err := ui.goalsView(r, isEditing(r), nil)
	if err != nil {
		return "", err
	}
	return ui.renderToHTML("card-goals", view)
}

func (ui *UI) goalsView(r *http.Request, editing bool, edited [][]string) (goalsCardView, error) {
	plan, err := ui.ws(r).Store.LoadPlan()
	if err != nil {
		return goalsCardView{}, err
	}
	shown := shownDay(r)
	cache, syncing := ui.ws(r).MyFitnessPal.Day(shown.Format(dayLayout))
	view := goalsCardView{Editing: editing, Goals: plan.Goals, Syncing: syncing, LastError: cache.ErrorText(), DayName: dayName(shown)}
	if day, synced := cache.Day(); synced && !editing {
		view.Nutrition = nutritionFrom(day, cache, syncing)
		view.Nutrition.DayName = view.DayName
	}
	if edited == nil {
		for _, goal := range plan.Goals {
			edited = append(edited, []string{goal.Label, goal.Value, goal.Unit})
		}
	}
	view.Editor = rowEditor{URL: goalsURL, Rows: edited, AddLabel: "Add goal",
		Columns: []rowColumn{{Placeholder: "Nutrient"}, {Placeholder: "Value", Narrow: true}, {Placeholder: "Unit", Narrow: true}}}
	return view, nil
}

func nutritionFrom(day myfitnesspal.Day, cache myfitnesspal.Cache, syncing bool) *nutritionView {
	eaten, goals := day.Totals, day.Goals
	view := &nutritionView{
		Day: day, Syncing: syncing, SyncedAgo: ago(cache.Age()), LastError: cache.ErrorText(),
		Budget:  goals.Calories + day.ExerciseCalories,
		Meals:   day.Meals,
		MealNow: myfitnesspal.MealForNow(time.Now()),
		Macros: []macroBar{
			{"Protein", eaten.Protein, goals.Protein, "g", "protein"},
			{"Carbs", eaten.Carbohydrates, goals.Carbohydrates, "g", "carbs"},
			{"Fat", eaten.Fat, goals.Fat, "g", "fat"},
		},
		Limits: []limitTile{
			limit("Sat fat", eaten.SaturatedFat, goals.SaturatedFat, "g", false),
			limit("Fiber", eaten.Fiber, goals.Fiber, "g", true),
			limit("Sodium", eaten.Sodium, goals.Sodium, "mg", false),
			limit("Cholesterol", eaten.Cholesterol, goals.Cholesterol, "mg", false),
			limit("Sugar", eaten.Sugar, goals.Sugar, "g", false),
			limit("Potassium", eaten.Potassium, goals.Potassium, "mg", true),
		},
	}
	view.Left = view.Budget - eaten.Calories
	// Where the calories came from: 4 kcal per gram of protein or carbs, 9 per gram of fat.
	fromMacro := map[string]float64{"protein": eaten.Protein * 4, "carbs": eaten.Carbohydrates * 4, "fat": eaten.Fat * 9}
	total := fromMacro["protein"] + fromMacro["carbs"] + fromMacro["fat"]
	if total > 0 {
		for _, macro := range []struct{ key, label string }{{"protein", "Protein"}, {"carbs", "Carbs"}, {"fat", "Fat"}} {
			view.Split = append(view.Split, calorieShare{macro.key, macro.label, int(math.Round(fromMacro[macro.key] / total * 100))})
		}
	}
	return view
}

func limit(label string, amount, goal float64, unit string, atLeast bool) limitTile {
	tile := limitTile{Label: label, Amount: amount, Goal: goal, Unit: unit, AtLeast: atLeast}
	if goal > 0 {
		tile.Over = !atLeast && amount > goal
		tile.Ample = atLeast && amount >= goal
	}
	return tile
}

// ago is "just now", "4 min ago" or "2 h ago".
func ago(d time.Duration) string {
	minutes := int(math.Round(d.Minutes()))
	switch {
	case minutes < 1:
		return "just now"
	case minutes < 60:
		return strconv.Itoa(minutes) + " min ago"
	}
	return strconv.Itoa(int(math.Round(float64(minutes)/60))) + " h ago"
}

func (ui *UI) goalsCard(w http.ResponseWriter, r *http.Request) {
	ui.renderGoalsWith(r, w, isEditing(r), nil)
}

func (ui *UI) renderGoalsWith(r *http.Request, w http.ResponseWriter, editing bool, edited [][]string) {
	view, err := ui.goalsView(r, editing, edited)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "card-goals", view)
}

// saveGoals saves the plan's goals (label, value, unit), or applies a row operation.
func (ui *UI) saveGoals(w http.ResponseWriter, r *http.Request) {
	rows := rowsFromForm(r, 3)
	if operation := rowOperation(r); operation != "" {
		ui.renderGoalsWith(r, w, true, applyRowOperation(rows, operation, 3))
		return
	}
	err := ui.ws(r).Store.UpdatePlan(func(plan *store.Plan) {
		plan.Goals = nil
		for _, row := range cleanRows(rows) {
			plan.Goals = append(plan.Goals, store.Goal{Label: row[0], Value: row[1], Unit: row[2]})
		}
	})
	if err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Goals saved"), planChanged)
	ui.renderGoalsWith(r, w, false, nil)
}

// syncNutrition syncs the shown day with MyFitnessPal now.
func (ui *UI) syncNutrition(w http.ResponseWriter, r *http.Request) {
	cache := ui.ws(r).MyFitnessPal.FetchDay(shownDay(r).Format(dayLayout))
	var message *notice
	if cache.ErrorText() != "" {
		message = &notice{Message: "MyFitnessPal: " + cache.ErrorText(), Bad: true}
	}
	announce(w, message, checklistChanged)
	ui.renderGoalsWith(r, w, false, nil)
}

// ---------- logging foods ----------
// The food panel opens inside the card: one list, the foods logged recently in MyFitnessPal or
// the saved meals (narrowed while typing), or what a MyFitnessPal search found; then pick one,
// choose servings, add.

type foodSearchView struct {
	Meal     string
	Query    string
	Searched bool // Results are a search's (else the list loads the recent foods)
	Results  []foodResult
	Error    string
}

type foodResult struct {
	Food     myfitnesspal.Food
	Encoded  string // the food as JSON, carried to the next step
	Serving  string // the first serving's label
	Per      myfitnesspal.FoodNutrition
	Index    int    // the serving to start with
	Quantity string // the amount to start with
}

func foodResultFor(food myfitnesspal.Food, serving int, quantity float64) foodResult {
	encoded, _ := json.Marshal(food)
	options := food.ServingOptions()
	if serving < 0 || serving >= len(options) {
		serving = 0
	}
	if quantity <= 0 {
		quantity = 1
	}
	return foodResult{Food: food, Encoded: string(encoded), Index: serving, Quantity: strconv.FormatFloat(quantity, 'f', -1, 64),
		Serving: options[serving].Label, Per: food.Nutrition.Scaled(options[serving].Multiplier * quantity)}
}

func (ui *UI) foodSearchForm(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "food-search", foodSearchView{Meal: r.URL.Query().Get("meal")})
}

func (ui *UI) searchFood(w http.ResponseWriter, r *http.Request) {
	view := foodSearchView{Meal: r.FormValue("meal"), Query: formText(r, "query")}
	if view.Query == "" { // Back from a food picked from the list
		ui.render(w, "food-search", view)
		return
	}
	view.Searched = true
	foods, err := ui.ws(r).MyFitnessPal.Client().Search(view.Query)
	if err != nil {
		view.Error = problem.Public(err)
	}
	for _, food := range foods {
		view.Results = append(view.Results, foodResultFor(food, 0, 1))
	}
	ui.render(w, "food-search", view)
}

type foodListView struct {
	Meal, Query string
	List        string // "recent" or "meals"
	Foods       []foodResult
	Error       string
}

// foodList is the recent foods or the saved meals matching what's typed so far.
func (ui *UI) foodList(w http.ResponseWriter, r *http.Request) {
	mfp := ui.ws(r).MyFitnessPal
	view := foodListView{Meal: r.FormValue("meal"), Query: formText(r, "query"), List: "recent"}
	words := strings.Fields(strings.ToLower(view.Query))
	var err error
	if r.FormValue("list") == "meals" {
		view.List = "meals"
		var meals []myfitnesspal.Food
		meals, err = mfp.SavedMeals()
		for _, meal := range meals {
			if myfitnesspal.MatchesWords(meal.Name+" "+strings.Join(meal.Items, " "), words) {
				view.Foods = append(view.Foods, foodResultFor(meal, 0, 1))
			}
		}
	} else {
		var recent []myfitnesspal.Recent
		recent, err = mfp.RecentFoods()
		for _, food := range recent {
			if myfitnesspal.MatchesWords(food.Food.Name+" "+food.Food.Brand, words) {
				view.Foods = append(view.Foods, foodResultFor(food.Food, food.Serving, food.Quantity))
			}
		}
	}
	if err != nil {
		view.Error = problem.Public(err)
	}
	ui.render(w, "food-list", view)
}

type mealSaveView struct{ Meal string }

// saveMealForm asks what to call a diary meal saved for later.
func (ui *UI) saveMealForm(w http.ResponseWriter, r *http.Request) {
	ui.render(w, "meal-save", mealSaveView{Meal: r.URL.Query().Get("meal")})
}

func (ui *UI) saveMeal(w http.ResponseWriter, r *http.Request) {
	name := formText(r, "name")
	if err := ui.ws(r).MyFitnessPal.SaveMeal(shownDay(r).Format(dayLayout), r.FormValue("meal"), name); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Saved “"+name+"” in your MyFitnessPal meals"))
	nothing(w)
}

// deleteSavedMeal deletes a saved meal and shows the Meals list again.
func (ui *UI) deleteSavedMeal(w http.ResponseWriter, r *http.Request) {
	if err := ui.ws(r).MyFitnessPal.DeleteSavedMeal(r.PathValue("meal")); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Deleted “"+r.FormValue("name")+"” from your MyFitnessPal meals"))
	ui.foodList(w, r)
}

type foodPickView struct {
	Food     myfitnesspal.Food
	Encoded  string
	Query    string
	Servings []myfitnesspal.Serving
	Serving  int
	Quantity string
	Meal     string
	Meals    []string
	Preview  myfitnesspal.FoodNutrition
}

// pickFood is the second step: how many, which serving, which meal, with a calorie preview.
func (ui *UI) pickFood(w http.ResponseWriter, r *http.Request) {
	view, err := ui.foodPickView(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "food-pick", view)
}

// previewFood updates the preview while the amount or serving changes.
func (ui *UI) previewFood(w http.ResponseWriter, r *http.Request) {
	view, err := ui.foodPickView(r)
	if err != nil {
		fail(w, err)
		return
	}
	ui.render(w, "food-preview", view)
}

func (ui *UI) foodPickView(r *http.Request) (foodPickView, error) {
	var food myfitnesspal.Food
	if json.Unmarshal([]byte(r.FormValue("food")), &food) != nil {
		return foodPickView{}, problem.Invalidf("pick a food")
	}
	view := foodPickView{Food: food, Encoded: r.FormValue("food"), Query: r.FormValue("query"),
		Servings: food.ServingOptions(), Quantity: formText(r, "quantity"), Meal: r.FormValue("meal")}
	if view.Quantity == "" {
		view.Quantity = "1"
	}
	view.Serving, _ = strconv.Atoi(r.FormValue("serving"))
	if view.Serving < 0 || view.Serving >= len(view.Servings) {
		view.Serving = 0
	}
	quantity, _ := strconv.ParseFloat(view.Quantity, 64)
	view.Preview = food.Nutrition.Scaled(view.Servings[view.Serving].Multiplier * quantity)
	cache, _ := ui.ws(r).MyFitnessPal.Day(shownDay(r).Format(dayLayout))
	if day, ok := cache.Day(); ok {
		for _, meal := range day.Meals {
			view.Meals = append(view.Meals, meal.Name)
		}
	}
	return view, nil
}

// addFood logs the picked food and redraws the card.
func (ui *UI) addFood(w http.ResponseWriter, r *http.Request) {
	view, err := ui.foodPickView(r)
	if err != nil {
		fail(w, err)
		return
	}
	quantity, err := strconv.ParseFloat(view.Quantity, 64)
	if err != nil || quantity <= 0 {
		fail(w, problem.Invalidf("Enter how many servings"))
		return
	}
	serving := view.Servings[view.Serving]
	date := shownDay(r).Format(dayLayout)
	mfp := ui.ws(r).MyFitnessPal
	if _, err := mfp.AddFood(date, view.Food.FoodID, serving.WeightID, quantity, view.Meal); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Added to "+view.Meal))
	ui.renderGoalsWith(r, w, false, nil)
}

type foodEditView struct {
	Form  myfitnesspal.EntryForm
	Error string
}

// editFoodForm opens MyFitnessPal's own edit form for an entry: amount, serving, meal.
func (ui *UI) editFoodForm(w http.ResponseWriter, r *http.Request) {
	form, err := ui.ws(r).MyFitnessPal.Client().EntryForm(r.PathValue("entry"))
	view := foodEditView{Form: form}
	if err != nil {
		view.Error = problem.Public(err)
	}
	ui.render(w, "food-edit", view)
}

func (ui *UI) saveFood(w http.ResponseWriter, r *http.Request) {
	quantity, err := strconv.ParseFloat(formText(r, "quantity"), 64)
	if err != nil || quantity <= 0 {
		fail(w, problem.Invalidf("Enter how many servings"))
		return
	}
	if _, err := ui.ws(r).MyFitnessPal.EditFood(shownDay(r).Format(dayLayout), r.PathValue("entry"), r.FormValue("serving"), quantity, r.FormValue("meal")); err != nil {
		fail(w, err)
		return
	}
	announce(w, say("Updated in MyFitnessPal"))
	ui.renderGoalsWith(r, w, false, nil)
}

func (ui *UI) deleteFood(w http.ResponseWriter, r *http.Request) {
	if _, err := ui.ws(r).MyFitnessPal.DeleteFood(shownDay(r).Format(dayLayout), r.PathValue("entry")); err != nil {
		if !errors.Is(err, myfitnesspal.ErrEntryGone) {
			fail(w, err)
			return
		}
		announce(w, &notice{Message: problem.Public(err), Bad: true})
		ui.renderGoalsWith(r, w, false, nil)
		return
	}
	announce(w, say("Removed from MyFitnessPal"))
	ui.renderGoalsWith(r, w, false, nil)
}

// closePanel empties a panel's slot (Cancel).
func (ui *UI) closePanel(w http.ResponseWriter, r *http.Request) { nothing(w) }

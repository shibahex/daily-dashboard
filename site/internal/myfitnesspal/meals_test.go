package myfitnesspal

import (
	"testing"
	"time"
)

func TestUsualFoodsByFrequencyOrRecency(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)
	at := func(daysAgo int) int64 { return now.AddDate(0, 0, -daysAgo).Unix() }
	bread := Food{FoodID: "1", Name: "Whole wheat bread", Brand: "Example Bakery"}
	turkey := Food{FoodID: "2", Name: "Sliced turkey"}
	oats := Food{FoodID: "3", Name: "Rolled oats"}
	logs := []Logged{
		{Food: bread, At: at(3), Quantity: 2},
		{Food: bread, At: at(2), Quantity: 2},
		{Food: turkey, At: at(1), Quantity: 1, Serving: 1},
		{Food: oats, At: at(60)},
		{Food: oats, At: at(61)},
		{Food: oats, At: at(62)},
	}
	frequent := usual(logs, "", "frequent", now)
	if len(frequent) != 3 || frequent[0].Food.FoodID != "1" || frequent[0].Times != 2 || frequent[2].Food.FoodID != "3" || frequent[2].Times != 0 {
		t.Fatalf("frequent: %+v", frequent)
	}
	recent := usual(logs, "", "recent", now)
	if recent[0].Food.FoodID != "2" || recent[0].Serving != 1 {
		t.Fatalf("recent: %+v", recent)
	}
	if found := usual(logs, "BAKERY wheat", "frequent", now); len(found) != 1 || found[0].Food.FoodID != "1" {
		t.Fatalf("search: %+v", found)
	}
}

func TestParseLoadMeals(t *testing.T) {
	page := `<div class="meals_page"><table class="table0"><tbody>
    <tr class="favorite">
      <td><input name="favorites[0][food_id]" type="hidden" value="1111" /><input class="checkbox" name="favorites[0][checked]" type="checkbox" value="1" /></td>
      <td>Soup Dinner</td>
      <td><input class="text" name="favorites[0][quantity]" type="text" value="1" />
        <select name="favorites[0][weight_id]"><option value="9001">1 meal</option></select></td>
    </tr>
    <tr class="favorite">
      <td><input name="favorites[1][food_id]" type="hidden" value="2222" /></td>
      <td>Taco Night</td>
      <td><select name="favorites[1][weight_id]"><option value="9002">1 meal</option></select></td>
    </tr></tbody></table></div>`
	meals, err := parseLoadMeals([]byte(page))
	if err != nil || len(meals) != 2 {
		t.Fatalf("meals %+v, %v", meals, err)
	}
	if m := meals[1]; m.FoodID != "2222" || m.Name != "Taco Night" || !m.SavedMeal || m.ServingOptions()[0].WeightID != "9002" {
		t.Fatalf("meal %+v", m)
	}
}

func TestParseMealForm(t *testing.T) {
	page := `<html><body><form action="/meal/create" method="post">
  <input type="hidden" name="authenticity_token" value="meal-form-token">
  <table id="meal-table"><thead><tr><td>Items in this meal</td></tr></thead>
    <tbody><tr><td>Canned Chicken, 12 oz</td><td>280</td></tr><tr><td>Noodle Soup</td><td>150</td></tr></tbody></table>
</form></body></html>`
	token, items, err := parseMealForm([]byte(page))
	if err != nil || token != "meal-form-token" || items != 2 {
		t.Fatalf("token %q items %d err %v", token, items, err)
	}
	if _, _, err := parseMealForm([]byte(`<html><body>Sign in</body></html>`)); err == nil {
		t.Fatal("a page without the form should fail")
	}
}

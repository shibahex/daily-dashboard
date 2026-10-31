package myfitnesspal

import "testing"

func TestParseLoadRecent(t *testing.T) {
	page := `<div id="recent_page_1" class="recent_page"><table class="table0"><tbody>
  <tr class="favorite">
    <td><input id="favorites_0_food_id" name="favorites[0][food_id]" type="hidden" value="1234" />
      <input name="favorites[0][checked]" type="hidden" value="0" />
      <input class="checkbox" name="favorites[0][checked]" type="checkbox" value="1" data-food-id="1234"
        data-food-external-id="aaaa-bbbb" data-food-verified="true" data-list-type="recent" /></td>
    <td>Example Bakery - Whole Wheat Bread</td>
    <td><strong>Qty:</strong><input class="text" name="favorites[0][quantity]" type="text" value="2" /><label>of</label>
      <select name="favorites[0][weight_id]">
        <option value="501">1 slice</option>
        <option value="502" selected=&quot;selected&quot;>1 g</option>
      </select></td>
  </tr>
  <tr class="favorite">
    <td><input name="favorites[1][food_id]" type="hidden" value="5678" />
      <input class="checkbox" type="checkbox" data-food-external-id="cccc" data-food-verified="false" /></td>
    <td>Rolled oats</td>
    <td><input name="favorites[1][quantity]" value="0.5" />
      <select name="favorites[1][weight_id]"><option value="601" selected=&quot;selected&quot;>1 cup</option></select></td>
  </tr></tbody></table></div>`
	rows, err := parseLoadRecent([]byte(page))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	if r := rows[0]; r.foodID != "1234" || r.externalID != "aaaa-bbbb" || !r.verified || r.brand != "Example Bakery" || r.name != "Whole Wheat Bread" ||
		r.quantity != 2 || r.weightID != "502" || r.serving != "1 g" || len(r.weightIDs) != 2 {
		t.Fatalf("first %+v", r)
	}
	if r := rows[1]; r.brand != "" || r.name != "Rolled oats" || r.quantity != 0.5 || r.weightID != "601" || r.verified {
		t.Fatalf("second %+v", r)
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
  <input type="hidden" name="date" value="2026-10-28"><input type="hidden" name="meal_id" value="2">
  <input type="text" name="meal[description]"><input type="submit" name="commit" value="Save">
  <table id="meal-table"><thead><tr><td>Items in this meal</td></tr></thead>
    <tbody><tr><td>Canned Chicken, 12 oz</td><td>280</td></tr><tr><td>Noodle Soup</td><td>150</td></tr></tbody></table>
</form></body></html>`
	form, items, err := parseMealForm([]byte(page))
	if err != nil || form.Get("authenticity_token") != "meal-form-token" || form.Get("date") != "2026-10-28" ||
		form.Get("meal_id") != "2" || form.Has("commit") || items != 2 {
		t.Fatalf("form %v items %d err %v", form, items, err)
	}
	if _, _, err := parseMealForm([]byte(`<html><body>Sign in</body></html>`)); err == nil {
		t.Fatal("a page without the form should fail")
	}
}

package myfitnesspal

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"dashboard/internal/store"
)

const diaryHTML = `<html><head><meta name="csrf-token" content="tok123"></head><body><table>
<tr class="meal_header"><td class="first alt">Breakfast</td><td>Calories</td></tr>
<tr><td class="first alt"><a data-food-entry-id="11" href="#">Oats, 1 cup</a></td><td>300</td></tr>
<tr class="meal_header"><td class="first alt">Midday</td></tr>
<tr><td><a data-food-entry-id="21" href="#">Eggs, 2 large</a></td><td>140</td></tr>
<tr><td><a data-food-entry-id="22" href="#">Eggs, 3 large</a></td><td>210</td></tr>
<tr class="meal_header"><td class="first alt">Dinner</td></tr>
<tr class="meal_header"><td></td></tr>
</table></body></html>`

func TestParseDiary(t *testing.T) {
	page, err := parseDiary([]byte(diaryHTML))
	if err != nil {
		t.Fatal(err)
	}
	if page.csrf != "tok123" || len(page.meals) != 4 || page.meals[3] != "Meal 4" || len(page.rows) != 3 {
		t.Fatalf("got %+v", page)
	}
	// Calories pick the right copy of a food; the row is used up.
	if id := page.match("Midday", "Eggs", 210); id != "22" {
		t.Errorf("match = %q", id)
	}
	if id := page.match("midday", "Eggs", 999); id != "21" {
		t.Errorf("match = %q", id)
	}
	if id := page.match("Midday", "Eggs", 140); id != "" {
		t.Errorf("a used row matched again: %q", id)
	}
	// "Lunch" is the renamed second meal; "Snacks" (position 3) is a meal named "Meal 4".
	for meal, want := range map[string]string{"breakfast": "0", "Lunch": "1", "DINNER": "2", "Snack": "3"} {
		if got, err := page.mealID(meal); got != want || err != nil {
			t.Errorf("mealID(%q) = %q, %v", meal, got, err)
		}
	}
	if _, err := page.mealID("Brunch"); err == nil {
		t.Error("an unknown meal should fail")
	}
	if _, err := parseDiary([]byte("<html><body>Sign in</body></html>")); !errors.Is(err, errSignedOut) {
		t.Errorf("the sign-in page should mean signed out, got %v", err)
	}
}

func TestBuildDay(t *testing.T) {
	page, _ := parseDiary([]byte(diaryHTML))
	var items []diaryItem
	json.Unmarshal([]byte(`[
		{"type":"food_entry","meal_name":"Midday","meal_position":1,"created_at":"2","servings":1.5,"serving_size":{"value":2,"unit":"large"},
		 "food":{"description":"Eggs"},"nutritional_contents":{"energy":{"value":210},"protein":18.25}},
		{"type":"food_entry","meal_name":"Breakfast","meal_position":0,"created_at":"1","servings":1,"serving_size":{"value":1,"unit":"cup"},
		 "food":{"description":"Oats"},"nutritional_contents":{"energy":{"value":300},"protein":10}},
		{"type":"exercise_entry","energy":{"unit":"calories","value":143.4}},
		{"type":"exercise_entry","energy":{"unit":"calories","value":20}}]`), &items)
	day := buildDay("2026-01-02", page, items, Nutrients{Calories: 2000}, []store.Weight{{Date: "2026-01-01", LB: 180}})
	if day.Totals.Calories != 510 || day.Totals.Protein != 28.3 || day.ExerciseCalories != 163 || day.Goals.Calories != 2000 {
		t.Errorf("totals %+v, exercise %v", day.Totals, day.ExerciseCalories)
	}
	if len(day.Meals) != 4 || day.Meals[0].Name != "Breakfast" || day.Meals[1].Name != "Midday" || len(day.Meals[2].Entries) != 0 {
		t.Fatalf("meals %+v", day.Meals)
	}
	eggs := day.Meals[1].Entries[0]
	if eggs.Amount != "3 large" || eggs.EntryID != "22" || day.Meals[0].Entries[0].EntryID != "11" {
		t.Errorf("entries %+v", day.Meals)
	}
}

func TestParseSearch(t *testing.T) {
	results, err := parseSearch([]byte(`<ul>
		<li><a data-original-id="5" data-external-id="77" data-weight-ids="9,10," href="#">Greek Yogurt</a>
		    <p class="search-nutritional-info">Acme, Plain, 1 cup, 150 calories</p></li>
		<li><a data-original-id="6" data-weight-ids="" href="#">No servings</a></li>
		<li><a data-original-id="7" data-weight-ids="3" href="#">Bare</a></li></ul>`))
	if err != nil || len(results) != 2 {
		t.Fatalf("%+v %v", results, err)
	}
	r := results[0]
	if r.foodID != "5" || r.externalID != "77" || len(r.weightIDs) != 2 || r.brand != "Acme  Plain" || r.serving != "1 cup" || r.calories != 150 {
		t.Errorf("%+v", r)
	}
	if queryEscape("mac & cheese/bowl") != "mac%20%26%20cheese/bowl" {
		t.Error(queryEscape("mac & cheese/bowl"))
	}
}

func TestParseEntryForm(t *testing.T) {
	form, token, err := parseEntryForm([]byte(`<form><input name="authenticity_token" value="abc">
		<p class="food-description">Oats,  rolled</p>
		<input name="food_entry[quantity]" value="1.5">
		<select name="food_entry[weight_id]"><option value="1">1 cup</option><option value="2" selected>100 g</option></select>
		<select name="food_entry[meal_id]"><option value="0" selected>Breakfast</option></select></form>`), "42")
	if err != nil || token != "abc" || form.Name != "Oats, rolled" || form.Quantity != 1.5 || !form.Servings[1].Selected || form.Meals[0].Label != "Breakfast" {
		t.Errorf("%+v %q %v", form, token, err)
	}
}

// Renewed login cookies are saved into cookies.json, keeping what else is there.
func TestRenewedCookiesAreSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	os.WriteFile(path, []byte(`{"cookies":{"remember_me":"old","_mfp_session":"s1"},"username":"someone"}`), 0o600)
	s := &session{path: path}
	if err := s.load(); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: http.Header{"Set-Cookie": {"remember_me=new; Path=/", "__cf_bm=x; Path=/"}}}
	s.keepCookies(resp)
	var saved savedLogin
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &saved)
	if saved.Cookies["remember_me"] != "new" || saved.Cookies["_mfp_session"] != "s1" || saved.Cookies["__cf_bm"] != "" || saved.Username != "someone" {
		t.Errorf("saved %+v", saved)
	}
	if s.cookies["__cf_bm"] != "x" {
		t.Error("other cookies are kept in memory")
	}
	if err := s.load(); err != nil || s.cookies == nil {
		t.Error("its own save shouldn't make it read the file again")
	}
	os.Remove(path)
	if err := s.load(); err == nil || s.cookies != nil {
		t.Error("a removed login should be forgotten")
	}
}

package myfitnesspal

import "testing"

func TestParseExerciseSearch(t *testing.T) {
	page := []byte(`<ul id="matching"><li><a href="/exercise/update_exercise/219?date=2026-01-05">Bench Press, Barbell</a></li>
<li><a href="/exercise/update_exercise/251?date=2026-01-05">Incline Bench Press</a></li><li><a href="/elsewhere/9">no</a></li></ul>`)
	found, err := parseExerciseSearch(page)
	if err != nil || len(found) != 2 || found[0] != (Exercise{ID: "219", Name: "Bench Press, Barbell"}) || found[1].ID != "251" {
		t.Fatalf("got %+v, %v", found, err)
	}
}

func TestParseExerciseForm(t *testing.T) {
	strength := []byte(`<input type="hidden" value="219" name="exercise_entry[exercise_id]">
<input name="authenticity_token" type="hidden" value="tok">
<h3>Weight per set:</h3> <input type="text" name="exercise_entry[weight]" id="exercise_entry_weight"> kg <script>x</script>`)
	form, err := parseExerciseForm(strength)
	if err != nil || form.token != "tok" || !form.kg || form.mets != 0 {
		t.Fatalf("got %+v, %v", form, err)
	}
	cardio := []byte(`<input type="hidden" value="209" name="exercise_entry[exercise_id]">
<input name="authenticity_token" type="hidden" value="t2"><input type="hidden" id="mets" value="3.0">`)
	if form, err := parseExerciseForm(cardio); err != nil || form.mets != 3 || form.kg {
		t.Fatalf("got %+v, %v", form, err)
	}
	if _, err := parseExerciseForm([]byte(`<p>signed out</p>`)); err == nil {
		t.Fatal("a page without the form should fail")
	}
}

func TestFormToken(t *testing.T) {
	page := []byte(`<form action="/account/change_language"><input name="authenticity_token" value="a"></form>
<form action="/exercise/search"><input name="authenticity_token" value="b"></form>`)
	if token, err := formToken(page, "/exercise/search"); err != nil || token != "b" {
		t.Fatalf("got %q, %v", token, err)
	}
}

package myfitnesspal

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"dashboard/internal/text"
)

// MyFitnessPal has no API for some things, so these read its classic web pages: the diary (the
// entry ids that editing takes, the meal ids and the form token), food search (the food and
// serving ids that adding takes) and an entry's edit form.

func parseHTML(page []byte) (*html.Node, error) {
	return html.Parse(bytes.NewReader(page))
}

func attr(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

func hasClass(n *html.Node, class string) bool {
	value, _ := attr(n, "class")
	return strings.Contains(value, class)
}

// textOf is the element's text, whitespace collapsed.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for child := range n.ChildNodes() {
			walk(child)
		}
	}
	walk(n)
	return text.Collapse(b.String())
}

// find lists the elements under n (in document order) that match.
func find(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	for d := range n.Descendants() {
		if d.Type == html.ElementNode && match(d) {
			found = append(found, d)
		}
	}
	return found
}

func tag(name string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Data == name }
}

func named(tagName, name string) func(*html.Node) bool {
	return func(n *html.Node) bool { v, _ := attr(n, "name"); return n.Data == tagName && v == name }
}

func childCells(row *html.Node) []*html.Node {
	var cells []*html.Node
	for child := range row.ChildNodes() {
		if child.Type == html.ElementNode && child.Data == "td" {
			cells = append(cells, child)
		}
	}
	return cells
}

func number(s string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
	return value, err == nil
}

// normalizeMeal compares meal names: case and spacing don't matter, "Snack" is "Snacks".
func normalizeMeal(meal string) string {
	meal = strings.ToLower(text.Collapse(meal))
	if meal == "snack" {
		return "snacks"
	}
	return meal
}

// ---------- the diary page ----------

type pageRow struct {
	entryID, meal, name string
	calories            float64
	hasCalories         bool
}

type diaryPage struct {
	csrf  string
	meals []string // the meal sections, in order: a meal's id is its position
	rows  []pageRow
	read  time.Time
}

func parseDiary(page []byte) (diaryPage, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return diaryPage{}, err
	}
	diary := diaryPage{read: time.Now()}
	for _, meta := range find(doc, named("meta", "csrf-token")) {
		diary.csrf, _ = attr(meta, "content")
	}
	meal := ""
	for _, row := range find(doc, tag("tr")) {
		cells := childCells(row)
		if hasClass(row, "meal_header") {
			label := ""
			if len(cells) > 0 {
				label = textOf(cells[0])
			}
			if label == "" {
				label = fmt.Sprintf("Meal %d", len(diary.meals)+1)
			}
			diary.meals = append(diary.meals, label)
			meal = normalizeMeal(label)
			continue
		}
		anchors := find(row, func(n *html.Node) bool { _, ok := attr(n, "data-food-entry-id"); return n.Data == "a" && ok })
		if len(anchors) == 0 {
			continue
		}
		id, _ := attr(anchors[0], "data-food-entry-id")
		entry := pageRow{entryID: id, meal: meal, name: textOf(anchors[0])}
		if len(cells) > 1 {
			entry.calories, entry.hasCalories = number(textOf(cells[1]))
		}
		diary.rows = append(diary.rows, entry)
	}
	// A diary page without meal sections is the sign-in page.
	if len(diary.meals) == 0 {
		return diary, errSignedOut
	}
	if diary.csrf == "" {
		return diary, fmt.Errorf("couldn't read the MyFitnessPal diary's form token")
	}
	return diary, nil
}

// match takes the page row for an entry from the API: the page names it "<description>,
// <amount>", and calories break ties between copies of one food.
func (d *diaryPage) match(meal, description string, calories float64) string {
	description, meal = text.Collapse(description), normalizeMeal(meal)
	best := -1
	for i, row := range d.rows {
		if row.meal != meal || !strings.HasPrefix(row.name, description) {
			continue
		}
		if best < 0 {
			best = i
		}
		if row.hasCalories && roundHalfEven(row.calories) == roundHalfEven(calories) {
			best = i
			break
		}
	}
	if best < 0 {
		return ""
	}
	id := d.rows[best].entryID
	d.rows = append(d.rows[:best], d.rows[best+1:]...)
	return id
}

var defaultMealPositions = map[string]int{"breakfast": 0, "lunch": 1, "dinner": 2, "snacks": 3}

// mealID is the position of the diary's meal named meal. A renamed default meal ("Lunch" when
// the diary's second meal is called "Midday") is found by its usual position.
func (d diaryPage) mealID(meal string) (string, error) {
	target := normalizeMeal(meal)
	for position, header := range d.meals {
		if normalizeMeal(header) == target {
			return strconv.Itoa(position), nil
		}
	}
	if position, ok := defaultMealPositions[target]; ok && position < len(d.meals) {
		if _, isDefault := defaultMealPositions[normalizeMeal(d.meals[position])]; !isDefault {
			return strconv.Itoa(position), nil
		}
	}
	return "", fmt.Errorf("no MyFitnessPal meal named %q. Your meals: %s", meal, strings.Join(d.meals, ", "))
}

// ---------- food search ----------

type searchResult struct {
	foodID, externalID, name, brand, serving string
	weightIDs                                []string
	calories                                 float64
}

func parseSearch(page []byte) ([]searchResult, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return nil, err
	}
	var results []searchResult
	anchors := find(doc, func(n *html.Node) bool {
		_, original := attr(n, "data-original-id")
		_, weights := attr(n, "data-weight-ids")
		return n.Data == "a" && original && weights
	})
	for _, anchor := range anchors {
		weights, _ := attr(anchor, "data-weight-ids")
		weightIDs := strings.FieldsFunc(weights, func(r rune) bool { return r == ',' })
		if len(weightIDs) == 0 {
			continue
		}
		result := searchResult{weightIDs: weightIDs, name: textOf(anchor)}
		result.foodID, _ = attr(anchor, "data-original-id")
		result.externalID, _ = attr(anchor, "data-external-id")
		// "<brand>, <serving>, <calories> calories" in the result's list item.
		item := anchor.Parent
		for item != nil && item.Data != "li" {
			item = item.Parent
		}
		if item != nil {
			for _, info := range find(item, func(n *html.Node) bool { return n.Data == "p" && hasClass(n, "search-nutritional-info") }) {
				if info.FirstChild == nil || info.FirstChild.Type != html.TextNode {
					break
				}
				parts := strings.Split(strings.TrimSpace(info.FirstChild.Data), ",")
				if len(parts) >= 3 {
					result.brand = strings.TrimSpace(strings.Join(parts[:len(parts)-2], " "))
				}
				if len(parts) >= 2 {
					result.serving = strings.TrimSpace(parts[len(parts)-2])
				}
				result.calories, _ = number(strings.ReplaceAll(parts[len(parts)-1], "calories", ""))
				break
			}
		}
		results = append(results, result)
	}
	return results, nil
}

// ---------- an entry's edit form ----------

func parseEntryForm(page []byte, entryID string) (EntryForm, string, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return EntryForm{}, "", err
	}
	quantity := find(doc, named("input", "food_entry[quantity]"))
	if len(quantity) == 0 {
		return EntryForm{}, "", fmt.Errorf("couldn't open that entry in MyFitnessPal")
	}
	form := EntryForm{EntryID: entryID}
	value, _ := attr(quantity[0], "value")
	form.Quantity, _ = number(value)
	var description []string
	for _, p := range find(doc, func(n *html.Node) bool { v, _ := attr(n, "class"); return n.Data == "p" && v == "food-description" }) {
		description = append(description, textOf(p))
	}
	form.Name = text.Collapse(strings.Join(description, " "))
	options := func(name string) []Option {
		var list []Option
		for _, sel := range find(doc, named("select", name)) {
			for _, option := range find(sel, tag("option")) {
				id, _ := attr(option, "value")
				_, selected := attr(option, "selected")
				list = append(list, Option{ID: id, Label: textOf(option), Selected: selected})
			}
		}
		return list
	}
	form.Servings, form.Meals = options("food_entry[weight_id]"), options("food_entry[meal_id]")
	token := ""
	if tokens := find(doc, named("input", "authenticity_token")); len(tokens) > 0 {
		token, _ = attr(tokens[0], "value")
	}
	return form, token, nil
}

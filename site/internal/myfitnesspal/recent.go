package myfitnesspal

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"dashboard/internal/problem"
)

// Recent foods are MyFitnessPal's own Recent list (the classic add-food page's Recent tab, the
// same as the phone app's), each with the serving and amount last logged. The tab has no
// nutrition, and its ids aren't the ones the API takes, so each food is found in the diary of the
// last two weeks by name (or else by a search) and read from the API like a search result. Each is
// looked up once: a food's servings and nutrition don't change.

const (
	recentDiaryDays = 14
	recentSearches  = 4 // searches at once, for foods not in those days
)

// Recent is a recently logged food and the amount logged last time.
type Recent struct {
	Food     Food
	Serving  int // index into Food.ServingOptions()
	Quantity float64
}

// RecentFoods lists the foods logged most recently, newest first, kept for a few minutes.
func (s *Syncer) RecentFoods() ([]Recent, error) {
	return s.recent.get(func() (recent []Recent, err error) {
		if s.known == nil {
			s.known = map[string]Food{}
		}
		err = s.client.call(lookupTimeout, func(ctx context.Context) (err error) {
			recent, err = s.client.recentFoods(ctx, time.Now(), s.known)
			return err
		})
		return recent, err
	})
}

// recentFoods reads the Recent tab, adding to known the foods it looks up.
func (c Client) recentFoods(ctx context.Context, today time.Time, known map[string]Food) ([]Recent, error) {
	s := c.session()
	page, err := s.getPage(ctx, "food/load_recent?date="+today.Format("2006-01-02")+"&meal=0")
	if err != nil {
		return nil, err
	}
	rows, err := parseLoadRecent(page)
	if err != nil {
		return nil, err
	}
	recent := make([]Recent, len(rows))
	var missing []int
	for i, row := range rows {
		recent[i].Quantity = row.quantity
		if food, ok := known[row.foodID]; ok {
			recent[i].Food = food
		} else {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		logged := s.loggedFoods(ctx, today)
		searches := make(chan struct{}, recentSearches)
		found := make([]bool, len(rows))
		var wg sync.WaitGroup
		for _, i := range missing {
			row := rows[i]
			recent[i].Food = Food{FoodID: row.foodID, Name: row.name, Brand: row.brand, Verified: row.verified, DefaultWeightID: row.weightIDs[0]}
			wg.Go(func() {
				result := row.searchResult
				result.externalID = logged[foodKey(row.fullName)]
				if result.externalID == "" {
					searches <- struct{}{}
					result.externalID = s.searchedID(ctx, row)
					<-searches
				}
				found[i] = result.externalID != "" && s.foodDetails(ctx, &recent[i].Food, result)
			})
		}
		wg.Wait()
		for _, i := range missing {
			recent[i].Food.NoNutrition = !found[i]
			known[rows[i].foodID] = recent[i].Food
		}
	}
	for i, row := range rows {
		for j, serving := range recent[i].Food.Servings {
			if serving.WeightID == row.weightID {
				recent[i].Serving = j
			}
		}
	}
	return recent, nil
}

// loggedFoods are the API's ids of the foods in the diary of the last two weeks, by name.
func (s *session) loggedFoods(ctx context.Context, today time.Time) map[string]string {
	days := make([][]diaryItem, recentDiaryDays)
	var wg sync.WaitGroup
	for i := range days {
		wg.Go(func() {
			var answer struct{ Items []diaryItem }
			if s.getJSON(ctx, "v2/diary?entry_date="+today.AddDate(0, 0, -i).Format("2006-01-02")+"&types=food_entry", &answer) == nil {
				days[i] = answer.Items
			}
		})
	}
	wg.Wait()
	ids := map[string]string{}
	for i := len(days) - 1; i >= 0; i-- { // the latest day wins
		for _, item := range days[i] {
			if item.Food.ID != "" && numericID.MatchString(item.Food.ID) {
				ids[foodKey(item.Food.Description)] = item.Food.ID
			}
		}
	}
	return ids
}

// searchedID finds a recent food by searching for its name: the result with its food id.
func (s *session) searchedID(ctx context.Context, row recentRow) string {
	query, ok := SearchQuery(row.fullName)
	if !ok {
		return ""
	}
	page, err := s.getPage(ctx, "food/search?search="+queryEscape(query)+"&page=1")
	if err != nil {
		return ""
	}
	results, _ := parseSearch(page)
	for _, result := range results {
		if result.foodID == row.foodID && len(result.weightIDs) == len(row.weightIDs) {
			return result.externalID
		}
	}
	return ""
}

func foodKey(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }

type recentRow struct {
	searchResult
	fullName string // "Brand - Name", as the diary has it
	weightID string // the serving logged last time
	quantity float64
	verified bool
}

// parseLoadRecent reads the Recent tab: each row has the food's ids, its name ("Brand - Name"),
// the amount last logged and a list of servings with the last one selected.
func parseLoadRecent(page []byte) ([]recentRow, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return nil, err
	}
	var rows []recentRow
	for _, tr := range find(doc, tag("tr")) {
		var row recentRow
		for _, input := range find(tr, tag("input")) {
			field, _ := attr(input, "name")
			value, _ := attr(input, "value")
			switch {
			case strings.HasSuffix(field, "[food_id]"):
				row.foodID = value
			case strings.HasSuffix(field, "[quantity]"):
				row.quantity, _ = strconv.ParseFloat(value, 64)
			}
			if id, ok := attr(input, "data-food-external-id"); ok {
				row.externalID = id
				verified, _ := attr(input, "data-food-verified")
				row.verified = verified == "true"
			}
		}
		for _, sel := range find(tr, tag("select")) {
			if field, _ := attr(sel, "name"); !strings.HasSuffix(field, "[weight_id]") {
				continue
			}
			for i, option := range find(sel, tag("option")) {
				id, _ := attr(option, "value")
				row.weightIDs = append(row.weightIDs, id)
				if _, selected := attr(option, "selected"); selected || i == 0 {
					row.weightID, row.serving = id, textOf(option)
				}
			}
		}
		for _, cell := range childCells(tr) {
			if len(find(cell, tag("select"))) == 0 && textOf(cell) != "" {
				row.name = textOf(cell)
				break
			}
		}
		row.fullName = row.name
		if brand, name, ok := strings.Cut(row.name, " - "); ok && brand != "" && name != "" {
			row.brand, row.name = brand, name
		}
		if !numericID.MatchString(row.foodID) || len(row.weightIDs) == 0 || !numericID.MatchString(row.weightID) || row.name == "" {
			continue
		}
		if row.quantity <= 0 {
			row.quantity = 1
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// kept is a list read from MyFitnessPal, reused for a few minutes, or for a minute after a
// failure so typing doesn't ask again on every keystroke.
type kept[T any] struct {
	mu   sync.Mutex
	list []T
	err  error
	read time.Time
}

const (
	keptFor   = 10 * time.Minute
	keptRetry = time.Minute
)

func (k *kept[T]) get(read func() ([]T, error)) ([]T, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	reuse := keptFor
	if k.err != nil {
		reuse = keptRetry
	}
	if !k.read.IsZero() && time.Since(k.read) < reuse {
		return k.list, k.err
	}
	list, err := read()
	if err != nil {
		list, err = nil, problem.Unavailablef(err.Error())
	}
	k.list, k.err, k.read = list, err, time.Now()
	return list, err
}

// forget makes the next get read the list again.
func (k *kept[T]) forget() {
	k.mu.Lock()
	k.read = time.Time{}
	k.mu.Unlock()
}

// MatchesWords reports whether s has each of the (lower-case) words.
func MatchesWords(s string, words []string) bool {
	s = strings.ToLower(s)
	for _, word := range words {
		if !strings.Contains(s, word) {
			return false
		}
	}
	return true
}

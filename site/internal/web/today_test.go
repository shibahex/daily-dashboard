package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dashboard/internal/myfitnesspal"
)

func TestShownDay(t *testing.T) {
	today := startOfDay(time.Now())
	yesterday := today.AddDate(0, 0, -1).Format(dayLayout)
	tomorrow := today.AddDate(0, 0, 1).Format(dayLayout)

	cases := []struct {
		name, target, currentURL string
		want                     string
	}{
		{"plain page", "/", "", today.Format(dayLayout)},
		{"page for yesterday", "/?day=" + yesterday, "", yesterday},
		{"no future days", "/?day=" + tomorrow, "", today.Format(dayLayout)},
		{"a card on yesterday's page", "/cards/weight", "http://host/?day=" + yesterday, yesterday},
		{"a card on another tab ignores its day", "/cards/weight", "http://host/history?day=" + yesterday, today.Format(dayLayout)},
		{"the card's own day wins", "/cards/checklist?day=" + yesterday, "http://host/", yesterday},
		{"nonsense", "/?day=soon", "", today.Format(dayLayout)},
		// Clicking ‹ is a boosted request: HX-Current-URL is still the day being left.
		{"moving to another day", "/?day=" + yesterday, "http://host/", yesterday},
		{"moving back to today", "/", "http://host/?day=" + yesterday, today.Format(dayLayout)},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.target, nil)
		if c.currentURL != "" {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Current-URL", c.currentURL)
		}
		if got := shownDay(r).Format(dayLayout); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDayNav(t *testing.T) {
	today := startOfDay(time.Now())
	nav := newDayNav(today.AddDate(0, 0, -1))
	if nav.IsToday || !nav.NextIsToday || nav.Prev != today.AddDate(0, 0, -2).Format(dayLayout) {
		t.Errorf("yesterday: %+v", nav)
	}
	if dayName(today) != "today" || dayName(today.AddDate(0, 0, -1)) != "yesterday" {
		t.Error("day names")
	}
}

// The Today templates render for an earlier day.
func TestEarlierDayTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	yesterday := startOfDay(time.Now()).AddDate(0, 0, -1)
	nav := newDayNav(yesterday)
	views := map[string]any{
		"page":           pageView{Tab: "today", Nav: &nav},
		"card-session":   sessionCardView{Heading: "Training · yesterday"},
		"card-checklist": checklistCardView{Day: yesterday.AddDate(0, 0, -2), PageDay: "yesterday"},
		"card-weight":    weightCardView{DayName: "yesterday", TodayLB: 180},
		"card-time":      timeCardView{Available: true, DayName: "yesterday", WeekName: "that week"},
		"card-goals": goalsCardView{DayName: "yesterday", Syncing: true, Nutrition: &nutritionView{DayName: "yesterday",
			Meals: []myfitnesspal.Meal{{Name: "Lunch", Entries: []myfitnesspal.Entry{{Name: "Eggs", EntryID: "12"}}}}}},
	}
	for name, view := range views {
		html, err := ui.renderToHTML(name, view)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		page := string(html)
		switch name {
		case "page":
			if !strings.Contains(page, `href="/">Today</a>`) || !strings.Contains(page, `class="date day-nav"`) {
				t.Errorf("page: no arrows or way back:\n%s", page)
			}
		case "card-goals":
			if !strings.Contains(page, "Nutrition · yesterday") {
				t.Errorf("card-goals: heading:\n%s", page)
			}
		}
	}
}

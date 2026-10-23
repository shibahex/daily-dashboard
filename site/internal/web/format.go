package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/tasks"
	"dashboard/internal/text"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// templateFuncs are the helpers templates call.
var templateFuncs = template.FuncMap{
	// numbers and text
	"number":      formatNumber,
	"percent":     percentOf,
	"percentText": func(ratio float64) string { return strconv.Itoa(int(math.Round(ratio*100))) + "%" },
	"pounds":      func(lb float64) string { return strconv.FormatFloat(lb, 'f', -1, 64) },
	"abs":         math.Abs,
	"negate":      func(v float64) float64 { return -v },
	"float":       func(n int) float64 { return float64(n) },
	"add":         func(a, b int) int { return a + b },
	"multiply":    func(a, b int) int { return a * b },
	"plural":      plural,
	"expiresIn":   expiresIn,
	"capitalize":  capitalize,
	"truncate":    text.Truncate,
	"join":        strings.Join,
	"markdown":    renderMarkdown,

	// times
	"duration":  formatDuration,
	"minutes":   minutesText,
	"shortDay":  func(t time.Time) string { return t.Format("Mon, Jan 2") },
	"monthDay":  func(t time.Time) string { return t.Format("Jan 2") },
	"longDay":   func(t time.Time) string { return t.Format("Monday, January 2, 2006") },
	"isoDay":    func(t time.Time) string { return t.Format("2006-01-02") },
	"timeRange": timeRange,

	// building attributes and passing values
	"dict":  dict,
	"vals":  vals,
	"query": url.QueryEscape,

	// the pages' fixed choices and small views
	"tabs":        func() []navTab { return navTabs },
	"taskViews":   func() []choice { return taskViews },
	"priorities":  func() []choice { return priorityChoices },
	"repeatTypes": func() []choice { return repeatTypes },
	"reasonFor":   reasonFor,
	"toggleDay":   toggleDay,
}

// navTab is a tab in the page's navigation.
type navTab struct{ ID, Label, URL string }

var navTabs = []navTab{
	{"today", "Today", "/"}, {"tasks", "Tasks", "/tasks"}, {"lifting", "Lifting", "/lifting"}, {"anki", "Anki", "/anki"},
	{"history", "History", "/history"}, {"mcps", "MCPs", "/mcps"}, {"docs", "Docs", "/docs"},
}

// choice is an option of a fixed menu or a row of radio buttons.
type choice struct{ ID, Label string }

var (
	taskViews       = []choice{{"pending", "Pending"}, {"repeating", "Repeating"}, {"completed", "Completed"}, {"deleted", "Deleted"}}
	priorityChoices = []choice{{"", "None"}, {"H", "High"}, {"M", "Medium"}, {"L", "Low"}}
	repeatTypes     = []choice{{"days", "Every N days"}, {"weekly", "On weekdays"}, {"monthly", "Monthly"}}
)

// reasonFor is the clickable reason of skipped tasks.
func reasonFor(note tasks.SkipNote, uuids string) reasonView {
	return reasonView{UUIDs: uuids, Reason: note.Reason}
}

// toggleDay is the day to ask for when a heatmap cell is clicked: none if it's already picked.
func toggleDay(day time.Time, selected bool) string {
	if selected {
		return ""
	}
	return day.Format("2006-01-02")
}

// vals is an hx-vals object from key, value pairs: {"item": "Weigh in"}.
func vals(pairs ...any) string {
	encoded, _ := json.Marshal(dict(pairs...))
	return string(encoded)
}

// formatNumber rounds to a whole number with thousands separators: 1,618.
func formatNumber(v float64) string {
	n := int64(math.Round(v))
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return sign + out.String()
}

// percentOf is v as a percentage of goal, capped at 100 (for progress bars).
func percentOf(v, goal float64) float64 {
	if goal <= 0 {
		return 0
	}
	return math.Min(100, v/goal*100)
}

// formatDuration is "42m" or "1h 05m".
func formatDuration(d time.Duration) string {
	minutes := int(math.Round(d.Minutes()))
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}

// timeRange is a 12-hour range: "7:00–7:45 PM", "11:30 AM–12:15 PM", or "9:00 PM–now".
func timeRange(start, end time.Time, running bool) string {
	if running {
		return start.Format("3:04 PM") + "–now"
	}
	if start.Format("PM") == end.Format("PM") {
		return start.Format("3:04") + "–" + end.Format("3:04 PM")
	}
	return start.Format("3:04 PM") + "–" + end.Format("3:04 PM")
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// dict builds a map from key, value pairs, to pass several values to a {{template}}.
func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[fmt.Sprint(pairs[i])] = pairs[i+1]
	}
	return m
}

// markdownRenderer turns Markdown into HTML. Raw HTML in the text is left out (goldmark's
// default), so a note or an AI answer can't inject markup.
var markdownRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

func renderMarkdown(source string) template.HTML {
	var out bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &out); err != nil {
		return template.HTML(template.HTMLEscapeString(source))
	}
	return template.HTML(out.String())
}

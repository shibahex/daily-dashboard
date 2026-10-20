package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// rowEditor edits a list of rows (checklist items, reminders, goals, exercises). Its buttons
// post the rows as they are plus an operation, and the card answers with the edited list, so
// nothing is saved until Save.
type rowEditor struct {
	URL      string // the card's URL: the form posts here
	Columns  []rowColumn
	Rows     [][]string
	AddLabel string
}

type rowColumn struct {
	Placeholder string
	Narrow      bool
}

// columnField is the form name of a column's inputs: c0, c1, ...
func columnField(column int) string { return fmt.Sprintf("c%d", column) }

// rowsFromForm reads the rows as typed: each column's inputs, in order.
func rowsFromForm(r *http.Request, columns int) [][]string {
	r.ParseForm()
	count := len(r.Form[columnField(0)])
	rows := make([][]string, count)
	for i := range rows {
		rows[i] = make([]string, columns)
		for column := 0; column < columns; column++ {
			if values := r.Form[columnField(column)]; i < len(values) {
				rows[i][column] = values[i]
			}
		}
	}
	return rows
}

// rowOperation is "add", "up:3", "down:3" or "remove:3", or "" for Save.
func rowOperation(r *http.Request) string { return r.FormValue("op") }

// applyRowOperation returns the rows after an add, move or remove.
func applyRowOperation(rows [][]string, operation string, columns int) [][]string {
	name, indexText, _ := strings.Cut(operation, ":")
	i, err := strconv.Atoi(indexText)
	inRange := err == nil && i >= 0 && i < len(rows)
	switch {
	case name == "add":
		return append(rows, make([]string, columns))
	case name == "up" && inRange && i > 0:
		rows[i-1], rows[i] = rows[i], rows[i-1]
	case name == "down" && inRange && i < len(rows)-1:
		rows[i+1], rows[i] = rows[i], rows[i+1]
	case name == "remove" && inRange:
		return append(rows[:i], rows[i+1:]...)
	}
	return rows
}

// cleanRows trims every value and drops rows whose first value is empty.
func cleanRows(rows [][]string) [][]string {
	var kept [][]string
	for _, row := range rows {
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		if len(row) > 0 && row[0] != "" {
			kept = append(kept, row)
		}
	}
	return kept
}

// firstColumn is each row's first value (for single-column lists).
func firstColumn(rows [][]string) []string {
	values := make([]string, len(rows))
	for i, row := range rows {
		values[i] = row[0]
	}
	return values
}

// asRows turns a list into single-column rows.
func asRows(values []string) [][]string {
	rows := make([][]string, len(values))
	for i, v := range values {
		rows[i] = []string{v}
	}
	return rows
}

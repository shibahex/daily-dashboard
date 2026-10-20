// Package text has small string helpers shared by the other packages.
package text

import (
	"strings"
	"unicode/utf8"
)

// Collapse trims the string and turns every run of whitespace into a single space.
func Collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// Truncate keeps at most max characters (not bytes, so it never splits a character).
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// FirstLine is the first non-empty line, or fallback when there is none.
func FirstLine(s, fallback string) string {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return fallback
}

// LastLine is the last non-empty line, or fallback when there is none.
func LastLine(s, fallback string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return fallback
}

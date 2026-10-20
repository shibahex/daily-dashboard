package server

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Body is a POST request's JSON object. Values are decoded only when a handler asks for them,
// so values that are just passed on (a plan, Anki params) keep their exact shape.
type Body map[string]json.RawMessage

// Has reports whether the key was sent at all (even as null).
func (b Body) Has(key string) bool { _, ok := b[key]; return ok }

// IsSet reports whether the key was sent with a value other than null.
func (b Body) IsSet(key string) bool { return b.Has(key) && !isNull(b[key]) }

// Raw is the value as sent.
func (b Body) Raw(key string) json.RawMessage { return b[key] }

// String is the value as text: strings as-is, numbers as written, missing or null as "".
func (b Body) String(key string) string { return rawText(b[key]) }

// TrimmedString is String without surrounding whitespace.
func (b Body) TrimmedString(key string) string { return strings.TrimSpace(b.String(key)) }

// Int is the value if it's a whole number written without a decimal point.
func (b Body) Int(key string) (int, bool) {
	literal := strings.TrimSpace(string(b[key]))
	if literal == "" || strings.ContainsAny(literal, ".eE\"") {
		return 0, false
	}
	n, err := strconv.Atoi(literal)
	return n, err == nil
}

// Number is the value if it's a JSON number (not a string or a bool).
func (b Body) Number(key string) (float64, bool) {
	literal := strings.TrimSpace(string(b[key]))
	if literal == "" || !(literal[0] == '-' || (literal[0] >= '0' && literal[0] <= '9')) {
		return 0, false
	}
	n, err := strconv.ParseFloat(literal, 64)
	return n, err == nil
}

// Strings is a list's items as text; anything other than a list gives nil.
func (b Body) Strings(key string) []string {
	var items []json.RawMessage
	if json.Unmarshal(b[key], &items) != nil {
		return nil
	}
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = rawText(item)
	}
	return texts
}

// Object is a nested object.
func (b Body) Object(key string) (Body, bool) { return decodeBody(b[key]) }

// Decoded decodes the value into a generic Go value (maps, slices, json.Number...).
func (b Body) Decoded(key string, v any) bool {
	decoder := json.NewDecoder(bytes.NewReader(b[key]))
	decoder.UseNumber()
	return decoder.Decode(v) == nil
}

func decodeBody(raw json.RawMessage) (Body, bool) {
	var body Body
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, false
	}
	return body, true
}

func isNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func rawText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
	}
	return string(trimmed)
}

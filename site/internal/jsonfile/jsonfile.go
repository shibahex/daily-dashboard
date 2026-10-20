// Package jsonfile reads and writes the dashboard's JSON files.
package jsonfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Read decodes the file into v. It returns false, leaving v as it was, when the file is
// missing or isn't valid JSON. Numbers decode as json.Number, so they keep their exact value.
func Read(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(v) == nil
}

// Write saves v with one-space indents. It writes a temp file first and renames it over the
// old one, so a crash never leaves half a file.
func Write(path string, v any) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(v); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp"
	if err := os.WriteFile(tmp, bytes.TrimRight(buf.Bytes(), "\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

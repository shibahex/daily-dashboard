package web

import (
	"encoding/json"
	"testing"
)

func TestASCIIJSONKeepsTheText(t *testing.T) {
	encoded, _ := json.Marshal(map[string]string{"message": "Done ✓ · “quoted” 🎉"})
	header := asciiJSON(encoded)
	for _, r := range header {
		if r >= 0x80 {
			t.Fatalf("not ASCII: %q", header)
		}
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(header), &decoded); err != nil || decoded["message"] != "Done ✓ · “quoted” 🎉" {
		t.Fatalf("decoded %q (%v)", decoded["message"], err)
	}
}

func TestParseTook(t *testing.T) {
	cases := map[string]int{"": 0, "30": 30, "45m": 45, "1h": 60, "1.5h": 90, "1h 30m": 90, "1h30": 90, "2 hours": 120, "1:30": 90}
	for typed, want := range cases {
		if got, err := parseTook(typed); err != nil || got != want {
			t.Errorf("parseTook(%q) = %d, %v; want %d", typed, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "25h", "0"} {
		if _, err := parseTook(bad); err == nil {
			t.Errorf("parseTook(%q) should fail", bad)
		}
	}
}

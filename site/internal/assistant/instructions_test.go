package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestInstructionsTime(t *testing.T) {
	zone := time.FixedZone("EDT", -4*3600)
	now := Now(time.Date(2026, 10, 27, 19, 42, 0, 0, zone))
	want := "It's Tuesday, October 27, 2026, 7:42 PM for the user (EDT, UTC-04:00); today's date is 2026-10-27."
	if !strings.HasPrefix(now, want) {
		t.Fatalf("Now: %q", now)
	}
	if prompt := Instructions(now, "", nil, false, false); !strings.HasPrefix(prompt, now+"\n") {
		t.Errorf("the time should open the prompt: %q", prompt[:80])
	}
	if prompt := Instructions("", "", nil, false, false); strings.HasPrefix(prompt, "\n") || strings.Contains(prompt, "It's ") {
		t.Errorf("without a time, the prompt should leave it out: %q", prompt[:80])
	}
}

package timewarrior

import (
	"slices"
	"testing"
)

func TestCleanTagsRefusesWhatTimewWouldReadAsSomethingElse(t *testing.T) {
	for _, tag := range []string{":fill", "@1", "rc.verbose=off", "RC.data.location=/tmp", "from", "-"} {
		if _, err := CleanTags([]string{tag}); err == nil {
			t.Errorf("%q: let through", tag)
		}
	}
	if tags, err := CleanTags([]string{"Study networking", "health"}); err != nil || len(tags) != 2 {
		t.Errorf("plain tags refused: %v %v", tags, err)
	}
}

func TestTaskTagsNeutralisesTaskText(t *testing.T) {
	got := TaskTags([]string{":adjust", "rc.verbose=off", "  Study   networking ", "", "for"})
	want := []string{"_:adjust", "_rc.verbose=off", "Study networking", "_for"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

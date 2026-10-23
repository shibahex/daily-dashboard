package problem

import (
	"errors"
	"fmt"
	"testing"
)

func TestPublic(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{Invalidf("pick a day"), "pick a day"},
		{fmt.Errorf("%w: %v", Unavailablef("Couldn't reach MyFitnessPal"), errors.New("dial tcp 10.0.0.2:443: refused")), "Couldn't reach MyFitnessPal"},
		{errors.New("open /app/site/plan.example.json: no such file or directory"), Hidden},
		{fmt.Errorf("reading plan: %w", errors.New("open /secret")), Hidden},
	} {
		if got := Public(c.err); got != c.want {
			t.Errorf("Public(%q) = %q, want %q", c.err, got, c.want)
		}
	}
}

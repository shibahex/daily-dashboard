package myfitnesspal

import (
	"errors"
	"testing"
)

func TestAuthFailureFindsTheReason(t *testing.T) {
	output := "Connect your MyFitnessPal account\n(no terminal detected)\n" +
		"Those cookies didn't authenticate: Could not access MyFitnessPal using the cookies provided.\n" +
		"If this mentions Cloudflare or a 403, try MFP_IMPERSONATE=chrome124.\nValidating with MyFitnessPal...\n"
	if got := authFailure(output, errors.New("exit status 1")); got != "Could not access MyFitnessPal using the cookies provided." {
		t.Fatalf("got %q", got)
	}
	if got := authFailure("something else broke\n", errors.New("exit status 1")); got != "something else broke" {
		t.Fatalf("got %q", got)
	}
}

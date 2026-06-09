package project

import (
	"strings"
	"testing"
)

func TestCanonicalSlug(t *testing.T) {
	valid := []string{"my-app", "blog", "a1", "x-y-z", "App", "  Hello-World  "}
	for _, raw := range valid {
		got, err := CanonicalSlug(raw)
		if err != nil {
			t.Errorf("CanonicalSlug(%q) = error %v, want ok", raw, err)
			continue
		}
		if want := strings.ToLower(strings.TrimSpace(raw)); got != want {
			t.Errorf("CanonicalSlug(%q) = %q, want canonical %q", raw, got, want)
		}
	}

	invalid := []string{
		"a",        // too short (min 2)
		"-x", "x-", // leading/trailing hyphen
		"x--y",                                       // double hyphen
		"up_per",                                     // underscore not allowed
		"white space",                                // space
		"",                                           // empty
		"café",                                       // non-ascii
		"edit", "settings", "new", "delete", "admin", // reserved
	}
	for _, raw := range invalid {
		if _, err := CanonicalSlug(raw); err == nil {
			t.Errorf("CanonicalSlug(%q) should be rejected", raw)
		}
	}
}

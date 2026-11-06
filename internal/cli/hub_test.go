package cli

import (
	"testing"
	"time"
)

func TestParseRetention(t *testing.T) {
	for value, want := range map[string]time.Duration{"": 90 * 24 * time.Hour, "0": 0, "30d": 30 * 24 * time.Hour, "720h": 720 * time.Hour, " 7d ": 7 * 24 * time.Hour} {
		if got, err := parseRetention(value); err != nil || got != want {
			t.Errorf("parseRetention(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, bad := range []string{"forever", "-1d", "1.5d", "-5h"} {
		if _, err := parseRetention(bad); err == nil {
			t.Errorf("parseRetention(%q) accepted", bad)
		}
	}
}

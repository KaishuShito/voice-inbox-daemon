package pipeline

import (
	"testing"
	"time"
)

func TestNextRetryAt(t *testing.T) {
	now := time.Date(2026, 2, 26, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		attempts int
		wantSec  int
	}{
		{attempts: 1, wantSec: 300},
		{attempts: 2, wantSec: 600},
		{attempts: 3, wantSec: 1200},
		{attempts: 8, wantSec: 38400},
		{attempts: 12, wantSec: 86400},
	}

	for _, tc := range cases {
		got := NextRetryAt(now, tc.attempts, 300, 86400)
		if got.Sub(now) != time.Duration(tc.wantSec)*time.Second {
			t.Fatalf("attempts=%d want=%ds got=%s", tc.attempts, tc.wantSec, got.Sub(now))
		}
	}
}

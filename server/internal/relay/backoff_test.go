package relay

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		1: 1 * time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 16 * time.Second,
		0: 1 * time.Second,
	}
	for retry, want := range cases {
		if got := Backoff(retry); got != want {
			t.Errorf("Backoff(%d)=%v, want %v", retry, got, want)
		}
	}
}

func TestBackoffForFixed30Minutes(t *testing.T) {
	sequence := []time.Duration{30 * time.Minute}
	for retry := 1; retry <= 5; retry++ {
		if got := BackoffFor(sequence, retry); got != 30*time.Minute {
			t.Errorf("BackoffFor(30m, %d)=%v, want 30m", retry, got)
		}
	}
}

func TestBackoffForCustomSequenceCapsAtLast(t *testing.T) {
	sequence := []time.Duration{30 * time.Minute, 60 * time.Minute}
	if got := BackoffFor(sequence, 1); got != 30*time.Minute {
		t.Errorf("retry 1: got %v", got)
	}
	if got := BackoffFor(sequence, 2); got != 60*time.Minute {
		t.Errorf("retry 2: got %v", got)
	}
	if got := BackoffFor(sequence, 10); got != 60*time.Minute {
		t.Errorf("retry 10 should cap at last: got %v", got)
	}
}

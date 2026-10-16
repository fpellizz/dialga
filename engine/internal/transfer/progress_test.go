package transfer

import (
	"testing"
	"time"
)

// fakeClock moves only when told.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestTracker(total int64) (*tracker, *fakeClock, *[]Progress) {
	clock := &fakeClock{t: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	var got []Progress
	tr := newTracker(Progress{Key: "k", Total: total, PartsTotal: 4},
		Options{OnProgress: func(p Progress) { got = append(got, p) }, now: clock.now})
	return tr, clock, &got
}

func TestRateAndETA(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		samples  []sample
		total    int64
		wantRate float64
		wantETA  int64
	}{
		{"one sample", []sample{{t0, 0}}, 100, 0, 0},
		{"under half a second", []sample{{t0, 0}, {t0.Add(400 * time.Millisecond), 50}}, 100, 0, 0},
		{"10 MiB/s, 90 MiB left", []sample{{t0, 0}, {t0.Add(time.Second), 10 * MiB}}, 100 * MiB, 10 * MiB, 9},
		{"stalled", []sample{{t0, 5}, {t0.Add(3 * time.Second), 5}}, 100, 0, 0},
		{"rounds the ETA", []sample{{t0, 0}, {t0.Add(2 * time.Second), 3}}, 10, 1.5, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rate, eta := rateAndETA(tc.samples, tc.total)
			if rate != tc.wantRate || eta != tc.wantETA {
				t.Fatalf("rate %v, eta %d; want %v, %d", rate, eta, tc.wantRate, tc.wantETA)
			}
		})
	}
}

func TestTrackerThrottlesUpdates(t *testing.T) {
	tr, clock, got := newTestTracker(100 * MiB)
	tr.begin(0, 0)
	// 100 updates in one second: at most 4 go out, plus the first.
	for i := range 100 {
		clock.advance(10 * time.Millisecond)
		tr.set(1, int64(i+1)*256*KiB)
	}
	if n := len(*got); n < 4 || n > 5 {
		t.Fatalf("%d updates in a second, want at most 4 after the first", n)
	}
	last := (*got)[len(*got)-1]
	if last.Done != 25*MiB || last.BytesPerSecond <= 0 || last.ETASeconds <= 0 {
		t.Fatalf("last update %+v", last)
	}
}

func TestTrackerDoneNeverGoesBack(t *testing.T) {
	tr, clock, got := newTestTracker(40)
	tr.begin(0, 0)
	clock.advance(time.Second)
	tr.set(1, 10)
	clock.advance(time.Second)
	tr.set(1, 0) // the part is retried from the start
	clock.advance(time.Second)
	tr.set(1, 4)
	if p := (*got)[len(*got)-1]; p.Done != 10 {
		t.Fatalf("done went back to %d", p.Done)
	}
	clock.advance(time.Second)
	tr.partDone(1, 10)
	tr.partDone(2, 30) // within 250 ms: not sent, but counted
	elapsed, rate := tr.finish()
	p := (*got)[len(*got)-1]
	if p.Done != 40 || p.Parts != 2 || p.ETASeconds != 0 || elapsed != 4*time.Second || rate != 10 || p.BytesPerSecond != 10 {
		t.Fatalf("final %+v, elapsed %s, rate %v", p, elapsed, rate)
	}
}

func TestTrackerResumedBytesAreNotRate(t *testing.T) {
	tr, clock, got := newTestTracker(100)
	tr.begin(60, 3) // three parts already in S3
	clock.advance(2 * time.Second)
	tr.partDone(4, 40)
	if p := (*got)[0]; p.Done != 60 || p.Parts != 3 {
		t.Fatalf("first update %+v", p)
	}
	if _, rate := tr.finish(); rate != 20 {
		t.Fatalf("rate %v, want 20 (only the bytes moved now)", rate)
	}
}

package transfer

import (
	"sync"
	"testing"
	"time"
)

// fakeClock moves only when told; the progress goroutine reads it too.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// received collects the delivered updates.
type received struct {
	mu sync.Mutex
	p  []Progress
}

func (r *received) add(p Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p = append(r.p, p)
}

func (r *received) all() []Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Progress{}, r.p...)
}

func newTestTracker(total int64) (*tracker, *fakeClock, *received) {
	clock := &fakeClock{t: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	got := &received{}
	tr := newTracker(Progress{Key: "k", Total: total, PartsTotal: 4}, Options{OnProgress: got.add, now: clock.now})
	return tr, clock, got
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
	token := tr.attempt(1)
	// 100 updates in one second: at most 4 go out, plus the first.
	for i := range 100 {
		clock.advance(10 * time.Millisecond)
		tr.set(1, token, int64(i+1)*256*KiB)
	}
	tr.stop()
	all := got.all()
	if n := len(all); n < 1 || n > 5 {
		t.Fatalf("%d updates in a second, want at most 4 after the first", n)
	}
	last := all[len(all)-1]
	if last.Done != 25*MiB || last.BytesPerSecond != 25*MiB || last.ETASeconds != 3 {
		t.Fatalf("last update %+v", last)
	}
}

func TestTrackerDoneNeverGoesBack(t *testing.T) {
	tr, clock, got := newTestTracker(40)
	tr.begin(0, 0)
	first := tr.attempt(1)
	clock.advance(time.Second)
	tr.set(1, first, 10)
	tr.drop(1) // the attempt failed
	clock.advance(time.Second)
	tr.set(1, first, 12) // the transport still reading the failed attempt
	second := tr.attempt(1)
	clock.advance(time.Second)
	tr.set(1, second, 4)
	clock.advance(time.Second)
	tr.partDone(1, 10)
	tr.set(1, second, 10) // late, after the part is done
	tr.partDone(2, 30)    // within 250 ms: not sent, but counted
	elapsed, rate := tr.finish()
	all := got.all()
	for i := 1; i < len(all); i++ {
		if all[i].Done < all[i-1].Done {
			t.Fatalf("done went back: %+v", all)
		}
	}
	for _, p := range all {
		if p.Done > 10 && p.Parts < 2 {
			t.Fatalf("a finished attempt was counted: %+v", all)
		}
	}
	p := all[len(all)-1]
	if p.Done != 40 || p.Parts != 2 || p.ETASeconds != 0 || elapsed != 4*time.Second || rate != 10 || p.BytesPerSecond != 10 {
		t.Fatalf("final %+v, elapsed %s, rate %v", p, elapsed, rate)
	}
}

func TestTrackerResumedBytesAreNotRate(t *testing.T) {
	tr, clock, got := newTestTracker(100)
	tr.begin(60, 3) // three parts already in S3
	clock.advance(2 * time.Second)
	tr.partDone(4, 40)
	if _, rate := tr.finish(); rate != 20 {
		t.Fatalf("rate %v, want 20 (only the bytes moved now)", rate)
	}
	// Updates may be replaced by newer ones, but none shows less than resumed.
	if p := got.all()[0]; p.Done < 60 || p.Parts < 3 {
		t.Fatalf("first update %+v", p)
	}
}

func TestSlowProgressNeverBlocksTheParts(t *testing.T) {
	release := make(chan struct{})
	got := &received{}
	clock := &fakeClock{t: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	tr := newTracker(Progress{Total: 1000}, Options{now: clock.now, OnProgress: func(p Progress) {
		<-release // an SSE client that does not read
		got.add(p)
	}})
	tr.begin(0, 0)
	token := tr.attempt(1)
	finished := make(chan struct{})
	go func() {
		for i := range 999 {
			clock.advance(time.Second)
			tr.set(1, token, int64(i+1))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the parts waited for the progress callback")
	}
	close(release)
	tr.partDone(1, 1000)
	tr.finish()
	// The stalled updates were replaced by newer ones; the last one arrives.
	all := got.all()
	if len(all) > 3 || all[len(all)-1].Done != 1000 {
		t.Fatalf("%d updates delivered, last %+v", len(all), all[len(all)-1])
	}
}

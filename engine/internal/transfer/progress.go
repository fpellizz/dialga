package transfer

import (
	"sync"
	"time"
)

// Progress is the state of one transfer, as OnProgress receives it.
type Progress struct {
	// Step is the plan action the transfer belongs to.
	Step  string `json:"step,omitempty"`
	Path  string `json:"path"`
	Key   string `json:"key"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	// Parts (or ranges) completed, out of PartsTotal.
	Parts      int `json:"parts"`
	PartsTotal int `json:"partsTotal"`
	// BytesPerSecond over the last 10 seconds; the average of the whole
	// transfer in the last update.
	BytesPerSecond float64 `json:"bytesPerSecond"`
	// ETASeconds is the time left at that rate; absent when unknown or done.
	ETASeconds int64 `json:"etaSeconds,omitempty"`
}

const (
	progressEvery  = 250 * time.Millisecond // at most 4 updates a second
	progressWindow = 10 * time.Second       // the rate is measured over this window
)

// tracker counts the bytes of the parts in flight and of the completed
// ones. Done never goes back: when a part is retried its bytes count again
// only once they pass what was shown.
type tracker struct {
	mu        sync.Mutex
	p         Progress
	inflight  map[int]int64
	completed int64
	start     time.Time
	last      time.Time // of the last update sent
	samples   []sample
	emit      func(Progress)
	now       func() time.Time
}

type sample struct {
	at   time.Time
	done int64
}

func newTracker(p Progress, o Options) *tracker {
	t := &tracker{p: p, inflight: map[int]int64{}, emit: o.OnProgress, now: o.now}
	t.start = t.now()
	return t
}

// begin sends the first update; resumed parts count as already done.
func (t *tracker) begin(doneBytes int64, doneParts int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completed, t.p.Done, t.p.Parts = doneBytes, doneBytes, doneParts
	t.samples = []sample{{t.start, doneBytes}}
	t.last = t.start
	t.emit(t.p)
}

// set records the bytes the current attempt of a part has moved.
func (t *tracker) set(part int, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight[part] = n
	t.update()
}

// partDone moves a part of n bytes to the completed ones.
func (t *tracker) partDone(part int, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inflight, part)
	t.completed += n
	t.p.Parts++
	t.update()
}

// finish sends the last update, with the average rate of the transfer.
func (t *tracker) finish() (elapsed time.Duration, rate float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed = t.now().Sub(t.start)
	moved := t.completed - t.samples[0].done
	if elapsed > 0 {
		rate = float64(moved) / elapsed.Seconds()
	}
	t.p.Done, t.p.BytesPerSecond, t.p.ETASeconds = t.completed, rate, 0
	t.emit(t.p)
	return elapsed, rate
}

func (t *tracker) update() {
	done := t.completed
	for _, n := range t.inflight {
		done += n
	}
	if done > t.p.Done {
		t.p.Done = min(done, t.p.Total)
	}
	now := t.now()
	if now.Sub(t.last) < progressEvery {
		return
	}
	t.last = now
	t.samples = append(t.samples, sample{now, t.p.Done})
	// Keep the samples of the window, and the one just before it.
	for len(t.samples) > 2 && now.Sub(t.samples[1].at) >= progressWindow {
		t.samples = t.samples[1:]
	}
	t.p.BytesPerSecond, t.p.ETASeconds = rateAndETA(t.samples, t.p.Total)
	t.emit(t.p)
}

// rateAndETA measures the rate between the first and the last sample, and
// the time left to reach total at that rate. Under half a second of
// samples the rate is not known yet.
func rateAndETA(samples []sample, total int64) (float64, int64) {
	first, last := samples[0], samples[len(samples)-1]
	span := last.at.Sub(first.at)
	if span < 500*time.Millisecond || last.done <= first.done {
		return 0, 0
	}
	rate := float64(last.done-first.done) / span.Seconds()
	eta := int64(float64(total-last.done)/rate + 0.5)
	return rate, eta
}

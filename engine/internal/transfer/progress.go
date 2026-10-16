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
// only once they pass what was shown. Each attempt of a part reports with
// its own token, so a failed attempt still read by the HTTP transport
// reports nothing.
type tracker struct {
	mu        sync.Mutex
	p         Progress
	inflight  map[int]int64
	attempts  map[int]int // token of the current attempt of each part
	completed int64
	start     time.Time
	last      time.Time // of the last update sent
	samples   []sample
	out       *progressOut
	now       func() time.Time
}

type sample struct {
	at   time.Time
	done int64
}

// newTracker starts the delivery of the updates: stop (or finish) ends it.
func newTracker(p Progress, o Options) *tracker {
	t := &tracker{p: p, inflight: map[int]int64{}, attempts: map[int]int{}, out: newProgressOut(o.OnProgress), now: o.now}
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
	t.out.send(t.p)
}

// attempt starts an attempt of a part and returns the token it reports with.
func (t *tracker) attempt(part int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[part]++
	delete(t.inflight, part)
	return t.attempts[part]
}

// set records the bytes an attempt of a part has moved.
func (t *tracker) set(part, token int, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempts[part] != token {
		return // a finished attempt
	}
	t.inflight[part] = n
	t.update()
}

// drop forgets the bytes of a failed attempt.
func (t *tracker) drop(part int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[part]++
	delete(t.inflight, part)
}

// partDone moves a part of n bytes to the completed ones.
func (t *tracker) partDone(part int, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[part]++
	delete(t.inflight, part)
	t.completed += n
	t.p.Parts++
	t.update()
}

// finish sends the last update, with the average rate of the transfer, and
// returns once the callback has it.
func (t *tracker) finish() (elapsed time.Duration, rate float64) {
	t.mu.Lock()
	elapsed = t.now().Sub(t.start)
	moved := t.completed - t.samples[0].done
	if elapsed > 0 {
		rate = float64(moved) / elapsed.Seconds()
	}
	t.p.Done, t.p.BytesPerSecond, t.p.ETASeconds = t.completed, rate, 0
	t.out.send(t.p)
	t.mu.Unlock()
	t.out.close()
	return elapsed, rate
}

// stop ends the delivery without a last update (a failed transfer).
func (t *tracker) stop() { t.out.close() }

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
	t.out.send(t.p)
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

// progressOut hands the updates to the callback from a goroutine of its own:
// a slow callback (an SSE client on a slow link) never holds up the parts,
// it only misses the updates that a newer one replaced. Calls never overlap,
// and close delivers the last update before it returns.
type progressOut struct {
	mu     sync.Mutex
	next   *Progress
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

func newProgressOut(emit func(Progress)) *progressOut {
	o := &progressOut{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(o.done)
		for range o.wake {
			o.mu.Lock()
			p := o.next
			o.next = nil
			o.mu.Unlock()
			if p != nil {
				emit(*p)
			}
		}
	}()
	return o
}

// send replaces the pending update with p; it never blocks.
func (o *progressOut) send(p Progress) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.next = &p
	select {
	case o.wake <- struct{}{}:
	default: // already woken: p is what it will read
	}
}

// close delivers the pending update and waits for the callback; it can be
// called more than once.
func (o *progressOut) close() {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.wake)
	}
	o.mu.Unlock()
	<-o.done
}

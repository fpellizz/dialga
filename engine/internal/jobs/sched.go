package jobs

import (
	"context"
	"slices"
	"sync"
)

// priority orders the requests for a slot: the oldest job first, then the
// order of its items, so databases go through each resource in the order
// the user listed them.
type priority struct {
	job  int64
	item int
}

func (p priority) less(o priority) bool {
	if p.job != o.job {
		return p.job < o.job
	}
	return p.item < o.item
}

// limiter hands out the slots of the shared resources. A pool is a key (the
// transfer pool, or the RDS tasks of one instance); each request carries
// its own limit, so a pool shared by jobs with different limits never goes
// over the limit of the request it grants.
type limiter struct {
	mu    sync.Mutex
	pools map[string]*pool
}

type pool struct {
	inUse   int
	waiting []*waiter // sorted by priority
}

type waiter struct {
	prio    priority
	limit   int
	granted bool
	ready   chan struct{} // closed when granted
}

func newLimiter() *limiter { return &limiter{pools: map[string]*pool{}} }

// ticket is a request for a slot, queued in priority order.
type ticket struct {
	l *limiter
	p *pool
	w *waiter
}

// enqueue queues a request for a slot of key (fewer than limit in use); it
// may be granted at once. Queuing the first requests of a job in item order,
// before its items start, keeps that order even when slots are free.
func (l *limiter) enqueue(key string, limit int, prio priority) *ticket {
	if limit < 1 {
		limit = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.pools[key]
	if p == nil {
		p = &pool{}
		l.pools[key] = p
	}
	w := &waiter{prio: prio, limit: limit, ready: make(chan struct{})}
	i, _ := slices.BinarySearchFunc(p.waiting, w, func(a, b *waiter) int {
		if a.prio.less(b.prio) {
			return -1
		}
		return 1 // equal priorities: first come, first served
	})
	p.waiting = slices.Insert(p.waiting, i, w)
	p.grant()
	return &ticket{l: l, p: p, w: w}
}

// acquire is enqueue then wait.
func (l *limiter) acquire(ctx context.Context, key string, limit int, prio priority, onWait func()) (release func(), err error) {
	return l.enqueue(key, limit, prio).wait(ctx, onWait)
}

// wait blocks until the slot is granted or ctx ends. onWait runs once,
// before blocking, when the slot is not granted yet.
func (t *ticket) wait(ctx context.Context, onWait func()) (release func(), err error) {
	l, p, w := t.l, t.p, t.w
	l.mu.Lock()
	granted := w.granted
	l.mu.Unlock()
	if !granted {
		if onWait != nil {
			onWait()
		}
		select {
		case <-w.ready:
		case <-ctx.Done():
			l.mu.Lock()
			if w.granted { // granted while ctx ended: give it back
				p.inUse--
			} else {
				p.waiting = slices.DeleteFunc(p.waiting, func(x *waiter) bool { return x == w })
			}
			p.grant()
			l.mu.Unlock()
			return nil, context.Cause(ctx)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			p.inUse--
			p.grant()
			l.mu.Unlock()
		})
	}, nil
}

// grant gives free slots to the waiters, in priority order (l.mu held).
func (p *pool) grant() {
	for i := 0; i < len(p.waiting); {
		w := p.waiting[i]
		if p.inUse < w.limit {
			p.inUse++
			w.granted = true
			close(w.ready)
			p.waiting = slices.Delete(p.waiting, i, i+1)
			continue
		}
		i++
	}
}

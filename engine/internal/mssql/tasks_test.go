package mssql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedReader answers Get with the next scripted result.
type scriptedReader struct {
	results []result
	calls   int
}

type result struct {
	task Task
	err  error
}

func (r *scriptedReader) List(context.Context, TaskFilter) ([]Task, error) { return nil, nil }

func (r *scriptedReader) Get(_ context.Context, id int) (Task, error) {
	res := r.results[min(r.calls, len(r.results)-1)]
	r.calls++
	res.task.ID = id
	return res.task, res.err
}

func task(l Lifecycle, pct int) result { return result{task: Task{Lifecycle: l, Percent: pct}} }

func TestLifecycleDone(t *testing.T) {
	for l, want := range map[Lifecycle]bool{
		Created: false, InProgress: false, CancelRequested: false,
		Success: true, Error: true, Cancelled: true,
	} {
		if l.Done() != want {
			t.Errorf("%s.Done() = %v, want %v", l, !want, want)
		}
	}
}

func TestWaitReportsChangesUntilDone(t *testing.T) {
	r := &scriptedReader{results: []result{
		task(Created, 0), task(Created, 0), task(InProgress, 40), task(InProgress, 40),
		task(InProgress, 90), task(Success, 100),
	}}
	var seen []string
	got, err := Wait(context.Background(), r, 7, WaitOptions{
		Interval: time.Millisecond,
		OnChange: func(t Task) { seen = append(seen, string(t.Lifecycle)) },
	})
	if err != nil || got.Lifecycle != Success || got.ID != 7 {
		t.Fatalf("Wait() = %+v, %v", got, err)
	}
	if want := "CREATED IN_PROGRESS IN_PROGRESS SUCCESS"; strings.Join(seen, " ") != want {
		t.Fatalf("changes %q, want %q", strings.Join(seen, " "), want)
	}
}

func TestWaitReturnsFailedTasks(t *testing.T) {
	r := &scriptedReader{results: []result{task(InProgress, 10), task(Error, 10)}}
	got, err := Wait(context.Background(), r, 3, WaitOptions{Interval: time.Millisecond})
	if err != nil || got.Lifecycle != Error {
		t.Fatalf("Wait() = %+v, %v", got, err)
	}
}

func TestWaitRetriesTransientErrors(t *testing.T) {
	flaky := errors.New("connection reset")
	r := &scriptedReader{results: []result{
		task(InProgress, 10), {err: flaky}, {err: flaky}, task(Success, 100),
	}}
	retries := 0
	got, err := Wait(context.Background(), r, 1, WaitOptions{
		Interval: time.Millisecond, MaxFailures: 3, OnRetry: func(error) { retries++ },
	})
	if err != nil || got.Lifecycle != Success || retries != 2 {
		t.Fatalf("Wait() = %+v, %v, retries %d", got, err, retries)
	}
}

func TestWaitGivesUpAfterMaxFailures(t *testing.T) {
	flaky := errors.New("connection reset")
	r := &scriptedReader{results: []result{{err: flaky}}}
	_, err := Wait(context.Background(), r, 1, WaitOptions{Interval: time.Millisecond, MaxFailures: 3})
	if !errors.Is(err, flaky) || r.calls != 3 {
		t.Fatalf("err %v after %d calls", err, r.calls)
	}
}

func TestWaitStopsOnNotFound(t *testing.T) {
	r := &scriptedReader{results: []result{{err: ErrTaskNotFound}}}
	_, err := Wait(context.Background(), r, 99, WaitOptions{Interval: time.Millisecond})
	if !errors.Is(err, ErrTaskNotFound) || r.calls != 1 {
		t.Fatalf("err %v after %d calls", err, r.calls)
	}
}

func TestWaitStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &scriptedReader{results: []result{task(InProgress, 10)}}
	_, err := Wait(ctx, r, 1, WaitOptions{
		Interval: time.Hour,
		OnChange: func(Task) { cancel() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
}

func TestAddr(t *testing.T) {
	if got := Addr(false, 11433, "db.example.rds.amazonaws.com", 1433); got != "127.0.0.1:11433" {
		t.Fatalf("tunnel addr %q", got)
	}
	if got := Addr(true, 0, "db.example.rds.amazonaws.com", 1433); got != "db.example.rds.amazonaws.com:1433" {
		t.Fatalf("direct addr %q", got)
	}
}

type closeCounter struct{ n int }

func (c *closeCounter) Close() error {
	c.n++
	return nil
}

func TestOpenWithClosesTheResourceWithThePool(t *testing.T) {
	c := &closeCounter{}
	db, err := OpenWith("127.0.0.1:1", "db.example.rds.amazonaws.com", "admin", "pw", c)
	if err != nil {
		t.Fatal(err)
	}
	if c.n != 0 {
		t.Fatal("closed before the pool")
	}
	if err := db.Close(); err != nil || c.n != 1 {
		t.Fatalf("db.Close() = %v, resource closed %d times", err, c.n)
	}
}

// The engine never names the duration column: its spelling differs between
// the docs and RDS ("duration(mins)"), and a wrong one fails the query.
func TestTaskColumnsSkipDuration(t *testing.T) {
	if strings.Contains(strings.ToLower(taskColumns), "duration") {
		t.Fatalf("taskColumns reads the duration column: %s", taskColumns)
	}
}

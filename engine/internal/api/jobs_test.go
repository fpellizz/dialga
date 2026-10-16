package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

func newJobsServer(t *testing.T, m *jobs.Manager) http.Handler {
	t.Helper()
	s, err := New(Options{Token: testToken, AllowedOrigins: DefaultOrigins, Version: "test", Jobs: m})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

// gatedJob starts a one-database job whose step logs "first", then waits
// for gate (or its context).
func gatedJob(t *testing.T, m *jobs.Manager, gate <-chan struct{}) jobs.Job {
	t.Helper()
	job, err := m.Start(jobs.Spec{Kind: jobs.Import, Target: "dev-sql", Instance: "dev-sqlserver", Items: []jobs.ItemSpec{{
		Name: "sales",
		Steps: []jobs.Step{jobs.NewStep("upload", "Upload sales.bak", jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
			r.Log(jobs.Info, "first")
			select {
			case <-gate:
				r.Log(jobs.Info, "second")
				return "uploaded", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func hasEvent(m *jobs.Manager, id string, typ jobs.EventType) bool {
	job, err := m.Get(id)
	if err != nil {
		return false
	}
	for _, ev := range job.Events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func endJob(t *testing.T, m *jobs.Manager, id string) jobs.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestListAndGetJobs(t *testing.T) {
	m := jobs.New(jobs.Options{})
	h := newJobsServer(t, m)
	var empty JobList
	if rec := do(h, http.MethodGet, "/api/v1/jobs"); rec.Code != http.StatusOK ||
		json.Unmarshal(rec.Body.Bytes(), &empty) != nil || empty.Jobs == nil || len(empty.Jobs) != 0 {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}

	gate := make(chan struct{})
	job := gatedJob(t, m, gate)
	waitFor(t, "the step", func() bool { return hasEvent(m, job.ID, jobs.EventLog) })

	var list JobList
	rec := do(h, http.MethodGet, "/api/v1/jobs")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID || list.Jobs[0].State != jobs.Running || list.Jobs[0].Databases[0] != "sales" {
		t.Fatalf("list %+v", list)
	}
	if strings.Contains(rec.Body.String(), `"events"`) || strings.Contains(rec.Body.String(), `"steps"`) {
		t.Fatal("the list carries steps or events")
	}

	var got jobs.Job
	rec = do(h, http.MethodGet, "/api/v1/jobs/"+job.ID)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if got.ID != job.ID || got.Items[0].Steps[0].Status != jobs.StepRunning || len(got.Events) < 3 {
		t.Fatalf("job %+v", got)
	}
	close(gate)
	endJob(t, m, job.ID)

	for _, path := range []string{"/api/v1/jobs/20260101T000000Z-00000000", "/api/v1/jobs/..%2Fetc", "/api/v1/jobs/x"} {
		if rec := do(h, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}

func TestCancelJob(t *testing.T) {
	m := jobs.New(jobs.Options{})
	h := newJobsServer(t, m)
	job := gatedJob(t, m, make(chan struct{}))
	waitFor(t, "the step", func() bool { return hasEvent(m, job.ID, jobs.EventLog) })

	rec := do(h, http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancel")
	var sum jobs.Summary
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &sum) != nil || !sum.CancelRequested || sum.ID != job.ID {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	if end := endJob(t, m, job.ID); end.State != jobs.Cancelled {
		t.Fatalf("job %s after cancel", end.State)
	}
	if rec := do(h, http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancel"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cancelled") {
		t.Fatalf("cancel an ended job: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/api/v1/jobs/20260101T000000Z-00000000/cancel"); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/v1/jobs/"+job.ID+"/cancel"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET cancel: %d", rec.Code)
	}
}

type sseEvent struct {
	id, event string
	data      jobs.Event
}

// readSSE reads one event, skipping comment lines; io.EOF at the end.
func readSSE(t *testing.T, r *bufio.Reader) (sseEvent, error) {
	t.Helper()
	var ev sseEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return ev, err
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "" && ev.event != "":
			return ev, nil
		case strings.HasPrefix(line, "id: "):
			ev.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			ev.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.data); err != nil {
				t.Fatalf("data %q: %v", line, err)
			}
		}
	}
}

func streamEvents(t *testing.T, base, path string, header map[string]string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body)
}

func TestJobEventsReplayThenLive(t *testing.T) {
	m := jobs.New(jobs.Options{})
	srv := httptest.NewServer(newJobsServer(t, m))
	defer srv.Close()
	gate := make(chan struct{})
	job := gatedJob(t, m, gate)
	waitFor(t, "the first log line", func() bool { return hasEvent(m, job.ID, jobs.EventLog) })

	resp, body := streamEvents(t, srv.URL, "/api/v1/jobs/"+job.ID+"/events", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var got []sseEvent
	for len(got) < 4 { // replay: queued, started, step.started, log "first"
		ev, err := readSSE(t, body)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if got[3].data.Message != "first" {
		t.Fatalf("replay %+v", got)
	}
	close(gate) // live from here
	for {
		ev, err := readSSE(t, body)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	want := []jobs.EventType{jobs.EventJobQueued, jobs.EventJobStarted, jobs.EventStepStarted, jobs.EventLog,
		jobs.EventLog, jobs.EventStepSucceeded, jobs.EventJobSucceeded}
	if len(got) != len(want) {
		t.Fatalf("%d events, want %d: %+v", len(got), len(want), got)
	}
	for i, ev := range got {
		if ev.event != string(want[i]) || ev.data.Type != want[i] || ev.id != strconv.FormatInt(int64(i+1), 10) || ev.data.Seq != int64(i+1) {
			t.Fatalf("event %d: %+v, want %s with id %d", i, ev, want[i], i+1)
		}
	}
}

func TestJobEventsAfter(t *testing.T) {
	m := jobs.New(jobs.Options{})
	srv := httptest.NewServer(newJobsServer(t, m))
	defer srv.Close()
	gate := make(chan struct{})
	close(gate)
	job := gatedJob(t, m, gate)
	end := endJob(t, m, job.ID)
	last := end.Events[len(end.Events)-1].Seq

	tests := []struct {
		name   string
		path   string
		header map[string]string
		first  int64
	}{
		{"everything", "", nil, 1},
		{"Last-Event-ID", "", map[string]string{"Last-Event-ID": "4"}, 5},
		{"after", "?after=5", nil, 6},
		{"header wins", "?after=1", map[string]string{"Last-Event-ID": "6"}, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, body := streamEvents(t, srv.URL, "/api/v1/jobs/"+job.ID+"/events"+tc.path, tc.header)
			var seqs []int64
			for {
				ev, err := readSSE(t, body)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				seqs = append(seqs, ev.data.Seq)
			}
			if len(seqs) == 0 || seqs[0] != tc.first || seqs[len(seqs)-1] != last {
				t.Fatalf("seqs %v, want %d..%d", seqs, tc.first, last)
			}
		})
	}
	for _, bad := range []string{"?after=-1", "?after=x"} {
		resp, _ := streamEvents(t, srv.URL, "/api/v1/jobs/"+job.ID+"/events"+bad, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, resp.StatusCode)
		}
	}
	if resp, _ := streamEvents(t, srv.URL, "/api/v1/jobs/20260101T000000Z-00000000/events", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown job: %d", resp.StatusCode)
	}
}

func TestJobEventsKeepAlive(t *testing.T) {
	saved := sseKeepAlive
	sseKeepAlive = 10 * time.Millisecond
	defer func() { sseKeepAlive = saved }()
	m := jobs.New(jobs.Options{})
	srv := httptest.NewServer(newJobsServer(t, m))
	defer srv.Close()
	gate := make(chan struct{})
	job := gatedJob(t, m, gate)
	defer func() {
		close(gate)
		endJob(t, m, job.ID)
	}()
	_, body := streamEvents(t, srv.URL, "/api/v1/jobs/"+job.ID+"/events", nil)
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == ": keep-alive\n" {
			return
		}
	}
}

func TestJobsDefaultManager(t *testing.T) {
	doc := testConfig
	h, _ := newTestServer(t, &doc, nil) // no Jobs option
	var list JobList
	if rec := do(h, http.MethodGet, "/api/v1/jobs"); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Jobs) != 0 {
		t.Fatalf("default manager: %d %s", rec.Code, rec.Body)
	}
}

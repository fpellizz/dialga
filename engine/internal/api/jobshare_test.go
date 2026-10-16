package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/session"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
	"github.com/decisyon/dialga/engine/internal/tunnel"
	"github.com/decisyon/dialga/engine/internal/tunnels"
)

type sharedTunnelStub struct {
	mu    sync.Mutex
	opens int
}

func (s *sharedTunnelStub) open(context.Context, config.Target, func(tunnel.State)) (tunnels.Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	return stubSharedTunnel{}, nil
}

type stubSharedTunnel struct{}

func (stubSharedTunnel) Addr() string { return "127.0.0.1:41998" }
func (stubSharedTunnel) Close() error { return nil }

// endingInstance ends the native tasks as end says: Error fails the job,
// InProgress never ends them (the job is cancelled while it waits).
type endingInstance struct {
	*jobInstance
	end mssql.Lifecycle
}

func (e endingInstance) Get(ctx context.Context, id int) (mssql.Task, error) {
	t, err := e.jobInstance.Get(ctx, id)
	if err == nil && e.end != "" {
		t.Lifecycle = e.end
	}
	return t, err
}

// sharedJob is a server over shared tunnels, in tunnel mode, whose sessions
// borrow a lease of the shared tunnel.
type sharedJob struct {
	srv      *Server
	manager  *jobs.Manager
	shared   *tunnels.Manager
	stub     *sharedTunnelStub
	target   config.Target
	mu       sync.Mutex
	sessions int
}

func newSharedJob(t *testing.T, end mssql.Lifecycle) *sharedJob {
	t.Helper()
	doc := strings.Replace(testConfig, "mode: external, local_port: 11433", "mode: tunnel", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	j := &sharedJob{stub: &sharedTunnelStub{}}
	stub := j.stub
	shared := tunnels.New(tunnels.Options{Open: stub.open})
	t.Cleanup(func() { _ = shared.Close() })
	rds := &jobInstance{states: map[string]string{}, tasks: map[int]*mssql.Task{}}
	s3 := transfertest.New()
	// The default OpenOps of the server, with the AWS part faked.
	orig := opsOpenWith
	t.Cleanup(func() { opsOpenWith = orig })
	opsOpenWith = func(s session.Sessions) func(context.Context, string, config.Target, string) (*ops.Session, func(), error) {
		return func(ctx context.Context, name string, tgt config.Target, _ string) (*ops.Session, func(), error) {
			lease, err := s.Tunnels.Acquire(ctx, tgt)
			if err != nil {
				return nil, nil, err
			}
			j.mu.Lock()
			j.sessions++
			j.mu.Unlock()
			return &ops.Session{Name: name, Target: tgt, SQL: endingInstance{rds, end}, Bucket: jobBucket{s3}}, lease.Release, nil
		}
	}
	manager := jobs.New(jobs.Options{})
	j.manager, j.shared = manager, shared
	srv, err := New(Options{
		Token: testToken, ConfigPath: path, Jobs: manager, Tunnels: shared, TaskInterval: time.Millisecond,
		Getenv: func(k string) string { return testEnv[k] },
		OpenS3: func(context.Context, config.Target) (transfer.Client, error) { return s3, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	j.srv = srv
	cfg, _ := config.Parse([]byte(doc))
	j.target = cfg.Targets["dev-sql"]
	return j
}

// start posts an import job of three databases and returns its id.
func (j *sharedJob) start(t *testing.T) string {
	t.Helper()
	var items []string
	dir := t.TempDir()
	for _, db := range []string{"a", "b", "c"} {
		file := filepath.Join(dir, db+".bak")
		if err := os.WriteFile(file, []byte(db), 0o600); err != nil {
			t.Fatal(err)
		}
		items = append(items, `{"file":`+jsonString(file)+`,"database":"`+db+`"}`)
	}
	rec := doJSON(j.srv.Handler(), http.MethodPost, "/api/v1/jobs", `{"kind":"import","target":"dev-sql","items":[`+strings.Join(items, ",")+`]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/api/v1/jobs/")
	return id
}

func TestJobStepsBorrowTheSharedTunnel(t *testing.T) {
	j := newSharedJob(t, "")
	job := j.wait(t, j.start(t))
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s", job.State)
	}
	j.expectReleased(t, 10)
}

func TestFailedJobReleasesTheSharedTunnel(t *testing.T) {
	j := newSharedJob(t, mssql.Error)
	if job := j.wait(t, j.start(t)); job.State != jobs.Failed {
		t.Fatalf("job %s", job.State)
	}
	j.expectReleased(t, 0)
}

func TestCancelledJobReleasesTheSharedTunnel(t *testing.T) {
	j := newSharedJob(t, mssql.InProgress)
	id := j.start(t)
	deadline := time.Now().Add(5 * time.Second)
	for { // wait until a step runs on the tunnel
		j.mu.Lock()
		n := j.sessions
		j.mu.Unlock()
		if n > 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := j.manager.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if job := j.wait(t, id); job.State != jobs.Cancelled {
		t.Fatalf("job %s", job.State)
	}
	j.expectReleased(t, 0)
}

func (j *sharedJob) wait(t *testing.T, id string) jobs.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := j.manager.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// expectReleased checks one tunnel was opened, no lease is still out and the
// tunnel stays up (it is the engine's); sessions > 0 is the exact count.
func (j *sharedJob) expectReleased(t *testing.T, sessions int) {
	t.Helper()
	st := j.shared.State(j.target)
	j.mu.Lock()
	n := j.sessions
	j.mu.Unlock()
	if j.stub.opens != 1 || st.Users != 0 || st.Status != tunnel.Ready || (sessions > 0 && n != sessions) {
		t.Fatalf("%d tunnel(s), %d sessions, state %+v", j.stub.opens, n, st)
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

// interruptedJob leaves in a journal one import on dev-sql that the engine
// stopped mid-upload, and opens it again with reg.
func interruptedJob(t *testing.T, reg *jobs.Registry) (*jobs.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	first, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	upload := jobs.NewStep("upload", "Upload sales.bak", jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	job, err := first.Start(jobs.Spec{Kind: jobs.Import, Target: "dev-sql", Instance: "dev-sqlserver", InstanceKey: "123456789012/eu-west-1/dev-sqlserver", Items: []jobs.ItemSpec{{
		Name: "sales", Steps: []jobs.Step{jobs.WithKind(upload, "upload", map[string]string{"key": "import/sales.bak"})},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the upload", func() bool { return hasEvent(first, job.ID, jobs.EventStepStarted) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := jobs.Open(dir, jobs.Options{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m, job.ID
}

// uploadFactory rebuilds the upload as a step that succeeds, recording the env.
type uploadFactory struct {
	mu  sync.Mutex
	env any
}

func (f *uploadFactory) registry() *jobs.Registry {
	reg := jobs.NewRegistry()
	reg.Register("upload", func(rb jobs.Rebuild) (jobs.Step, error) {
		f.mu.Lock()
		f.env = rb.Env
		f.mu.Unlock()
		return jobs.NewStep(rb.Step.ID, rb.Step.Title, jobs.ResourceTransfer,
			func(context.Context, jobs.Reporter) (string, error) { return "resumed", nil }), nil
	})
	return reg
}

type resumeEnv struct {
	name, password string
	calls          int
	err            error
}

func (e *resumeEnv) build(_ context.Context, name string, _ config.Target, password string) (any, error) {
	e.calls++
	e.name, e.password = name, password
	if e.err != nil {
		return nil, e.err
	}
	return "env of " + name, nil
}

func resumeServer(t *testing.T, m *jobs.Manager, doc *string, env map[string]string, jobEnv *resumeEnv) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if doc != nil {
		if err := os.WriteFile(path, []byte(*doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Token: testToken, ConfigPath: path, Getenv: func(k string) string { return env[k] }, Jobs: m}
	if jobEnv != nil {
		opts.JobEnv = jobEnv.build
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func postBody(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var resumePassword = map[string]string{"TEST_SQL_PASSWORD": "s3cret"}

func TestResumeJob(t *testing.T) {
	f := &uploadFactory{}
	m, id := interruptedJob(t, f.registry())
	doc := testConfig
	env := &resumeEnv{}
	h := resumeServer(t, m, &doc, resumePassword, env)

	rec := postBody(h, "/api/v1/jobs/"+id+"/resume", "")
	var snap jobs.Job
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &snap) != nil {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body)
	}
	if snap.ID != id || snap.State != jobs.Queued || snap.Events[len(snap.Events)-1].Type != jobs.EventJobResumed {
		t.Fatalf("snapshot %s, last event %s", snap.State, snap.Events[len(snap.Events)-1].Type)
	}
	if env.name != "dev-sql" || env.password != "s3cret" {
		t.Fatalf("env built for %q with %q", env.name, env.password)
	}
	end := endJob(t, m, id)
	f.mu.Lock()
	gotEnv := f.env
	f.mu.Unlock()
	if end.State != jobs.Succeeded || end.Items[0].Steps[0].Detail != "resumed" || gotEnv != "env of dev-sql" {
		t.Fatalf("job %s, env %v", end.State, gotEnv)
	}
	if rec := postBody(h, "/api/v1/jobs/"+id+"/resume", ""); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "is succeeded") {
		t.Fatalf("resume again: %d %s", rec.Code, rec.Body)
	}
}

func TestResumeProtectedTargetAsksTheName(t *testing.T) {
	f := &uploadFactory{}
	m, id := interruptedJob(t, f.registry())
	doc := strings.Replace(testConfig, "    bucket:", "    protected: true\n    bucket:", 1)
	env := &resumeEnv{}
	h := resumeServer(t, m, &doc, resumePassword, env)
	for _, body := range []string{"", `{"confirm":"prod"}`} {
		rec := postBody(h, "/api/v1/jobs/"+id+"/resume", body)
		if rec.Code != http.StatusUnprocessableEntity ||
			rec.Body.String() != "not confirmed: this import asks to retype \"dev-sql\" in confirm\n" {
			t.Fatalf("body %q: %d %q", body, rec.Code, rec.Body)
		}
	}
	if env.calls != 0 {
		t.Fatal("the environment was built before the confirmation")
	}
	if rec := postBody(h, "/api/v1/jobs/"+id+"/resume", `{"confirm":"dev-sql"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("confirmed: %d %s", rec.Code, rec.Body)
	}
	endJob(t, m, id)
}

func TestResumeJobErrors(t *testing.T) {
	f := &uploadFactory{}
	m, id := interruptedJob(t, f.registry())
	bare, bareID := interruptedJob(t, jobs.NewRegistry()) // no factory for "upload"
	doc := testConfig
	otherTarget := strings.Replace(testConfig, "dev-sql:", "lab-sql:", 1)
	otherAccount := strings.Replace(testConfig, "123456789012", "999999999999", 1)
	otherRegion := strings.Replace(testConfig, "eu-west-1", "us-east-1", 1)
	otherInstance := strings.Replace(testConfig, "instance: dev-sqlserver", "instance: other-sqlserver", 1)
	path := "/api/v1/jobs/" + id + "/resume"
	tests := []struct {
		name   string
		m      *jobs.Manager
		path   string
		doc    *string
		env    map[string]string
		jobEnv *resumeEnv
		body   string
		status int
		want   string
	}{
		{"unknown job", m, "/api/v1/jobs/20200101T000000Z-00000000/resume", &doc, resumePassword, &resumeEnv{}, "", 404, "job not found"},
		{"bad body", m, path, &doc, resumePassword, &resumeEnv{}, "{", 400, "invalid body"},
		{"unknown field", m, path, &doc, resumePassword, &resumeEnv{}, `{"force":true}`, 400, "unknown field"},
		{"no configuration", m, path, nil, resumePassword, &resumeEnv{}, "", 409, noSuchFile},
		{"target gone", m, path, &otherTarget, resumePassword, &resumeEnv{}, "", 422, "target dev-sql of this job is not in the configuration"},
		{"other instance", m, path, &otherInstance, resumePassword, &resumeEnv{}, "", 422, "now points at instance other-sqlserver, the job ran on dev-sqlserver"},
		{"other account", m, path, &otherAccount, resumePassword, &resumeEnv{}, "", 422, "the job ran on 123456789012/eu-west-1/dev-sqlserver, the target is on 999999999999/"},
		{"other region", m, path, &otherRegion, resumePassword, &resumeEnv{}, "", 422, "the target is on 123456789012/us-east-1/"},
		{"no password", m, path, &doc, nil, &resumeEnv{}, "", 422, "SQL password"},
		{"env fails", m, path, &doc, resumePassword, &resumeEnv{err: errors.New("AWS: no credentials")}, "", 502, "AWS: no credentials"},
		{"not resumable", bare, "/api/v1/jobs/" + bareID + "/resume", &doc, resumePassword, &resumeEnv{}, "", 422, `no factory rebuilds steps of kind "upload"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := resumeServer(t, tc.m, tc.doc, tc.env, tc.jobEnv)
			rec := postBody(h, tc.path, tc.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%d %q, want %d with %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
	// None of them changed the job.
	if j, _ := m.Get(id); j.State != jobs.Interrupted {
		t.Fatalf("job %s after the refusals", j.State)
	}
}

// An import the engine stopped while it followed the restore resumes through
// the API with the pipeline's own environment: no second restore is queued.
func TestResumeImportJobWithThePipeline(t *testing.T) {
	doc := testConfig
	dir := t.TempDir()
	m1, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s3 := transfertest.New()
	rds := &jobInstance{states: map[string]string{}, tasks: map[int]*mssql.Task{}, hold: make(chan struct{})}
	js := newJobServerOn(t, &doc, testEnv, m1, s3, rds)
	rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", importBody(t, ""))
	var job jobs.Job
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &job) != nil {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "the restore task", func() bool { return hasEvent(m1, job.ID, jobs.EventTaskStatus) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m1.Close(ctx); err != nil {
		t.Fatal(err)
	}

	m2, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m2.Close(context.Background()) })
	if got, _ := m2.Get(job.ID); got.State != jobs.Interrupted {
		t.Fatalf("job %s after the restart", got.State)
	}
	rds.hold = nil
	js = newJobServerOn(t, &doc, testEnv, m2, s3, rds)
	rec = doJSON(js.h, http.MethodPost, "/api/v1/jobs/"+job.ID+"/resume", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body)
	}
	end := endJob(t, m2, job.ID)
	if end.State != jobs.Succeeded || len(rds.tasks) != 1 || rds.states["sales"] != "ONLINE" {
		t.Fatalf("job %s, %d task(s), sales %q: %+v", end.State, len(rds.tasks), rds.states["sales"], end.Items)
	}
}

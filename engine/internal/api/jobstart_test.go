package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

// jobInstance is an RDS instance in memory whose native tasks succeed on
// their second read; a restore brings its database online.
type jobInstance struct {
	mu     sync.Mutex
	states map[string]string
	tasks  map[int]*mssql.Task
	// hold, when set, blocks the reads of a native task until it is
	// closed or the context ends.
	hold chan struct{}
}

func (f *jobInstance) DatabaseState(_ context.Context, db string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[db], nil
}

func (f *jobInstance) ActiveTasks(context.Context, string) (int, error) { return 0, nil }

func (f *jobInstance) StartTask(_ context.Context, _, db string) (mssql.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &mssql.Task{ID: len(f.tasks) + 1, Type: "RESTORE_DB", Database: db, Lifecycle: mssql.Created}
	f.tasks[t.ID] = t
	return *t, nil
}

func (f *jobInstance) Exec(context.Context, string) error { return nil }

func (f *jobInstance) DatabaseReport(_ context.Context, db string) (mssql.DatabaseReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: db, State: f.states[db]}, Access: true}, nil
}

func (f *jobInstance) List(context.Context, mssql.TaskFilter) ([]mssql.Task, error) { return nil, nil }

func (f *jobInstance) Get(ctx context.Context, id int) (mssql.Task, error) {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return mssql.Task{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tasks[id]
	if t.Lifecycle == mssql.InProgress {
		t.Lifecycle, t.Percent = mssql.Success, 100
		f.states[t.Database] = "ONLINE"
	} else if !t.Lifecycle.Done() {
		t.Lifecycle = mssql.InProgress
	}
	return *t, nil
}

type jobBucket struct{ s3 *transfertest.S3 }

func (b jobBucket) Size(_ context.Context, key string) (int64, error) {
	if o := b.s3.Get("dev-sqlserver-backups", key); o != nil {
		return int64(len(o.Data)), nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b jobBucket) Keys(context.Context, string) ([]string, error) { return nil, nil }

type jobServer struct {
	h       http.Handler
	jobs    *jobs.Manager
	s3      *transfertest.S3
	rds     *jobInstance
	s3Err   error
	opsOpen int
}

func newJobServer(t *testing.T, configDoc *string, env map[string]string) *jobServer {
	t.Helper()
	return newJobServerOn(t, configDoc, env, jobs.New(jobs.Options{}),
		transfertest.New(), &jobInstance{states: map[string]string{}, tasks: map[int]*mssql.Task{}})
}

// newJobServerOn is newJobServer over a manager, an S3 and an instance the
// caller keeps (a restart of the engine keeps the last two).
func newJobServerOn(t *testing.T, configDoc *string, env map[string]string, m *jobs.Manager, s3 *transfertest.S3, rds *jobInstance) *jobServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	js := &jobServer{jobs: m, s3: s3, rds: rds}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path, Jobs: js.jobs,
		Getenv: func(k string) string { return env[k] }, TaskInterval: time.Millisecond,
		OpenS3: func(context.Context, config.Target) (transfer.Client, error) { return js.s3, js.s3Err },
		OpenOps: func(_ context.Context, name string, tgt config.Target, _ string) (*ops.Session, func(), error) {
			js.opsOpen++
			return &ops.Session{Name: name, Target: tgt, SQL: js.rds, Bucket: jobBucket{js.s3}}, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	js.h = s.Handler()
	return js
}

func importBody(t *testing.T, extra string) string {
	t.Helper()
	return `{"kind":"import","target":"dev-sql","items":[{"file":` + jsonString(backupFile(t, "backup")) + `,"database":"sales"}]` + extra + `}`
}

func TestStartJobDryRunChangesNothing(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", importBody(t, `,"dryRun":true`))
	var plan pipeline.Plan
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &plan) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if !plan.Ready || len(plan.Items) != 1 || plan.Items[0].Key != "import/sales.bak" || len(plan.Items[0].Actions) != 4 ||
		len(plan.Items[0].Uploads) != 1 || len(plan.Digest) != 32 || !strings.HasPrefix(plan.Preview, "Dry run: import of 1 database(s)") {
		t.Fatalf("plan %s", rec.Body)
	}
	if js.s3.Calls("CreateMultipartUpload") != 0 || len(js.jobs.List()) != 0 {
		t.Fatal("a dry run changed something")
	}
}

func TestStartJobRunsInTheEngine(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	file := jsonString(backupFile(t, "backup"))
	body := func(extra string) string {
		return `{"kind":"import","target":"dev-sql","items":[{"file":` + file + `,"database":"sales"}]` + extra + `}`
	}
	preview := doJSON(js.h, http.MethodPost, "/api/v1/jobs", body(`,"dryRun":true`))
	var plan pipeline.Plan
	_ = json.Unmarshal(preview.Body.Bytes(), &plan)
	rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", body(`,"digest":"`+plan.Digest+`"`))
	var job jobs.Job
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &job) != nil ||
		rec.Header().Get("Location") != "/api/v1/jobs/"+job.ID || job.Kind != jobs.Import || len(job.Items) != 1 {
		t.Fatalf("status %d, location %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := js.jobs.Wait(ctx, job.ID)
	if err != nil || done.State != jobs.Succeeded || js.s3.Get("dev-sqlserver-backups", "import/sales.bak") == nil {
		t.Fatalf("job %s, %v: %+v", done.State, err, done.Items)
	}
}

func TestStartJobErrors(t *testing.T) {
	doc := testConfig
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	tests := []struct {
		name  string
		doc   *string
		env   map[string]string
		s3Err error
		setup func(*jobServer)
		body  string
		want  int
	}{
		{"not JSON", &doc, testEnv, nil, nil, `{`, http.StatusBadRequest},
		{"unknown field", &doc, testEnv, nil, nil, `{"kind":"import","target":"dev-sql","items":[],"bucket":"x"}`, http.StatusBadRequest},
		{"unknown kind", &doc, testEnv, nil, nil, `{"kind":"copy","target":"dev-sql","items":[{"database":"a"}]}`, http.StatusBadRequest},
		{"relative file", &doc, testEnv, nil, nil, `{"kind":"import","target":"dev-sql","items":[{"file":"a.bak","database":"a"}]}`, http.StatusBadRequest},
		{"file in an export", &doc, testEnv, nil, nil, `{"kind":"export","target":"dev-sql","items":[{"file":"/a.bak","database":"a"}]}`, http.StatusBadRequest},
		{"bad upload key", &doc, testEnv, nil, nil, "", http.StatusBadRequest},
		{"unknown target", &doc, testEnv, nil, nil, `{"kind":"import","target":"nope","items":[{"file":"/a.bak","database":"a"}]}`, http.StatusNotFound},
		{"missing config", nil, testEnv, nil, nil, `{"kind":"import","target":"dev-sql","items":[{"file":"/a.bak","database":"a"}]}`, http.StatusConflict},
		{"no password", &doc, nil, nil, nil, "", http.StatusUnprocessableEntity},
		{"AWS fails", &doc, testEnv, errors.New("no credentials"), nil, "", http.StatusBadGateway},
		{"pre-flight fails", &doc, testEnv, nil, func(js *jobServer) { js.rds.states["sales"] = "ONLINE" }, "", http.StatusPreconditionFailed},
		{"digest changed", &doc, testEnv, nil, nil, `,"digest":"0123"`, http.StatusPreconditionFailed},
		{"protected, no confirm", &protected, testEnv, nil, nil, "", http.StatusUnprocessableEntity},
		{"engine stopping", &doc, testEnv, nil, func(js *jobServer) { _ = js.jobs.Close(context.Background()) }, "", http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			js := newJobServer(t, tc.doc, tc.env)
			js.s3Err = tc.s3Err
			if tc.setup != nil {
				tc.setup(js)
			}
			body := tc.body
			switch {
			case tc.name == "bad upload key":
				body = `{"kind":"import","target":"dev-sql","items":[{"file":` + jsonString(backupFile(t, "x")) + `,"database":"a","key":"it's.bak"}]}`
			case body == "" || strings.HasPrefix(body, ","):
				body = importBody(t, body)
			}
			rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", body)
			if rec.Code != tc.want || js.s3.Calls("CreateMultipartUpload") != 0 {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusPreconditionFailed {
				var refused JobRefused
				if json.Unmarshal(rec.Body.Bytes(), &refused) != nil || refused.Reason == "" || len(refused.Plan.Items) != 1 {
					t.Fatalf("412 body %s", rec.Body)
				}
			}
		})
	}
	js := newJobServer(t, &protected, testEnv)
	if rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", importBody(t, `,"confirm":"dev-sql"`)); rec.Code != http.StatusCreated {
		t.Fatalf("confirmed: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestStartJobSkipsFailingItems(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.rds.states["hr"] = "ONLINE"
	body := `{"kind":"import","target":"dev-sql","skipFailing":true,"items":[` +
		`{"file":` + jsonString(backupFile(t, "s")) + `,"database":"sales"},` +
		`{"files":[` + jsonString(filepath.Join(t.TempDir(), "hr.bak")) + `],"database":"hr"}]}`
	rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs", body)
	var job jobs.Job
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &job) != nil || len(job.Items) != 1 || job.Items[0].Name != "sales" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

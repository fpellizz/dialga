package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

// The Activity page lists every operation: the single ones run as jobs, and
// so do the requests that were refused.

func jobOf(t *testing.T, js *jobServer, body string) jobs.Job {
	t.Helper()
	_, data := events(t, body)
	var ref JobRef
	if json.Unmarshal([]byte(data[0]), &ref) != nil || !jobs.ValidID(ref.ID) {
		t.Fatalf("first event %s", data[0])
	}
	job, err := js.jobs.Get(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestExecuteRunsAsAJob(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.s3.Put("dev-sqlserver-backups", "import/sales.bak", []byte("x"), "")
	rec := doJSON(js.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/restore/execute", `{"database":"sales","key":"sales.bak"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	names, data := events(t, rec.Body.String())
	if names[0] != "job" || names[len(names)-1] != "done" {
		t.Fatalf("events %v", names)
	}
	job := jobOf(t, js, rec.Body.String())
	var done PlanDone
	if json.Unmarshal([]byte(data[len(data)-1]), &done) != nil || !done.OK || done.JobID != job.ID {
		t.Fatalf("done %s", data[len(data)-1])
	}
	if job.Kind != jobs.Operation || job.Operation != "restore" || job.Origin != jobs.OriginApp || job.State != jobs.Succeeded ||
		job.Target != "dev-sql" || len(job.Items) != 1 || job.Items[0].Name != "sales" {
		t.Fatalf("job %+v", job)
	}
	// The plan actions are the steps: same ids, titles and T-SQL; the native
	// task is followed like in the other jobs.
	var ids []string
	for _, st := range job.Items[0].Steps {
		ids = append(ids, st.ID+":"+st.Kind+":"+string(st.Status))
	}
	if got := strings.Join(ids, " "); got != "restore:pipeline.task:succeeded wait:pipeline.wait:succeeded" {
		t.Fatalf("steps %s", got)
	}
	if !strings.Contains(string(job.Items[0].Steps[0].Params), "rds_restore_database") || job.Items[0].Task == nil || job.Items[0].Task.Lifecycle != "SUCCESS" {
		t.Fatalf("item %+v", job.Items[0])
	}
	// And it is in the list, with its origin.
	list := js.jobs.List()
	if len(list) != 1 || list[0].ID != job.ID || list[0].Operation != "restore" {
		t.Fatalf("list %+v", list)
	}
}

func TestExecuteDropAndCancelRunAsJobs(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.rds.states["sales"] = "ONLINE"
	rec := doJSON(js.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/drop/execute", `{"database":"sales","confirm":"sales"}`)
	job := jobOf(t, js, rec.Body.String())
	if job.Operation != "drop" || job.State != jobs.Succeeded || job.Items[0].Steps[0].ID != "drop" || job.Items[0].Steps[0].Kind != "pipeline.sql" ||
		job.Items[0].Result != "sales dropped" {
		t.Fatalf("drop job %+v", job.Items)
	}
}

// A client that goes away does not stop the operation: the job finishes.
func TestExecuteGoesOnWithoutTheClient(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.s3.Put("dev-sqlserver-backups", "import/sales.bak", []byte("x"), "")
	js.rds.hold = make(chan struct{})
	srv := httptest.NewServer(js.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/targets/dev-sql/plans/restore/execute",
		strings.NewReader(`{"database":"sales","key":"sales.bak"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	var id string
	for sc.Scan() {
		if after, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ref JobRef
			_ = json.Unmarshal([]byte(after), &ref)
			id = ref.ID
			break
		}
	}
	if id == "" {
		t.Fatal("no job event")
	}
	cancel() // the client is gone while the task is followed
	_ = resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if j, _ := js.jobs.Get(id); j.State.Ended() {
		t.Fatalf("the job ended with the client: %s", j.State)
	}
	close(js.rds.hold)
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	done, err := js.jobs.Wait(wctx, id)
	if err != nil || done.State != jobs.Succeeded {
		t.Fatalf("job %s, %v", done.State, err)
	}
}

// The operation is stopped with the job, like any other.
func TestExecuteCancelsThroughTheJob(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.s3.Put("dev-sqlserver-backups", "import/sales.bak", []byte("x"), "")
	js.rds.hold = make(chan struct{})
	srv := httptest.NewServer(js.h)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/targets/dev-sql/plans/restore/execute", strings.NewReader(`{"database":"sales","key":"sales.bak"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	var id string
	for sc.Scan() {
		if after, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ref JobRef
			_ = json.Unmarshal([]byte(after), &ref)
			id = ref.ID
			break
		}
	}
	if rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs/"+id+"/cancel", ``); rec.Code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	var last string
	for sc.Scan() {
		if after, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			last = after
		}
	}
	var done PlanDone
	if json.Unmarshal([]byte(last), &done) != nil || done.OK || done.JobID != id || done.Error == "" {
		t.Fatalf("done %s", last)
	}
	if j, _ := js.jobs.Get(id); j.State != jobs.Cancelled {
		t.Fatalf("job %s", j.State)
	}
}

func TestRefusedExecuteIsRecorded(t *testing.T) {
	doc := testConfig
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	tests := []struct {
		name   string
		doc    *string
		env    map[string]string
		states map[string]string
		path   string
		body   string
		want   int
		checks bool
	}{
		{"pre-flight failed", &doc, testEnv, nil, "finish", `{"database":"sales"}`, http.StatusPreconditionFailed, true},
		{"plan changed", &doc, testEnv, map[string]string{"sales": "ONLINE"}, "drop", `{"database":"sales","confirm":"sales","digest":"0123"}`, http.StatusPreconditionFailed, false},
		{"not confirmed", &doc, testEnv, map[string]string{"sales": "ONLINE"}, "drop", `{"database":"sales"}`, http.StatusUnprocessableEntity, false},
		{"protected", &protected, testEnv, map[string]string{"sales": "ONLINE"}, "drop", `{"database":"sales","confirm":"sales"}`, http.StatusUnprocessableEntity, false},
		{"no password", &doc, nil, nil, "drop", `{"database":"sales"}`, http.StatusUnprocessableEntity, false},
		{"cancel of a task that ended", &doc, testEnv, nil, "cancel", `{"taskId":7}`, http.StatusPreconditionFailed, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			js := newJobServer(t, tc.doc, tc.env)
			for k, v := range tc.states {
				js.rds.states[k] = v
			}
			js.rds.tasks[7] = &mssql.Task{ID: 7, Type: "RESTORE_DB", Database: "sales", Lifecycle: mssql.Success}
			rec := doJSON(js.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/"+tc.path+"/execute", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			list := js.jobs.List()
			if len(list) != 1 {
				t.Fatalf("jobs %+v", list)
			}
			got, _ := js.jobs.Get(list[0].ID)
			if got.State != jobs.Failed || got.Kind != jobs.Operation || got.Operation != tc.path || got.Refusal == nil ||
				got.Refusal.Status != tc.want || got.Refusal.Reason == "" || len(got.Items) != 1 || got.Items[0].State != jobs.Failed ||
				got.EndedAt == nil || (len(got.Refusal.Checks) > 0) != tc.checks {
				t.Fatalf("job %+v, refusal %+v", got, got.Refusal)
			}
			if tc.path == "cancel" && got.Items[0].Name != "task 7" || tc.path != "cancel" && got.Items[0].Name != "sales" {
				t.Fatalf("items %+v", got.Items)
			}
			var l JobList
			_ = json.Unmarshal(doJSON(js.h, http.MethodGet, "/api/v1/jobs", "").Body.Bytes(), &l)
			if len(l.Jobs) != 1 || l.Jobs[0].Kind != jobs.Operation || l.Jobs[0].Operation != tc.path || l.Jobs[0].Origin != jobs.OriginApp ||
				l.Jobs[0].Refusal == nil || l.Jobs[0].Refusal.Status != tc.want || l.Jobs[0].Refusal.Reason == "" || len(l.Jobs[0].Refusal.Checks) != 0 {
				t.Fatalf("list summary %+v", l.Jobs)
			}
			if strings.Contains(string(mustMarshal(t, got)), "s3cret") {
				t.Fatal("the password reached the journal")
			}
		})
	}
}

func TestSessionFailureIsRecorded(t *testing.T) {
	doc := testConfig
	srv := newOpsServer(t, &doc, testEnv, &fakeInstance{}, fakeObjects{})
	srv.openErr = context.DeadlineExceeded
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/restore/execute", `{"database":"sales","key":"sales.bak"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
	list := srv.jobs.List()
	if len(list) != 1 || list[0].Refusal == nil || list[0].Refusal.Status != 502 || list[0].Operation != "restore" {
		t.Fatalf("jobs %+v", list)
	}
}

func TestPreviewsAndDryRunsCreateNoJob(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.rds.states["sales"] = "ONLINE"
	for _, req := range [][2]string{
		{"/api/v1/targets/dev-sql/plans/drop", `{"database":"sales"}`},
		{"/api/v1/targets/dev-sql/plans/finish", `{"database":"sales"}`}, // a failing check is an answer, not a refusal
		{"/api/v1/jobs", importBody(t, `,"dryRun":true`)},
	} {
		if rec := doJSON(js.h, http.MethodPost, req[0], req[1]); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", req[0], rec.Code, rec.Body)
		}
	}
	// Not even a dry run that cannot get a password.
	nopw := newJobServer(t, &doc, nil)
	if rec := doJSON(nopw.h, http.MethodPost, "/api/v1/jobs", importBody(t, `,"dryRun":true`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	if n := len(js.jobs.List()) + len(nopw.jobs.List()); n != 0 {
		t.Fatalf("%d jobs", n)
	}
}

func TestReportLeavesNoJob(t *testing.T) {
	doc := testConfig
	js := newJobServer(t, &doc, testEnv)
	js.rds.states["sales"] = "ONLINE"
	if rec := do2(js.h, "/api/v1/targets/dev-sql/databases/sales/report"); rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if rec := do2(js.h, "/api/v1/targets/dev-sql/databases/nope/report"); rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if n := len(js.jobs.List()); n != 0 {
		t.Fatalf("%d jobs", n)
	}
}

func do2(h http.Handler, path string) *httptest.ResponseRecorder { return do(h, http.MethodGet, path) }

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A job of a CLI run shows up in the list of the app engine while it runs,
// and the engine can follow it; it cannot stop it.
func TestCLIJobAppearsInTheAppList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("job locks are not implemented on Windows yet (v2.0): a job of another process looks interrupted")
	}
	dir := t.TempDir()
	appJobs, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appJobs.Close(context.Background()) }()
	doc := testConfig
	js := newJobServerOn(t, &doc, testEnv, appJobs, transfertest.New(), &jobInstance{states: map[string]string{}, tasks: map[int]*mssql.Task{}})

	cli, err := jobs.Open(dir, jobs.Options{Origin: jobs.OriginCLI})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	step := jobs.NewStep("work", "Work", jobs.NoResource, func(ctx context.Context, _ jobs.Reporter) (string, error) {
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	job, err := cli.Start(jobs.Spec{Kind: jobs.Import, Target: "dev-sql", Items: []jobs.ItemSpec{{Name: "sales", Steps: []jobs.Step{step}}}})
	if err != nil {
		t.Fatal(err)
	}
	listed := func() JobList {
		var l JobList
		_ = json.Unmarshal(doJSON(js.h, http.MethodGet, "/api/v1/jobs", "").Body.Bytes(), &l)
		return l
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(listed().Jobs) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if l := listed(); len(l.Jobs) != 1 || l.Jobs[0].ID != job.ID || l.Jobs[0].Origin != jobs.OriginCLI || l.Jobs[0].State.Ended() {
		t.Fatalf("list %+v", l)
	}
	if rec := doJSON(js.h, http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancel", ``); rec.Code != http.StatusConflict {
		t.Fatalf("cancel of a job of another process: %d %s", rec.Code, rec.Body)
	}
	close(release)
	for l := listed(); len(l.Jobs) == 1 && !l.Jobs[0].State.Ended() && time.Now().Before(deadline); l = listed() {
		time.Sleep(20 * time.Millisecond)
	}
	if l := listed(); l.Jobs[0].State != jobs.Succeeded {
		t.Fatalf("list after the end %+v", l)
	}
	_ = cli.Close(context.Background())
}

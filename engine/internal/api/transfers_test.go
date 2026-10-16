package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

const testBucket = "dev-sqlserver-backups"

// newTransferServer serves configDoc (nil: no file) on a fake S3; openErr
// makes opening S3 fail.
func newTransferServer(t *testing.T, configDoc *string, fake *transfertest.S3, openErr error) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path,
		OpenS3: func(context.Context, config.Target) (transfer.Client, error) { return fake, openErr },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type sseEvent struct {
	name string
	data string
}

func parseSSE(body string) []sseEvent {
	var events []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				e.name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				e.data = v
			}
		}
		events = append(events, e)
	}
	return events
}

func backupFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sales.bak")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestUploadsPlanChangesNothing(t *testing.T) {
	doc := testConfig
	fake := transfertest.New()
	h := newTransferServer(t, &doc, fake, nil)
	path := backupFile(t, "backup")
	rec := post(h, "/api/v1/targets/dev-sql/uploads", `{"files":[`+jsonString(path)+`]}`)
	var plan transfer.Batch
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(rec.Body.Bytes(), &plan) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(plan.Items) != 1 || plan.Items[0].Key != "import/sales.bak" || len(plan.Plan.Actions) != 1 ||
		!strings.HasPrefix(plan.Plan.Actions[0].Statement, "PUT s3://dev-sqlserver-backups/import/sales.bak") ||
		plan.Rejected == nil || plan.Confirmation != "" {
		t.Fatalf("plan %s", rec.Body)
	}
	if fake.Calls("CreateMultipartUpload") != 0 {
		t.Fatal("the plan uploaded")
	}
}

func TestUploadsApplyStreams(t *testing.T) {
	doc := testConfig
	fake := transfertest.New()
	fake.Put(testBucket, "import/old.bak", []byte("old"), "")
	h := newTransferServer(t, &doc, fake, nil)
	path := backupFile(t, "backup")
	old := filepath.Join(filepath.Dir(path), "old.bak")
	if err := os.WriteFile(old, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := post(h, "/api/v1/targets/dev-sql/uploads",
		`{"files":[`+jsonString(path)+`,`+jsonString(old)+`],"apply":true,"concurrency":2}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	events := parseSSE(rec.Body.String())
	names := map[string]int{}
	for _, e := range events {
		names[e.name]++
	}
	if events[0].name != "plan" || !strings.Contains(events[0].data, `"reason":"already in`) ||
		names["step"] != 2 || names["progress"] < 1 || events[len(events)-1].name != "done" {
		t.Fatalf("events %v\n%s", names, rec.Body)
	}
	var done TransferDone
	if err := json.Unmarshal([]byte(events[len(events)-1].data), &done); err != nil {
		t.Fatal(err)
	}
	// One file uploaded, one rejected: not ok.
	if done.OK || done.Error != "" || len(done.Results) != 1 || !done.Results[0].Verified {
		t.Fatalf("done %+v", done)
	}
	if fake.Get(testBucket, "import/sales.bak") == nil || string(fake.Get(testBucket, "import/old.bak").Data) != "old" {
		t.Fatal("S3 is not as planned")
	}
	var progress transfer.Progress
	for _, e := range events {
		if e.name == "progress" {
			_ = json.Unmarshal([]byte(e.data), &progress)
		}
	}
	if progress.Step != "upload-1" || progress.Done != 6 || progress.Total != 6 {
		t.Fatalf("last progress %+v", progress)
	}
}

func TestUploadsFailureIsInTheStream(t *testing.T) {
	doc := testConfig
	fake := transfertest.New()
	fake.OnUploadPart = func(context.Context, int32, int) error {
		return transfertest.Error("UploadPart", 403, "AccessDenied")
	}
	h := newTransferServer(t, &doc, fake, nil)
	rec := post(h, "/api/v1/targets/dev-sql/uploads", `{"files":[`+jsonString(backupFile(t, "x"))+`],"apply":true}`)
	events := parseSSE(rec.Body.String())
	var done TransferDone
	if rec.Code != http.StatusOK || json.Unmarshal([]byte(events[len(events)-1].data), &done) != nil ||
		done.OK || !strings.Contains(done.Error, "nothing is left in S3") || fake.Uploads() != 0 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestUploadsErrors(t *testing.T) {
	doc := testConfig
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	path := jsonString(backupFile(t, "backup"))
	tests := []struct {
		name    string
		doc     *string
		openErr error
		body    string
		want    int
	}{
		{"not JSON", &doc, nil, `{`, http.StatusBadRequest},
		{"unknown field", &doc, nil, `{"files":[` + path + `],"bucket":"other"}`, http.StatusBadRequest},
		{"no files", &doc, nil, `{"files":[]}`, http.StatusBadRequest},
		{"relative path", &doc, nil, `{"files":["sales.bak"]}`, http.StatusBadRequest},
		{"key and prefix", &doc, nil, `{"files":[` + path + `],"key":"a.bak","prefix":"p"}`, http.StatusBadRequest},
		{"concurrency", &doc, nil, `{"files":[` + path + `],"concurrency":99}`, http.StatusBadRequest},
		{"unknown target", &doc, nil, `{"files":[` + path + `]}`, http.StatusNotFound},
		{"missing config", nil, nil, `{"files":[` + path + `]}`, http.StatusConflict},
		{"AWS fails", &doc, errors.New("no credentials"), `{"files":[` + path + `]}`, http.StatusBadGateway},
		{"protected, no confirm", &protected, nil, `{"files":[` + path + `],"apply":true}`, http.StatusUnprocessableEntity},
		{"protected, wrong confirm", &protected, nil, `{"files":[` + path + `],"apply":true,"confirm":"prod"}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := transfertest.New()
			h := newTransferServer(t, tc.doc, fake, tc.openErr)
			target := "dev-sql"
			if tc.want == http.StatusNotFound {
				target = "nope"
			}
			rec := post(h, "/api/v1/targets/"+target+"/uploads", tc.body)
			if rec.Code != tc.want || fake.Calls("CreateMultipartUpload") != 0 {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
	h := newTransferServer(t, &protected, transfertest.New(), nil)
	rec := post(h, "/api/v1/targets/dev-sql/uploads", `{"files":[`+path+`],"apply":true,"confirm":"dev-sql"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("confirmed: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestDownloads(t *testing.T) {
	doc := testConfig
	fake := transfertest.New()
	fake.Put(testBucket, "export/sales.bak", []byte("exported"), types.ChecksumAlgorithmCrc64nvme)
	h := newTransferServer(t, &doc, fake, nil)
	dir := t.TempDir()
	rec := post(h, "/api/v1/targets/dev-sql/downloads", `{"key":"sales.bak","dest":`+jsonString(dir)+`}`)
	var plan transfer.Batch
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &plan) != nil || len(plan.Items) != 1 ||
		plan.Items[0].Path != filepath.Join(dir, "sales.bak") || plan.Items[0].Checksum != "CRC64NVME" {
		t.Fatalf("plan: status %d, body %s", rec.Code, rec.Body)
	}
	rec = post(h, "/api/v1/targets/dev-sql/downloads", `{"key":"sales.bak","dest":`+jsonString(dir)+`,"apply":true}`)
	got, _ := os.ReadFile(filepath.Join(dir, "sales.bak"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || string(got) != "exported" {
		t.Fatalf("apply: status %d, body %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]int{
		`{"key":"sales.bak","dest":"relative"}`:                     http.StatusBadRequest,
		`{"dest":` + jsonString(dir) + `}`:                          http.StatusBadRequest,
		`{"key":"s3://other/x.bak","dest":` + jsonString(dir) + `}`: http.StatusBadRequest,
	} {
		if rec := post(h, "/api/v1/targets/dev-sql/downloads", body); rec.Code != want {
			t.Errorf("%s: status %d, want %d", body, rec.Code, want)
		}
	}
}

// blockedUpload is a fake S3 whose parts hang until their context ends;
// started is closed when the first part arrives.
func blockedUpload() (*transfertest.S3, chan struct{}) {
	fake := transfertest.New()
	started := make(chan struct{})
	var once sync.Once
	fake.OnUploadPart = func(ctx context.Context, _ int32, _ int) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	return fake, started
}

func postUpload(ctx context.Context, url, file string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/v1/targets/dev-sql/uploads",
		strings.NewReader(`{"files":[`+jsonString(file)+`],"apply":true}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// readyWriter hands over the ready line of Serve.
type readyWriter chan []byte

func (w readyWriter) Write(p []byte) (int, error) {
	w <- append([]byte(nil), p...)
	return len(p), nil
}

func TestServeShutdownAbortsTheUploads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	fake, started := blockedUpload()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, served := make(readyWriter, 1), make(chan error, 1)
	go func() {
		served <- Serve(ctx, Options{
			Token: testToken, ConfigPath: path,
			OpenS3: func(context.Context, config.Target) (transfer.Client, error) { return fake, nil },
		}, ready)
	}()
	var line struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(<-ready, &line); err != nil {
		t.Fatal(err)
	}
	go postUpload(context.Background(), fmt.Sprintf("http://127.0.0.1:%d", line.Port), backupFile(t, "backup"))
	<-started
	cancel() // the shell is gone: stdin closed, or SIGTERM
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	// Serve returned only once the upload was aborted.
	if fake.Calls("AbortMultipartUpload") != 1 || fake.Uploads() != 0 {
		t.Fatalf("aborts %d, open uploads %d", fake.Calls("AbortMultipartUpload"), fake.Uploads())
	}
}

func TestClientGoneAbortsTheUpload(t *testing.T) {
	doc := testConfig
	fake, started := blockedUpload()
	srv := httptest.NewServer(newTransferServer(t, &doc, fake, nil))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		postUpload(ctx, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), backupFile(t, "backup"))
	}()
	<-started
	cancel() // the GUI closes the stream
	<-done
	waitFor(t, "the abort", func() bool { return fake.Calls("AbortMultipartUpload") == 1 && fake.Uploads() == 0 })
}

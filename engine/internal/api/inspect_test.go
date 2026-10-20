package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/inspect"
)

// newInspectServer serves configDoc (nil: no file) with run as RunInspect.
func newInspectServer(t *testing.T, configDoc *string, run InspectFunc) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{Token: testToken, ConfigPath: path, RunInspect: run})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type sseEvent struct{ name, data string }

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for block := range strings.SplitSeq(strings.TrimSpace(body), "\n\n") {
		var e sseEvent
		for line := range strings.SplitSeq(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				e.name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				e.data = v
			}
		}
		out = append(out, e)
	}
	return out
}

func bakFile(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the inspection refuses Windows paths (bind mount syntax, v2.0)")
	}
	p := filepath.Join(t.TempDir(), "app.bak")
	if err := os.WriteFile(p, []byte("bak"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectStream(t *testing.T) {
	file := bakFile(t)
	var got inspect.Request
	run := func(_ context.Context, req inspect.Request, emit check.Emitter) (*inspect.Result, error) {
		got = req
		emit(check.Event{Step: "runtime", Label: "Container runtime", Status: check.OK})
		return &inspect.Result{Outcome: inspect.Restorable, Backups: []inspect.Backup{{Files: req.Paths, Outcome: inspect.Restorable}}}, nil
	}
	doc := testConfig
	h := newInspectServer(t, &doc, run)
	rec := postJSON(h, "/api/v1/inspect", `{"paths":[`+jstr(file)+`],"target":"dev-sql","image":"localhost/mssql:2025"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	events := parseSSE(rec.Body.String())
	var names []string
	for _, e := range events {
		names = append(names, e.name)
	}
	if !slices.Equal(names, []string{"step", "result", "done"}) || events[2].data != `{"ok":true}` {
		t.Fatalf("events %+v", events)
	}
	var res inspect.Result
	if err := json.Unmarshal([]byte(events[1].data), &res); err != nil || res.Backups[0].Files[0] != file {
		t.Fatalf("result %s: %v", events[1].data, err)
	}
	if got.Target == nil || got.Image != "localhost/mssql:2025" || !slices.Equal(got.Paths, []string{file}) {
		t.Fatalf("request %+v", got)
	}
}

func TestInspectStreamError(t *testing.T) {
	run := func(_ context.Context, _ inspect.Request, emit check.Emitter) (*inspect.Result, error) {
		emit(check.Event{Step: "runtime", Label: "Container runtime", Status: check.Failed, Detail: "no container runtime"})
		return nil, errors.New("no container runtime")
	}
	// Without a target the configuration is not needed.
	h := newInspectServer(t, nil, run)
	rec := postJSON(h, "/api/v1/inspect", `{"paths":[`+jstr(bakFile(t))+`]}`)
	events := parseSSE(rec.Body.String())
	if rec.Code != http.StatusOK || len(events) != 3 || events[1].name != "notice" ||
		!strings.Contains(events[1].data, "inspection stopped: no container runtime") || events[2].data != `{"ok":false}` {
		t.Fatalf("status %d, events %+v", rec.Code, events)
	}
}

func TestInspectRejects(t *testing.T) {
	file := bakFile(t)
	run := func(context.Context, inspect.Request, check.Emitter) (*inspect.Result, error) {
		t.Fatal("inspect ran")
		return nil, nil
	}
	doc := testConfig
	tests := []struct {
		name   string
		config *string
		body   string
		status int
		want   string
	}{
		{"not JSON", &doc, `paths`, http.StatusBadRequest, "invalid body"},
		{"unknown field", &doc, `{"paths":[` + jstr(file) + `],"password":"x"}`, http.StatusBadRequest, "unknown field"},
		{"no paths", &doc, `{"paths":[]}`, http.StatusBadRequest, "no .bak file"},
		{"relative path", &doc, `{"paths":["app.bak"]}`, http.StatusBadRequest, "must be absolute"},
		{"missing file", &doc, `{"paths":[` + jstr(file+".missing") + `]}`, http.StatusBadRequest, noSuchFile},
		{"bad image", &doc, `{"paths":[` + jstr(file) + `],"image":"--privileged"}`, http.StatusBadRequest, "invalid image"},
		{"unknown target", &doc, `{"paths":[` + jstr(file) + `],"target":"nope"}`, http.StatusNotFound, "unknown target"},
		{"target without configuration", nil, `{"paths":[` + jstr(file) + `],"target":"dev-sql"}`, http.StatusConflict, noSuchFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(newInspectServer(t, tc.config, run), "/api/v1/inspect", tc.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status %d, body %q", rec.Code, rec.Body)
			}
		})
	}
}

// jstr is s as a JSON string: Windows paths have backslashes.
func jstr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// noSuchFile is the text the OS gives for a missing file.
var noSuchFile = func() string {
	_, err := os.Open(filepath.Join(os.TempDir(), "dialga-test-missing-file"))
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return "no such file"
}()

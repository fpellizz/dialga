package inspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/decisyon/dialga/engine/internal/check"
)

const testPassword = "a!TESTONLY"

// fakeQuerier answers statements with fixed rows or errors.
type fakeQuerier struct {
	rows   map[string][]row
	errs   map[string]error
	closed bool
}

func (f *fakeQuerier) query(_ context.Context, stmt string) ([]row, error) {
	if err := f.errs[stmt]; err != nil {
		return nil, err
	}
	rows, ok := f.rows[stmt]
	if !ok {
		return nil, errors.New("unexpected statement: " + stmt)
	}
	return rows, nil
}

func (f *fakeQuerier) Close() error { f.closed = true; return nil }

const versionQuery = "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(30)) AS version"

// backupsQuerier serves /bak/1.bak = the 2017 backup, /bak/2.bak = the 2022
// one, /bak/3.bak = not a backup.
func backupsQuerier(t *testing.T) *fakeQuerier {
	label2022 := row{}
	for k, v := range fixture(t, "labelonly-2017.json")[0] {
		label2022[k] = v
	}
	label2022["MediaSetId"] = "11111111-2222-3333-4444-555555555555"
	return &fakeQuerier{
		rows: map[string][]row{
			versionQuery: {{"version": "16.0.4305.5"}},
			"RESTORE LABELONLY FROM DISK = N'/bak/1.bak'":                  fixture(t, "labelonly-2017.json"),
			"RESTORE LABELONLY FROM DISK = N'/bak/2.bak'":                  {label2022},
			"RESTORE HEADERONLY FROM DISK = N'/bak/1.bak'":                 fixture(t, "headeronly-2017.json"),
			"RESTORE HEADERONLY FROM DISK = N'/bak/2.bak'":                 fixture(t, "headeronly-2022.json"),
			"RESTORE FILELISTONLY FROM DISK = N'/bak/1.bak' WITH FILE = 1": fixture(t, "filelistonly-2017.json"),
			"RESTORE FILELISTONLY FROM DISK = N'/bak/2.bak' WITH FILE = 1": fixture(t, "filelistonly-2022.json"),
		},
		errs: map[string]error{
			"RESTORE LABELONLY FROM DISK = N'/bak/3.bak'": mssql.Error{Number: errTerminated, All: []mssql.Error{
				{Number: 3241, Message: "The media family on device '/bak/3.bak' is incorrectly formed."},
				{Number: errTerminated, Message: "RESTORE LABELONLY is terminating abnormally."},
			}},
		},
	}
}

func testFiles(t *testing.T, names ...string) []string {
	t.Helper()
	skipOnWindows(t)
	dir := resolvedTempDir(t)
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(dir, n)
		if err := os.WriteFile(paths[i], []byte("bak"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func testInspector(f *fakeRunner, connect func(ctx context.Context, addr, password string) (querier, error)) *inspector {
	return &inspector{
		getenv:   func(string) string { return "" },
		lookPath: installed("podman"),
		run:      f.run,
		connect:  connect,
		password: func() string { return testPassword },
		poll:     time.Millisecond,
		ready:    50 * time.Millisecond,
		cleanup:  time.Second,
	}
}

func serve(q querier) func(context.Context, string, string) (querier, error) {
	return func(_ context.Context, addr, password string) (querier, error) {
		if addr != "127.0.0.1:40000" || password != testPassword {
			return nil, errors.New("wrong address or password")
		}
		return q, nil
	}
}

func testTarget(free int64) func(context.Context) (Target, error) {
	return func(context.Context) (Target, error) {
		return Target{Name: "dev-sql", Instance: "dev-sqlserver", Engine: "sqlserver-se",
			EngineVersion: "14.00.3540.1.v1", Major: 14, FreeBytes: &free}, nil
	}
}

// ends are the final events, as "step:status".
func ends(events []check.Event) []string {
	var out []string
	for _, e := range events {
		if e.Status != check.Running {
			out = append(out, e.Step+":"+string(e.Status))
		}
	}
	return out
}

func TestInspectWithTarget(t *testing.T) {
	f, q := newFakeRunner(), backupsQuerier(t)
	paths := testFiles(t, "app2017.bak", "app2022.bak", "junk.bak")
	var events []check.Event
	res, err := testInspector(f, serve(q)).inspect(context.Background(),
		Request{Paths: paths, Target: testTarget(100 << 30)}, func(e check.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"target:ok", "runtime:ok", "image:ok", "container:ok", "ready:ok", "media:failed",
		"header:ok", "files:ok", "verdict:failed", "space:ok", "cleanup:ok"}
	if got := ends(events); !slices.Equal(got, want) {
		t.Fatalf("events\n got %v\nwant %v", got, want)
	}
	if res.Outcome != Unreadable || res.Server != "16.0.4305.5" || res.Runtime != "podman 5.8.4" || len(res.Backups) != 3 {
		t.Fatalf("result %+v", res)
	}
	if b := res.Backups[0]; b.Outcome != Restorable || b.Header.DatabaseName != "t2017" || b.RequiredBytes != 16<<20 {
		t.Errorf("2017: %+v", b)
	}
	if b := res.Backups[1]; b.Outcome != NotRestorable || b.Header.Major != 16 || !strings.Contains(b.Remedy, ".bacpac") {
		t.Errorf("2022: %+v", b)
	}
	if b := res.Backups[2]; b.Outcome != Unreadable || b.Error != "The media family on device 'junk.bak' is incorrectly formed." ||
		!strings.Contains(b.Remedy, "2025-latest") {
		t.Errorf("junk: %+v", b)
	}
	if res.RequiredBytes != 32<<20 || res.Space.Status != check.OK {
		t.Errorf("required %d, space %+v", res.RequiredBytes, res.Space)
	}
	verdict := events[slices.IndexFunc(events, func(e check.Event) bool { return e.Step == "verdict" && e.Status != check.Running })]
	if !strings.Contains(verdict.Detail, "1 of 3 restorable on dev-sql · not restorable: app2022.bak · unreadable: junk.bak") {
		t.Errorf("verdict detail %q", verdict.Detail)
	}

	run := f.called("run")
	if run == nil || !slices.Equal(run.env, []string{"MSSQL_SA_PASSWORD=" + testPassword}) {
		t.Fatalf("run: %+v", run)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), testPassword) {
			t.Fatalf("password on a command line: %q", c.args)
		}
	}
	if !slices.Contains(run.args, paths[2]+":/bak/3.bak:ro") {
		t.Errorf("mounts: %q", run.args)
	}
	if last := f.calls[len(f.calls)-1]; last.args[0] != "rm" || !q.closed {
		t.Errorf("last call %q, session closed %v", last.args, q.closed)
	}
}

func TestInspectHeadersOnly(t *testing.T) {
	f, q := newFakeRunner(), backupsQuerier(t)
	var events []check.Event
	res, err := testInspector(f, serve(q)).inspect(context.Background(),
		Request{Paths: testFiles(t, "a.bak"), Image: "localhost/mssql:2025"}, func(e check.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"runtime:ok", "image:ok", "container:ok", "ready:ok", "media:ok", "header:ok", "files:ok", "cleanup:ok"}
	if got := ends(events); !slices.Equal(got, want) {
		t.Fatalf("events %v", got)
	}
	if res.Outcome != Read || res.Backups[0].Outcome != Read || res.Backups[0].Findings != nil || res.Space != nil {
		t.Fatalf("result %+v", res)
	}
	if run := f.called("run"); run.args[len(run.args)-1] != "localhost/mssql:2025" || res.Image != "localhost/mssql:2025" {
		t.Fatalf("image: %q", run.args)
	}
}

func TestInspectSpaceRulesOut(t *testing.T) {
	f, q := newFakeRunner(), backupsQuerier(t)
	res, err := testInspector(f, serve(q)).inspect(context.Background(),
		Request{Paths: testFiles(t, "a.bak"), Target: testTarget(10 << 20)}, func(check.Event) {})
	if err != nil || res.Outcome != NotRestorable || res.Backups[0].Outcome != Restorable || res.Space.Status != check.Failed {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestInspectStopsEarly(t *testing.T) {
	tests := []struct {
		name    string
		target  func(context.Context) (Target, error)
		setup   func(*fakeRunner)
		connect func(context.Context, string, string) (querier, error)
		wantErr string
		want    []string
		removed bool
	}{
		{
			name:    "target lookup fails: nothing started",
			target:  func(context.Context) (Target, error) { return Target{}, errors.New("no AWS credentials") },
			wantErr: "no AWS credentials",
			want: []string{"target:failed", "runtime:skipped", "image:skipped", "container:skipped", "ready:skipped",
				"media:skipped", "header:skipped", "files:skipped", "verdict:skipped", "space:skipped"},
		},
		{
			name:    "image missing",
			setup:   func(f *fakeRunner) { f.errs["image"] = errors.New("exit status 1") },
			wantErr: "podman pull " + DefaultImage,
			want:    []string{"runtime:ok", "image:failed", "container:skipped", "ready:skipped", "media:skipped", "header:skipped", "files:skipped"},
		},
		{
			name:    "container does not start",
			setup:   func(f *fakeRunner) { f.errs["run"] = errors.New("podman run: port in use") },
			wantErr: "port in use",
			want:    []string{"runtime:ok", "image:ok", "container:failed", "ready:skipped", "media:skipped", "header:skipped", "files:skipped", "cleanup:ok"},
			removed: true,
		},
		{
			name: "SQL Server stops",
			setup: func(f *fakeRunner) {
				f.out["inspect"] = "false"
				f.out["logs"] = "starting\nsqlservr: This program requires a machine with at least 2000 megabytes of memory.\n"
			},
			connect: func(context.Context, string, string) (querier, error) { return nil, errors.New("connection refused") },
			wantErr: "at least 2000 megabytes",
			want:    []string{"runtime:ok", "image:ok", "container:ok", "ready:failed", "media:skipped", "header:skipped", "files:skipped", "cleanup:ok"},
			removed: true,
		},
		{
			name:    "SQL Server never ready",
			connect: func(context.Context, string, string) (querier, error) { return nil, errors.New("login failed") },
			wantErr: "not ready after 50ms: login failed",
			want:    []string{"runtime:ok", "image:ok", "container:ok", "ready:failed", "media:skipped", "header:skipped", "files:skipped", "cleanup:ok"},
			removed: true,
		},
		{
			name:    "cannot remove the container",
			setup:   func(f *fakeRunner) { f.errs["rm"] = errors.New("podman rm: busy") },
			connect: func(context.Context, string, string) (querier, error) { return nil, errors.New("login failed") },
			wantErr: "not ready",
			want:    []string{"runtime:ok", "image:ok", "container:ok", "ready:failed", "media:skipped", "header:skipped", "files:skipped", "cleanup:warning"},
			removed: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			if tc.setup != nil {
				tc.setup(f)
			}
			var events []check.Event
			_, err := testInspector(f, tc.connect).inspect(context.Background(),
				Request{Paths: testFiles(t, "a.bak"), Target: tc.target}, func(e check.Event) { events = append(events, e) })
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := ends(events); !slices.Equal(got, tc.want) {
				t.Fatalf("events\n got %v\nwant %v", got, tc.want)
			}
			if removed := f.called("rm") != nil; removed != tc.removed {
				t.Fatalf("removed %v, calls %+v", removed, f.calls)
			}
			if tc.target != nil && len(f.calls) != 0 {
				t.Fatalf("runtime called after a failed target lookup: %+v", f.calls)
			}
		})
	}
}

func TestInspectCancelStillRemoves(t *testing.T) {
	f := newFakeRunner()
	ctx, cancel := context.WithCancel(context.Background())
	connect := func(context.Context, string, string) (querier, error) {
		cancel() // the user closes the wizard while SQL Server starts
		return nil, errors.New("connection refused")
	}
	_, err := testInspector(f, connect).inspect(ctx, Request{Paths: testFiles(t, "a.bak")}, func(check.Event) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	rm := f.called("rm")
	if rm == nil || rm.ctxErr != nil {
		t.Fatalf("rm %+v: the removal must not inherit the cancelled context", rm)
	}
}

func TestInspectStripedOutOfOrder(t *testing.T) {
	f := newFakeRunner()
	stripe := func(n string) []row {
		r := row{}
		for k, v := range fixture(t, "labelonly-striped.json")[0] {
			r[k] = v
		}
		r["FamilySequenceNumber"] = n
		return []row{r}
	}
	q := &fakeQuerier{rows: map[string][]row{
		versionQuery: {{"version": "16.0.4305.5"}},
		"RESTORE LABELONLY FROM DISK = N'/bak/1.bak'":                                        stripe("2"),
		"RESTORE LABELONLY FROM DISK = N'/bak/2.bak'":                                        stripe("1"),
		"RESTORE HEADERONLY FROM DISK = N'/bak/2.bak', DISK = N'/bak/1.bak'":                 fixture(t, "headeronly-2022.json"),
		"RESTORE FILELISTONLY FROM DISK = N'/bak/2.bak', DISK = N'/bak/1.bak' WITH FILE = 1": fixture(t, "filelistonly-2022.json"),
	}}
	paths := testFiles(t, "s2.bak", "s1.bak")
	res, err := testInspector(f, serve(q)).inspect(context.Background(), Request{Paths: paths}, func(check.Event) {})
	if err != nil || len(res.Backups) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if b := res.Backups[0]; !slices.Equal(b.Files, []string{paths[1], paths[0]}) || b.Outcome != Read || b.Header.DatabaseName != "t2022" {
		t.Fatalf("striped: %+v", b)
	}
}

// skipOnWindows skips the tests that hand real paths to the container
// runtime: the bind mount source:target syntax cannot carry "C:\...", so the
// inspection refuses such paths (v1 ships for Linux; Windows is v2.0).
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("inspection of local paths needs a drive-letter-aware bind mount (v2.0)")
	}
}

// resolvedTempDir is a temporary directory without symbolic links in its
// path (on macOS /var is a link to /private/var), as ResolvePaths returns.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

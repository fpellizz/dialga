package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// fakeSQL is an instance in memory: database states, tasks, and the
// statements run against it.
type fakeSQL struct {
	states  map[string]string
	active  map[string]int // by database, "" for all
	tasks   map[int]mssql.Task
	report  mssql.DatabaseReport
	ran     []string
	execErr error
	// after a task is started, Get answers these lifecycles in turn
	progress []mssql.Lifecycle
	reads    int
}

func (f *fakeSQL) DatabaseState(_ context.Context, db string) (string, error) {
	return f.states[db], nil
}

func (f *fakeSQL) ActiveTasks(_ context.Context, db string) (int, error) { return f.active[db], nil }

func (f *fakeSQL) StartTask(_ context.Context, stmt, db string) (mssql.Task, error) {
	f.ran = append(f.ran, stmt)
	if f.execErr != nil {
		return mssql.Task{}, f.execErr
	}
	if f.tasks == nil {
		f.tasks = map[int]mssql.Task{}
	}
	task := mssql.Task{ID: 100 + len(f.tasks), Type: "RESTORE_DB", Database: db, Lifecycle: mssql.Created}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeSQL) Exec(_ context.Context, stmt string) error {
	f.ran = append(f.ran, stmt)
	return f.execErr
}

func (f *fakeSQL) DatabaseReport(_ context.Context, db string) (mssql.DatabaseReport, error) {
	if f.states[db] == "" {
		return mssql.DatabaseReport{}, fmt.Errorf("%s: %w", db, mssql.ErrNoDatabase)
	}
	return f.report, nil
}

func (f *fakeSQL) List(context.Context, mssql.TaskFilter) ([]mssql.Task, error) { return nil, nil }

func (f *fakeSQL) Get(_ context.Context, id int) (mssql.Task, error) {
	task, ok := f.tasks[id]
	if !ok {
		return mssql.Task{}, mssql.ErrTaskNotFound
	}
	if len(f.progress) > 0 {
		task.Lifecycle = f.progress[min(f.reads, len(f.progress)-1)]
		f.reads++
	}
	return task, nil
}

// fakeBucket holds object sizes by key.
type fakeBucket map[string]int64

func (b fakeBucket) Size(_ context.Context, key string) (int64, error) {
	if size, ok := b[key]; ok {
		return size, nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b fakeBucket) Keys(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range b {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

var testTarget = config.Target{
	AccountID: "123456789012", Region: "eu-west-1", AWSProfile: "default",
	Instance: "dev-sqlserver", Bucket: "dev-backups",
	Prefixes: config.Prefixes{Import: "import", Export: "export"},
	SQL:      config.SQL{User: "admin", Password: "env:PW"},
}

func newSession(f *fakeSQL, b fakeBucket) *Session {
	return &Session{
		Name: "dev-sql", Target: testTarget, SQL: f, Bucket: b,
		Now: func() time.Time { return time.Date(2026, 10, 16, 9, 15, 0, 0, time.FixedZone("CEST", 7200)) },
	}
}

// verdicts renders the checks as "step:status" for comparison.
func verdicts(p *Prepared) string {
	var parts []string
	for _, c := range p.Checks {
		parts = append(parts, c.Step+":"+string(c.Status))
	}
	return strings.Join(parts, " ")
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		op   string
		p    Params
		want string // "" = valid
	}{
		{"restore", OpRestore, Params{Database: "sales", Key: "import/sales.bak"}, ""},
		{"restore striped", OpRestore, Params{Database: "sales", Key: "sales_part*.bak"}, ""},
		{"restore without key", OpRestore, Params{Database: "sales"}, "needs the S3 key"},
		{"system database", OpRestore, Params{Database: "msdb", Key: "a.bak"}, "system database"},
		{"bad database", OpDrop, Params{Database: "x;drop"}, "invalid database name"},
		{"bad key", OpRestore, Params{Database: "sales", Key: "it's.bak"}, "invalid S3 key"},
		{"parent reference", OpRestore, Params{Database: "sales", Key: "import/../x.bak"}, "parent directory"},
		{"other bucket", OpRestore, Params{Database: "sales", Key: "s3://other/x.bak"}, "the bucket must be dev-backups"},
		{"bucket without key", OpRestore, Params{Database: "sales", Key: "s3://dev-backups/"}, "names no object"},
		{"export default key", OpExport, Params{Database: "sales"}, ""},
		{"export star", OpExport, Params{Database: "sales", Key: "export/x*.bak"}, "no * allowed"},
		{"finish", OpFinish, Params{Database: "sales"}, ""},
		{"cancel", OpCancel, Params{TaskID: 7}, ""},
		{"cancel without id", OpCancel, Params{}, "invalid task id"},
		{"unknown", "rename", Params{}, "unknown operation"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.op, tc.p, testTarget)
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveKey(t *testing.T) {
	tests := []struct {
		in, key string
		bare    bool
	}{
		{"sales.bak", "import/sales.bak", true},
		{"other/sales.bak", "other/sales.bak", false},
		{"s3://dev-backups/deep/sales.bak", "deep/sales.bak", false},
		{"s3://dev-backups/sales.bak", "sales.bak", false},
	}
	for _, tc := range tests {
		key, bare, err := resolveKey(tc.in, "import", "dev-backups")
		if err != nil || key != tc.key || bare != tc.bare {
			t.Errorf("resolveKey(%q) = %q, %v, %v; want %q, %v", tc.in, key, bare, err, tc.key, tc.bare)
		}
	}
}

func TestRestorePreflight(t *testing.T) {
	objects := fakeBucket{"import/sales.bak": 3 << 20, "root.bak": 10, "import/p1.bak": 1, "import/p2.bak": 1}
	many := fakeBucket{}
	for i := range 11 {
		many[fmt.Sprintf("import/m%02d.bak", i)] = 1
	}
	tests := []struct {
		name    string
		states  map[string]string
		active  int
		bucket  fakeBucket
		key     string
		opts    mssql.RestoreOptions
		want    string
		wantKey string
	}{
		{"ready", nil, 0, objects, "sales.bak", mssql.RestoreOptions{}, "source:ok database:ok tasks:ok", "import/sales.bak"},
		{"bare name at the root", nil, 0, objects, "root.bak", mssql.RestoreOptions{}, "source:ok database:ok tasks:ok", "root.bak"},
		{"object missing", nil, 0, objects, "import/nope.bak", mssql.RestoreOptions{}, "source:failed database:ok tasks:ok", "import/nope.bak"},
		{"database exists", map[string]string{"sales": "ONLINE"}, 0, objects, "sales.bak", mssql.RestoreOptions{}, "source:ok database:failed tasks:ok", "import/sales.bak"},
		{"tasks running", nil, 2, objects, "sales.bak", mssql.RestoreOptions{}, "source:ok database:ok tasks:warning", "import/sales.bak"},
		{"striped", nil, 0, objects, "import/p*.bak", mssql.RestoreOptions{}, "source:ok database:ok tasks:ok", "import/p*.bak"},
		{"striped none", nil, 0, objects, "import/q*.bak", mssql.RestoreOptions{}, "source:failed database:ok tasks:ok", "import/q*.bak"},
		{"striped too many", nil, 0, many, "import/m*.bak", mssql.RestoreOptions{}, "source:failed database:ok tasks:ok", "import/m*.bak"},
		{"differential", map[string]string{"sales": "RESTORING"}, 0, objects, "sales.bak", mssql.RestoreOptions{Differential: true}, "source:ok database:ok tasks:ok", "import/sales.bak"},
		{"differential missing", nil, 0, objects, "sales.bak", mssql.RestoreOptions{Differential: true}, "source:ok database:failed tasks:ok", "import/sales.bak"},
		{"differential online", map[string]string{"sales": "ONLINE"}, 0, objects, "sales.bak", mssql.RestoreOptions{Differential: true}, "source:ok database:failed tasks:ok", "import/sales.bak"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSQL{states: tc.states, active: map[string]int{"": tc.active}}
			p, err := newSession(f, tc.bucket).Restore(context.Background(), "sales", tc.key, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := verdicts(p); got != tc.want {
				t.Fatalf("checks %s, want %s (%+v)", got, tc.want, p.Checks)
			}
			if p.Ready() != !strings.Contains(tc.want, "failed") || p.Params.Key != tc.wantKey {
				t.Fatalf("ready %v, key %q", p.Ready(), p.Params.Key)
			}
			want := mssql.RestoreDatabase("sales", mssql.S3ARN("dev-backups", tc.wantKey), tc.opts)
			if len(p.Plan.Actions) != 1 || p.Plan.Actions[0].Statement != want || p.Plan.Confirmation() != "" {
				t.Fatalf("plan %+v", p.Plan)
			}
			if len(f.ran) != 0 {
				t.Fatalf("a builder ran %v", f.ran)
			}
		})
	}
}

func TestRestoreSourceDetail(t *testing.T) {
	p, err := newSession(&fakeSQL{}, fakeBucket{"import/sales.bak": 3 << 20}).
		Restore(context.Background(), "sales", "sales.bak", mssql.RestoreOptions{})
	if err != nil || p.Checks[0].Detail != "s3://dev-backups/import/sales.bak (3.0 MiB)" {
		t.Fatalf("err %v, checks %+v", err, p.Checks)
	}
	if p.Plan.Operation != OpRestore || p.Plan.Target != "dev-sql" || p.Plan.Instance != "dev-sqlserver" {
		t.Fatalf("plan %+v", p.Plan)
	}
}

func TestExport(t *testing.T) {
	online := map[string]string{"sales": "ONLINE"}
	tests := []struct {
		name    string
		states  map[string]string
		bucket  fakeBucket
		key     string
		want    string
		wantKey string
	}{
		{"default key in UTC", online, fakeBucket{}, "", "database:ok destination:ok tasks:ok", "export/sales_20261016-071500.bak"},
		{"bare name", online, fakeBucket{}, "copy.bak", "database:ok destination:ok tasks:ok", "export/copy.bak"},
		{"never overwrites", online, fakeBucket{"export/copy.bak": 1}, "export/copy.bak", "database:ok destination:failed tasks:ok", "export/copy.bak"},
		{"database missing", nil, fakeBucket{}, "", "database:failed destination:ok tasks:ok", "export/sales_20261016-071500.bak"},
		{"database restoring", map[string]string{"sales": "RESTORING"}, fakeBucket{}, "", "database:failed destination:ok tasks:ok", "export/sales_20261016-071500.bak"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newSession(&fakeSQL{states: tc.states}, tc.bucket).Export(context.Background(), "sales", tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got := verdicts(p); got != tc.want || p.Params.Key != tc.wantKey {
				t.Fatalf("checks %s, key %q", got, p.Params.Key)
			}
			if want := mssql.BackupDatabase("sales", mssql.S3ARN("dev-backups", tc.wantKey)); p.Plan.Actions[0].Statement != want {
				t.Fatalf("statement %q", p.Plan.Actions[0].Statement)
			}
		})
	}
}

func TestFinishCancelDrop(t *testing.T) {
	f := &fakeSQL{
		states: map[string]string{"restoring": "RESTORING", "online": "ONLINE"},
		active: map[string]int{"online": 1},
		tasks: map[int]mssql.Task{
			5: {ID: 5, Type: "RESTORE_DB", Database: "x", Lifecycle: mssql.InProgress, Percent: 40},
			6: {ID: 6, Type: "BACKUP_DB", Database: "x", Lifecycle: mssql.Success},
			7: {ID: 7, Type: "BACKUP_DB", Database: "x", Lifecycle: mssql.CancelRequested},
		},
	}
	s := newSession(f, fakeBucket{})
	ctx := context.Background()
	tests := []struct {
		name  string
		build func() (*Prepared, error)
		want  string
		stmt  string
	}{
		{"finish", func() (*Prepared, error) { return s.Finish(ctx, "restoring") }, "database:ok", mssql.FinishRestore("restoring")},
		{"finish online", func() (*Prepared, error) { return s.Finish(ctx, "online") }, "database:failed", mssql.FinishRestore("online")},
		{"finish missing", func() (*Prepared, error) { return s.Finish(ctx, "nope") }, "database:failed", mssql.FinishRestore("nope")},
		{"cancel running", func() (*Prepared, error) { return s.Cancel(ctx, 5) }, "task:ok", mssql.CancelTask(5)},
		{"cancel ended", func() (*Prepared, error) { return s.Cancel(ctx, 6) }, "task:failed", mssql.CancelTask(6)},
		{"cancel requested", func() (*Prepared, error) { return s.Cancel(ctx, 7) }, "task:warning", mssql.CancelTask(7)},
		{"cancel unknown", func() (*Prepared, error) { return s.Cancel(ctx, 99) }, "task:failed", mssql.CancelTask(99)},
		{"drop", func() (*Prepared, error) { return s.Drop(ctx, "restoring") }, "database:ok tasks:ok", mssql.DropDatabase("restoring")},
		{"drop busy", func() (*Prepared, error) { return s.Drop(ctx, "online") }, "database:ok tasks:warning", mssql.DropDatabase("online")},
		{"drop missing", func() (*Prepared, error) { return s.Drop(ctx, "nope") }, "database:failed tasks:ok", mssql.DropDatabase("nope")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.build()
			if err != nil {
				t.Fatal(err)
			}
			if got := verdicts(p); got != tc.want || p.Plan.Actions[0].Statement != tc.stmt {
				t.Fatalf("checks %s, statement %q", got, p.Plan.Actions[0].Statement)
			}
		})
	}
	drop, _ := s.Drop(ctx, "online")
	if drop.Plan.Subject != "online" || drop.Plan.Confirmation() != "online" || drop.Plan.Actions[0].Risk != core.Destructive {
		t.Fatalf("drop plan %+v", drop.Plan)
	}
	if cancel, _ := s.Cancel(ctx, 5); cancel.Plan.Actions[0].Title != "Cancel task 5 (RESTORE_DB of x)" {
		t.Fatalf("cancel title %q", cancel.Plan.Actions[0].Title)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	s := newSession(&fakeSQL{}, fakeBucket{})
	for _, op := range []string{"rename", OpRestore, OpDrop} {
		if _, err := s.Build(context.Background(), op, Params{Database: "x;y"}); err == nil {
			t.Errorf("%s: no error", op)
		}
	}
}

func TestExecuteRestoreWaits(t *testing.T) {
	f := &fakeSQL{progress: []mssql.Lifecycle{mssql.Created, mssql.InProgress, mssql.Success}}
	s := newSession(f, fakeBucket{"import/sales.bak": 1})
	p, err := s.Restore(context.Background(), "sales", "sales.bak", mssql.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	var seen []mssql.Lifecycle
	task, err := p.Execute(context.Background(), ExecOptions{
		Wait: true, Interval: time.Millisecond,
		OnStep: func(e core.Event) { events = append(events, e.Step+":"+string(e.Status)+":"+e.Detail) },
		OnTask: func(t mssql.Task) { seen = append(seen, t.Lifecycle) },
	})
	if err != nil || task == nil || task.ID != 100 || task.Lifecycle != mssql.Success {
		t.Fatalf("task %+v, err %v", task, err)
	}
	want := "restore:running: restore:ok:task 100 created wait:running: wait:ok:task completed"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events %s\nwant   %s", got, want)
	}
	if fmt.Sprint(seen) != "[CREATED IN_PROGRESS SUCCESS]" || len(f.ran) != 1 || f.ran[0] != p.Plan.Actions[0].Statement {
		t.Fatalf("seen %v, ran %v", seen, f.ran)
	}
}

func TestExecuteTaskErrorAndNoWait(t *testing.T) {
	f := &fakeSQL{states: map[string]string{"sales": "ONLINE"}, progress: []mssql.Lifecycle{mssql.Error}}
	s := newSession(f, fakeBucket{})
	p, _ := s.Export(context.Background(), "sales", "")
	var last core.Event
	task, err := p.Execute(context.Background(), ExecOptions{Wait: true, Interval: time.Millisecond,
		OnStep: func(e core.Event) { last = e }})
	if err != nil || task.Lifecycle != mssql.Error || last.Step != "wait" || last.Status != core.Failed {
		t.Fatalf("task %+v, err %v, last %+v", task, err, last)
	}
	p, _ = s.Export(context.Background(), "sales", "export/other.bak")
	task, err = p.Execute(context.Background(), ExecOptions{})
	if err != nil || task == nil || task.Lifecycle != mssql.Created {
		t.Fatalf("no wait: task %+v, err %v", task, err)
	}
}

func TestExecuteGuards(t *testing.T) {
	ctx := context.Background()
	f := &fakeSQL{states: map[string]string{"sales": "ONLINE"}}
	s := newSession(f, fakeBucket{})

	notReady, _ := s.Finish(ctx, "sales")
	if _, err := notReady.Execute(ctx, ExecOptions{}); !errors.Is(err, ErrNotReady) || len(f.ran) != 0 {
		t.Fatalf("not ready: err %v, ran %v", err, f.ran)
	}
	drop, _ := s.Drop(ctx, "sales")
	if _, err := drop.Execute(ctx, ExecOptions{Confirm: "Sales"}); !errors.Is(err, core.ErrNotConfirmed) || len(f.ran) != 0 {
		t.Fatalf("wrong confirmation: err %v, ran %v", err, f.ran)
	}
	if _, err := drop.Execute(ctx, ExecOptions{Confirm: "sales"}); err != nil || len(f.ran) != 1 {
		t.Fatalf("right confirmation: err %v, ran %v", err, f.ran)
	}

	protected := newSession(f, fakeBucket{})
	protected.Target.Protected = true
	protectedDrop, _ := protected.Drop(ctx, "sales")
	if protectedDrop.Plan.Confirmation() != "dev-sql" {
		t.Fatalf("protected confirmation %q", protectedDrop.Plan.Confirmation())
	}
	if _, err := protectedDrop.Execute(ctx, ExecOptions{Confirm: "sales"}); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("protected with the database name: %v", err)
	}

	f.execErr = errors.New("Database 'sales' is in use")
	var events []core.Event
	if _, err := drop.Execute(ctx, ExecOptions{Confirm: "sales", OnStep: func(e core.Event) { events = append(events, e) }}); err == nil ||
		events[len(events)-1].Status != core.Failed {
		t.Fatalf("failing action: err %v, events %+v", err, events)
	}
}

func TestDigest(t *testing.T) {
	s := newSession(&fakeSQL{states: map[string]string{"sales": "ONLINE"}}, fakeBucket{})
	a, _ := s.Export(context.Background(), "sales", "export/a.bak")
	b, _ := s.Export(context.Background(), "sales", "export/a.bak")
	c, _ := s.Export(context.Background(), "sales", "export/c.bak")
	if a.Digest() != b.Digest() || a.Digest() == c.Digest() || len(a.Digest()) != 32 {
		t.Fatalf("digests %s %s %s", a.Digest(), b.Digest(), c.Digest())
	}
}

func TestPost(t *testing.T) {
	online := mssql.DatabaseInfo{Name: "sales", State: "ONLINE", CompatibilityLevel: 140}
	tests := []struct {
		name   string
		report mssql.DatabaseReport
		ok     bool
		want   string
	}{
		{"clean", mssql.DatabaseReport{Database: online, Access: true}, true, "database:ok access:ok orphans:ok windows:ok"},
		{"orphans and windows", mssql.DatabaseReport{Database: online, Access: true, OrphanUsers: []string{"app"}, WindowsPrincipals: 2},
			true, "database:ok access:ok orphans:warning windows:warning"},
		{"no access", mssql.DatabaseReport{Database: online}, false, "database:ok access:failed"},
		{"restoring", mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: "sales", State: "RESTORING"}}, false, "database:failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(&fakeSQL{states: map[string]string{"sales": "ONLINE"}, report: tc.report}, fakeBucket{})
			r, err := s.Post(context.Background(), "sales")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range r.Findings {
				got = append(got, f.Step+":"+string(f.Status))
			}
			if r.OK != tc.ok || strings.Join(got, " ") != tc.want || r.Target != "dev-sql" {
				t.Fatalf("ok %v, findings %v (%+v)", r.OK, got, r.Findings)
			}
		})
	}
	s := newSession(&fakeSQL{states: map[string]string{"sales": "ONLINE"},
		report: mssql.DatabaseReport{Database: online, Access: true, OrphanUsers: []string{"app", "etl"}}}, fakeBucket{})
	r, _ := s.Post(context.Background(), "sales")
	if !strings.Contains(r.Findings[2].Detail, "2 SQL user(s) without a login on the instance: app, etl") ||
		!strings.Contains(r.Findings[2].Detail, "ALTER USER [u] WITH LOGIN = [u]") {
		t.Fatalf("orphans detail %q", r.Findings[2].Detail)
	}
	if _, err := s.Post(context.Background(), "nope"); !errors.Is(err, mssql.ErrNoDatabase) {
		t.Fatalf("missing database: %v", err)
	}
	if _, err := s.Post(context.Background(), "tempdb"); err == nil {
		t.Fatal("system database accepted")
	}
}

func TestGlobAndBytes(t *testing.T) {
	re := globPattern("import/a+b*.bak")
	for key, want := range map[string]bool{
		"import/a+b1.bak": true, "import/a+b/x/2.bak": true, "import/aab1.bak": false, "import/a+b1.bakx": false,
	} {
		if re.MatchString(key) != want {
			t.Errorf("%q: match %v", key, !want)
		}
	}
	for n, want := range map[int64]string{512: "512 B", 2048: "2.0 KiB", 3 << 20: "3.0 MiB", 5 << 30: "5.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

//go:build integration

package ops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcmssql "github.com/testcontainers/testcontainers-go/modules/mssql"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// The integration tests run the operations against SQL Server 2022 in a
// container, with the rds_* procedures mocked (internal/testdata/mock-rds.sql)
// and the bucket in memory. They connect as "god", the non-sysadmin login of
// the mock that plays the RDS master user; "sa" prepares the fixtures.
var (
	sa, god *sql.DB
	// the image the CI pulls; override with DIALGA_MSSQL_IMAGE
	mssqlImage = "mcr.microsoft.com/mssql/server:2022-latest"
)

// extraMock completes the mock for these tests: rds_finish_restore (absent
// from mock-rds.sql) and a database left RESTORING, as after a full restore
// with @with_norecovery = 1.
const extraMock = `USE msdb;
GO
CREATE PROCEDURE dbo.rds_finish_restore @db_name sysname AS
BEGIN
  DECLARE @s nvarchar(400) = N'RESTORE DATABASE ' + QUOTENAME(@db_name) + N' WITH RECOVERY';
  EXEC (@s);
END
GO
GRANT EXECUTE ON dbo.rds_finish_restore TO god;
GO
USE master;
RESTORE DATABASE staged_db FROM DISK = N'/bak/app_db_2022.bak' WITH NORECOVERY,
  MOVE N'app_db' TO N'/var/opt/mssql/data/staged_db.mdf',
  MOVE N'app_db_log' TO N'/var/opt/mssql/data/staged_db_log.ldf';
GO
CREATE DATABASE sa_only_db;
GO`

func TestMain(m *testing.M) {
	os.Exit(runWithContainer(m))
}

func runWithContainer(m *testing.M) int {
	ctx := context.Background()
	if img := os.Getenv("DIALGA_MSSQL_IMAGE"); img != "" {
		mssqlImage = img
	}
	saPassword := throwawayPassword()
	ctr, err := tcmssql.Run(ctx, mssqlImage, tcmssql.WithAcceptEULA(), tcmssql.WithPassword(saPassword))
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "terminate:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "SQL Server container:", err)
		return 1
	}
	if err := setUp(ctx, ctr, saPassword); err != nil {
		fmt.Fprintln(os.Stderr, "fixtures:", err)
		return 1
	}
	defer func() { _ = sa.Close(); _ = god.Close() }()
	return m.Run()
}

// throwawayPassword meets the SQL Server policy and lives only in memory.
func throwawayPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "Dg1!" + hex.EncodeToString(b)
}

func setUp(ctx context.Context, ctr *tcmssql.MSSQLServerContainer, saPassword string) error {
	code, out, err := ctr.Exec(ctx, []string{"sh", "-c", "mkdir -p /bak && chown mssql /bak"}, tcexec.WithUser("root"))
	if err != nil || code != 0 {
		msg, _ := io.ReadAll(out)
		return fmt.Errorf("mkdir /bak: exit %d, %v: %s", code, err, msg)
	}
	hostPort, err := ctr.PortEndpoint(ctx, "1433/tcp", "")
	if err != nil {
		return err
	}
	if sa, err = openPlain(ctx, hostPort, "sa", saPassword); err != nil {
		return err
	}
	mock, err := os.ReadFile(filepath.Join("..", "testdata", "mock-rds.sql"))
	if err != nil {
		return err
	}
	godPassword := throwawayPassword()
	if err := runScript(ctx, sa, string(mock)+"\nGO\n"+extraMock); err != nil {
		return err
	}
	// The mock creates god with a fixed password: replace it with one that
	// exists only in this process.
	if _, err := sa.ExecContext(ctx, `DECLARE @s nvarchar(400) =
		N'ALTER LOGIN god WITH PASSWORD = ' + QUOTENAME(@pw, '''') + N';'; EXEC (@s);`,
		sql.Named("pw", godPassword)); err != nil {
		return err
	}
	god, err = openPlain(ctx, hostPort, "god", godPassword)
	if err != nil {
		return err
	}
	god.SetMaxOpenConns(1) // like mssql.Open: one connection per session
	return nil
}

// openPlain opens a test-only connection without TLS: the container has a
// self-signed certificate and no RDS name. Production code uses mssql.Open.
func openPlain(ctx context.Context, hostPort, user, password string) (*sql.DB, error) {
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(user, password), Host: hostPort}
	u.RawQuery = url.Values{"database": {"master"}, "encrypt": {"disable"}}.Encode()
	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
	// "Recovery is complete" can come a moment before logins are accepted.
	for i := 0; ; i++ {
		if err = db.PingContext(ctx); err == nil || i == 30 {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s login: %w", user, err)
	}
	return db, nil
}

// runScript runs a script one batch per GO, on one connection so that USE
// carries over from a batch to the next.
func runScript(ctx context.Context, db *sql.DB, script string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var batch []string
	flush := func() error {
		text := strings.TrimSpace(strings.Join(batch, "\n"))
		batch = batch[:0]
		if text == "" {
			return nil
		}
		if _, err := conn.ExecContext(ctx, text); err != nil {
			return fmt.Errorf("batch %q: %w", oneLineSQL(text), err)
		}
		return nil
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "GO") {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		batch = append(batch, line)
	}
	return flush()
}

func oneLineSQL(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// goldenSession works on the bucket and names of the golden files.
func goldenSession(bucket fakeBucket) *Session {
	t := testTarget
	t.Bucket = "golden-bucket"
	return &Session{Name: "it", Target: t, SQL: mssql.Session{DB: god}, Bucket: bucket,
		Now: func() time.Time { return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) }}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "mssql", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

// mustPlan builds a plan that must pass its pre-flight with the golden statement.
func mustPlan(t *testing.T, p *Prepared, err error, goldenFile string) *Prepared {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready() {
		t.Fatalf("%s: pre-flight failed: %+v", p.Plan.Operation, p.Checks)
	}
	if got, want := p.Plan.Actions[0].Statement, golden(t, goldenFile); got != want {
		t.Fatalf("%s: statement differs from mssql-import.sh\n got: %q\nwant: %q", p.Plan.Operation, got, want)
	}
	return p
}

func setTask(t *testing.T, id int, lifecycle mssql.Lifecycle, pct int) {
	t.Helper()
	if _, err := sa.Exec(`UPDATE msdb.dbo.mock_tasks SET lifecycle = @l, pct = @p, last_updated = GETDATE()
		WHERE task_id = @id`, sql.Named("l", string(lifecycle)), sql.Named("p", pct), sql.Named("id", id)); err != nil {
		t.Fatal(err)
	}
}

func dbState(t *testing.T, db string) string {
	t.Helper()
	state, err := mssql.Session{DB: sa}.DatabaseState(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func countTasks(t *testing.T) int {
	t.Helper()
	var n int
	if err := sa.QueryRow(`SELECT COUNT(*) FROM msdb.dbo.mock_tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationGoldenLifecycle runs every plan against the mock, with the
// names of the golden files: restore (plain, NoRecovery, differential),
// finish, export with the default key, cancel and drop.
func TestIntegrationGoldenLifecycle(t *testing.T) {
	ctx := context.Background()
	s := goldenSession(fakeBucket{"import/golden.bak": 1, "import/golden_full.bak": 1, "import/golden_diff.bak": 1})

	// Restore, followed until the (mock) task succeeds.
	p, err := s.Restore(ctx, "golden_db", "golden.bak", mssql.RestoreOptions{})
	p = mustPlan(t, p, err, "restore.sql")
	var seen []string
	task, err := p.Execute(ctx, ExecOptions{Wait: true, Interval: 20 * time.Millisecond, OnTask: func(task mssql.Task) {
		seen = append(seen, string(task.Lifecycle))
		if task.Lifecycle == mssql.Created {
			setTask(t, task.ID, mssql.Success, 100)
		}
	}})
	if err != nil || task.Type != "RESTORE_DB" || task.Database != "golden_db" || task.Lifecycle != mssql.Success ||
		task.S3Object != "arn:aws:s3:::golden-bucket/import/golden.bak" {
		t.Fatalf("restore task %+v, err %v", task, err)
	}
	if strings.Join(seen, " ") != "CREATED SUCCESS" {
		t.Fatalf("task updates %v", seen)
	}

	p, err = s.Restore(ctx, "golden_db", "import/golden_full.bak", mssql.RestoreOptions{NoRecovery: true})
	p = mustPlan(t, p, err, "restore-norecovery.sql")
	if task, err := p.Execute(ctx, ExecOptions{}); err != nil || task.Type != "RESTORE_DB" || task.Lifecycle != mssql.Created {
		t.Fatalf("norecovery task %+v, err %v", task, err)
	}

	// The mock restores nothing: leave golden_db RESTORING for real.
	if _, err := sa.Exec(`RESTORE DATABASE golden_db FROM DISK = N'/bak/app_db_2022.bak' WITH NORECOVERY,
		MOVE N'app_db' TO N'/var/opt/mssql/data/golden_db.mdf',
		MOVE N'app_db_log' TO N'/var/opt/mssql/data/golden_db_log.ldf'`); err != nil {
		t.Fatal(err)
	}
	p, err = s.Restore(ctx, "golden_db", "s3://golden-bucket/import/golden_diff.bak", mssql.RestoreOptions{Differential: true})
	p = mustPlan(t, p, err, "restore-differential.sql")
	if _, err := p.Execute(ctx, ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	p, err = s.Finish(ctx, "golden_db")
	p = mustPlan(t, p, err, "finish.sql")
	var finished core.Event
	if _, err := p.Execute(ctx, ExecOptions{OnStep: func(e core.Event) { finished = e }}); err != nil ||
		finished.Detail != "golden_db is ONLINE" || dbState(t, "golden_db") != "ONLINE" {
		t.Fatalf("finish: err %v, last event %+v", err, finished)
	}

	// Export with the default key; the mock task gets id 42 like cancel.sql.
	if _, err := sa.Exec(`DBCC CHECKIDENT ('msdb.dbo.mock_tasks', RESEED, 41)`); err != nil {
		t.Fatal(err)
	}
	p, err = s.Export(ctx, "golden_db", "")
	p = mustPlan(t, p, err, "backup.sql")
	task, err = p.Execute(ctx, ExecOptions{})
	if err != nil || task.ID != 42 || task.Type != "BACKUP_DB" || task.Database != "golden_db" ||
		task.S3Object != "arn:aws:s3:::golden-bucket/export/golden_db_20261016-000000.bak" {
		t.Fatalf("export task %+v, err %v", task, err)
	}

	p, err = s.Cancel(ctx, 42)
	p = mustPlan(t, p, err, "cancel.sql")
	if _, err := p.Execute(ctx, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, err := (mssql.Session{DB: god}).Get(ctx, 42); err != nil || got.Lifecycle != mssql.Cancelled {
		t.Fatalf("cancelled task %+v, err %v", got, err)
	}
	// Cancelling it again is refused by the pre-flight.
	if again, err := s.Cancel(ctx, 42); err != nil || again.Ready() {
		t.Fatalf("second cancel: err %v, checks %+v", err, again.Checks)
	}

	// Drop: the wrong text changes nothing, the database name drops it.
	p, err = s.Drop(ctx, "golden_db")
	p = mustPlan(t, p, err, "drop.sql")
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "GOLDEN_DB"}); !errors.Is(err, core.ErrNotConfirmed) || dbState(t, "golden_db") == "" {
		t.Fatalf("wrong confirmation: err %v", err)
	}
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "golden_db"}); err != nil || dbState(t, "golden_db") != "" {
		t.Fatalf("drop: err %v, state %q", err, dbState(t, "golden_db"))
	}
}

func TestIntegrationRestoreRefusedWhenDatabaseExists(t *testing.T) {
	ctx := context.Background()
	s := goldenSession(fakeBucket{"import/app.bak": 1})
	before := countTasks(t)
	p, err := s.Restore(ctx, "app_db", "app.bak", mssql.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready() || p.Checks[1].Step != "database" || p.Checks[1].Status != check.Failed ||
		!strings.Contains(p.Checks[1].Detail, "app_db already exists (ONLINE)") {
		t.Fatalf("checks %+v", p.Checks)
	}
	if _, err := p.Execute(ctx, ExecOptions{}); !errors.Is(err, ErrNotReady) || countTasks(t) != before {
		t.Fatalf("execute: err %v, tasks %d -> %d", err, before, countTasks(t))
	}
	// Finishing an online database is refused too.
	if f, err := s.Finish(ctx, "app_db"); err != nil || f.Ready() {
		t.Fatalf("finish online: err %v, checks %+v", err, f.Checks)
	}
}

func TestIntegrationProtectedTargetConfirmation(t *testing.T) {
	ctx := context.Background()
	if _, err := sa.Exec(`CREATE DATABASE protected_db`); err != nil {
		t.Fatal(err)
	}
	s := goldenSession(fakeBucket{})
	s.Target.Protected = true
	p, err := s.Drop(ctx, "protected_db")
	if err != nil || !p.Ready() || p.Plan.Confirmation() != "it" {
		t.Fatalf("err %v, confirmation %q", err, p.Plan.Confirmation())
	}
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "protected_db"}); !errors.Is(err, core.ErrNotConfirmed) || dbState(t, "protected_db") == "" {
		t.Fatalf("database name on a protected target: err %v", err)
	}
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "it"}); err != nil || dbState(t, "protected_db") != "" {
		t.Fatalf("target name: err %v", err)
	}
}

func TestIntegrationExportDefaultKeyNow(t *testing.T) {
	s := goldenSession(fakeBucket{})
	s.Now = nil
	p, err := s.Export(context.Background(), "app_db", "")
	if err != nil || !p.Ready() {
		t.Fatalf("err %v, checks %+v", err, p.Checks)
	}
	if !regexp.MustCompile(`^export/app_db_\d{8}-\d{6}\.bak$`).MatchString(p.Params.Key) {
		t.Fatalf("default key %q", p.Params.Key)
	}
	// The same key taken: never overwritten.
	s.Bucket = fakeBucket{p.Params.Key: 1}
	if again, _ := s.Export(context.Background(), "app_db", p.Params.Key); again.Ready() {
		t.Fatalf("existing key accepted: %+v", again.Checks)
	}
}

func TestIntegrationStartTaskReadsTheAnswer(t *testing.T) {
	ctx := context.Background()
	admin := mssql.Session{DB: sa}
	// An answer with a task_id column: that task, even if it is not the newest.
	var oldest int
	if err := sa.QueryRow(`SELECT MIN(task_id) FROM msdb.dbo.mock_tasks`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	task, err := admin.StartTask(ctx, `SELECT TOP (1) task_id, N'x' AS other FROM msdb.dbo.rds_fn_task_status(NULL, 0) ORDER BY task_id`, "unused")
	if err != nil || task.ID != oldest {
		t.Fatalf("task %+v, err %v (want %d)", task, err, oldest)
	}
	// No answer: the newest task of the database after the statement.
	task, err = admin.StartTask(ctx, `INSERT msdb.dbo.mock_tasks(task_type, database_name, pct, lifecycle, last_updated, created_at)
		VALUES ('RESTORE_DB', 'fallback_db', 0, 'CREATED', GETDATE(), GETDATE())`, "fallback_db")
	if err != nil || task.Database != "fallback_db" || task.Lifecycle != mssql.Created {
		t.Fatalf("fallback task %+v, err %v", task, err)
	}
	// Neither: an error that says where to look.
	if _, err := admin.StartTask(ctx, `SELECT 1 AS n`, "ghost_db"); err == nil || !strings.Contains(err.Error(), "dialga status --db ghost_db") {
		t.Fatalf("no task: %v", err)
	}
	// An error raised by the procedure surfaces.
	if _, err := admin.StartTask(ctx, `RAISERROR(N'mock failure', 16, 1)`, "x"); err == nil || !strings.Contains(err.Error(), "mock failure") {
		t.Fatalf("raised error: %v", err)
	}
}

func TestIntegrationPost(t *testing.T) {
	ctx := context.Background()
	s := goldenSession(fakeBucket{})
	s.Target.SQL.User = "god"

	r, err := s.Post(ctx, "app_db")
	if err != nil {
		t.Fatal(err)
	}
	var findings []string
	for _, f := range r.Findings {
		findings = append(findings, f.Step+":"+string(f.Status))
	}
	if !r.OK || strings.Join(findings, " ") != "database:ok access:ok orphans:warning windows:ok" {
		t.Fatalf("findings %v: %+v", findings, r.Findings)
	}
	if r.Database.Name != "app_db" || r.Database.State != "ONLINE" || r.Database.CompatibilityLevel != 160 ||
		r.Database.RecoveryModel == "" || r.Database.Owner != "sa" || r.Database.Collation == "" {
		t.Fatalf("database %+v", r.Database)
	}
	if len(r.Files) != 2 || r.Files[0].Type != "ROWS" || r.Files[1].Type != "LOG" || r.Files[0].SizeMB <= 0 ||
		r.Files[0].MaxSizeMB != nil || !strings.HasSuffix(r.Files[0].PhysicalName, ".mdf") {
		t.Fatalf("files %+v", r.Files)
	}
	if strings.Join(r.OrphanUsers, ",") != "appuser" || r.WindowsPrincipals != 0 {
		t.Fatalf("orphans %v, windows %d", r.OrphanUsers, r.WindowsPrincipals)
	}
	objects := map[string]int{}
	for _, o := range r.Objects {
		objects[o.Type] = o.Count
	}
	if objects["USER_TABLE"] != 1 || objects["VIEW"] != 1 || objects["PRIMARY_KEY_CONSTRAINT"] != 1 {
		t.Fatalf("objects %+v", r.Objects)
	}

	// A database the login cannot enter, like a restored one owned by rdsa.
	r, err = s.Post(ctx, "sa_only_db")
	if err != nil || r.OK || r.Access || r.Findings[1].Step != "access" || !strings.Contains(r.Findings[1].Detail, "god cannot enter sa_only_db") {
		t.Fatalf("no access: err %v, report %+v", err, r)
	}
	// A database left RESTORING.
	r, err = s.Post(ctx, "staged_db")
	if err != nil || r.OK || r.Database.State != "RESTORING" || len(r.Findings) != 1 ||
		!strings.Contains(r.Findings[0].Detail, "dialga finish staged_db") {
		t.Fatalf("restoring: err %v, report %+v", err, r)
	}
	if _, err := s.Post(ctx, "missing_db"); !errors.Is(err, mssql.ErrNoDatabase) {
		t.Fatalf("missing: %v", err)
	}
}

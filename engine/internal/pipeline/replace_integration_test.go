//go:build integration

package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// The restore over an existing database, end to end on SQL Server 2022 with
// the rds_* procedures mocked: the renames run rdsadmin.dbo.rds_modify_db_name
// of the mock (ALTER DATABASE ... MODIFY NAME).

// failOnce makes the first statement that contains match fail.
type failOnce struct {
	mssql.Session
	match string
	done  atomic.Bool
}

func (f *failOnce) Exec(ctx context.Context, stmt string) error {
	if strings.Contains(stmt, f.match) && f.done.CompareAndSwap(false, true) {
		return errors.New("injected failure")
	}
	return f.Session.Exec(ctx, stmt)
}

type replaceLab struct {
	in  instance
	c   *s3.Client
	env Env
}

// newReplaceLab starts the instance; free is the free storage the sessions
// report, wrap (optional) replaces the SQL of the sessions.
func newReplaceLab(t *testing.T, free int64, wrap func(mssql.Session) ops.SQL) replaceLab {
	t.Helper()
	in := startSQLServer(t)
	c := startMinIO(t)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go playRDS(ctx, t, in, c)
	target := testTarget
	target.Bucket = itBucket
	env := Env{
		Name: "it", Target: target, S3: c, TaskInterval: 300 * time.Millisecond,
		OpenOps: func(context.Context) (*ops.Session, func(), error) {
			var q ops.SQL = mssql.Session{DB: in.god}
			if wrap != nil {
				q = wrap(mssql.Session{DB: in.god})
			}
			return &ops.Session{Name: "it", Target: target, SQL: q, Bucket: awsx.Bucket{Client: c, Name: itBucket},
				FreeStorage: func(context.Context) (int64, error) { return free, nil }}, func() {}, nil
		},
	}
	return replaceLab{in: in, c: c, env: env}
}

// existing creates a database with a marker table, owned by god.
func (l replaceLab) existing(t *testing.T, db string) {
	t.Helper()
	for _, stmt := range []string{
		"CREATE DATABASE [" + db + "]",
		"ALTER AUTHORIZATION ON DATABASE::[" + db + "] TO god",
		"CREATE TABLE [" + db + "].dbo.marker(v nvarchar(10))",
		"INSERT [" + db + "].dbo.marker VALUES (N'old')",
	} {
		if _, err := l.in.sa.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// hasTable reports whether db has the table: t1 comes with the backup, marker
// with the database that was there.
func (l replaceLab) hasTable(t *testing.T, db, table string) bool {
	t.Helper()
	var n int
	if err := l.in.sa.QueryRow("SELECT COUNT(*) FROM ["+db+"].sys.tables WHERE name = @n", sql.Named("n", table)).Scan(&n); err != nil {
		t.Fatalf("%s: %v", db, err)
	}
	return n == 1
}

func (l replaceLab) names(t *testing.T, like string) []string {
	t.Helper()
	rows, err := l.in.sa.Query("SELECT name FROM sys.databases WHERE name LIKE @l ORDER BY name", sql.Named("l", like))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func (l replaceLab) plan(t *testing.T, db, mode string) (*Plan, jobs.Job) {
	t.Helper()
	file := filepath.Join(t.TempDir(), db+".bak")
	if err := os.WriteFile(file, []byte("new backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := importOf(ImportItem{Files: []string{file}, Database: db, Replace: mode})
	p, err := Build(context.Background(), l.env, req)
	if err != nil || !p.Ready {
		t.Fatalf("plan: %v %v", err, p.Failed())
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, p.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if job, err = m.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	return p, job
}

func TestIntegrationReplaceSwapKeepsTheOldDatabase(t *testing.T) {
	l := newReplaceLab(t, 1<<40, nil)
	l.existing(t, "it_swap")
	p, job := l.plan(t, "it_swap", "swap")
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s: %+v", job.State, job.Items[0])
	}
	old := "it_swap_old_" + p.Stamp
	if !l.hasTable(t, "it_swap", "t1") || l.hasTable(t, "it_swap", "marker") {
		t.Fatal("it_swap must hold the data of the backup")
	}
	if !l.hasTable(t, old, "marker") || l.hasTable(t, old, "t1") {
		t.Fatalf("%s must keep the data it had", old)
	}
	if got := l.names(t, "it_swap%"); len(got) != 2 {
		t.Fatalf("databases %v: the temporary one must be gone", got)
	}
	if !strings.Contains(job.Items[0].Result, "kept as "+old) {
		t.Fatalf("result %q", job.Items[0].Result)
	}
}

func TestIntegrationReplaceDropBacksUpToS3First(t *testing.T) {
	l := newReplaceLab(t, 1<<40, nil)
	l.existing(t, "it_drop")
	p, job := l.plan(t, "it_drop", "drop")
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s: %+v", job.State, job.Items[0])
	}
	key := "export/it_drop_before_replace_" + p.Stamp + ".bak"
	if _, err := l.c.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(itBucket), Key: aws.String(key)}); err != nil {
		t.Fatalf("the safety backup %s: %v", key, err)
	}
	if !l.hasTable(t, "it_drop", "t1") || l.hasTable(t, "it_drop", "marker") {
		t.Fatal("it_drop must be the restored database")
	}
	if got := l.names(t, "it_drop%"); len(got) != 1 || !strings.Contains(job.Items[0].Result, key) {
		t.Fatalf("databases %v, result %q", got, job.Items[0].Result)
	}
}

func TestIntegrationReplaceAutoDropsWhenSpaceIsShort(t *testing.T) {
	l := newReplaceLab(t, 15, nil) // two copies of the 10-byte backup do not fit
	l.existing(t, "it_auto")
	p, job := l.plan(t, "it_auto", "auto")
	var reason string
	for _, c := range p.Items[0].Checks {
		if c.Step == "replace" {
			reason = c.Detail
		}
	}
	if !strings.HasPrefix(reason, "drop: not enough free storage for two copies (needs 20 B, 15 B free)") {
		t.Fatalf("reason %q", reason)
	}
	if job.State != jobs.Succeeded || !l.hasTable(t, "it_auto", "t1") || len(l.names(t, "it_auto%")) != 1 {
		t.Fatalf("job %s, databases %v", job.State, l.names(t, "it_auto%"))
	}
}

func TestIntegrationReplaceSwapCompensatesTheSecondRename(t *testing.T) {
	l := newReplaceLab(t, 1<<40, func(s mssql.Session) ops.SQL {
		return &failOnce{Session: s, match: "_dialga_new_"} // first mention of the temporary name in an Exec: its rename
	})
	l.existing(t, "it_comp")
	p, job := l.plan(t, "it_comp", "swap")
	if job.State != jobs.Failed {
		t.Fatalf("job %s", job.State)
	}
	it := job.Items[0]
	if !strings.Contains(it.Error, "the previous database it_comp_old_"+p.Stamp+" is back as it_comp") {
		t.Fatalf("error %q", it.Error)
	}
	if !l.hasTable(t, "it_comp", "marker") || l.hasTable(t, "it_comp", "t1") {
		t.Fatal("it_comp must be the previous database again")
	}
	if got := l.names(t, "it_comp%"); len(got) != 2 || got[1] != "it_comp_dialga_new_"+p.Stamp {
		t.Fatalf("databases %v: the new copy must stay under its temporary name", got)
	}
	// open to every session: god can use it
	if _, err := l.in.god.Exec("SELECT 1 FROM [it_comp].dbo.marker"); err != nil {
		t.Fatalf("it_comp is not usable: %v", err)
	}
}

//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/minio"
	tcmssql "github.com/testcontainers/testcontainers-go/modules/mssql"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// The integration test runs import and export jobs end to end: the uploads
// and downloads on MinIO, the native operations on SQL Server 2022 with the
// rds_* procedures mocked (internal/testdata/mock-rds.sql). The mock only
// queues tasks: playRDS does what RDS would, reading the bucket for a
// restore and writing it for a backup, then flips the task to SUCCESS.

const (
	itBucket   = "it-backups"
	minioImage = "quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z"
)

var mssqlImage = "mcr.microsoft.com/mssql/server:2022-latest" // override with DIALGA_MSSQL_IMAGE

func throwawayPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "Dg1!" + hex.EncodeToString(b)
}

// openPlain opens a test-only connection without TLS (self-signed
// certificate, no RDS name); production code uses mssql.Open.
func openPlain(ctx context.Context, hostPort, user, password string) (*sql.DB, error) {
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(user, password), Host: hostPort}
	u.RawQuery = url.Values{"database": {"master"}, "encrypt": {"disable"}}.Encode()
	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
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

// runScript runs a script one batch per GO on one connection.
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
		_, err := conn.ExecContext(ctx, text)
		return err
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

// instance is SQL Server with the mock: sa plays RDS, god is the master user.
type instance struct {
	sa, god *sql.DB
}

func startSQLServer(t *testing.T) instance {
	t.Helper()
	ctx := context.Background()
	if img := os.Getenv("DIALGA_MSSQL_IMAGE"); img != "" {
		mssqlImage = img
	}
	saPassword := throwawayPassword()
	ctr, err := tcmssql.Run(ctx, mssqlImage, tcmssql.WithAcceptEULA(), tcmssql.WithPassword(saPassword))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	if code, out, err := ctr.Exec(ctx, []string{"sh", "-c", "mkdir -p /bak && chown mssql /bak"}, tcexec.WithUser("root")); err != nil || code != 0 {
		msg, _ := io.ReadAll(out)
		t.Fatalf("mkdir /bak: exit %d, %v: %s", code, err, msg)
	}
	hostPort, err := ctr.PortEndpoint(ctx, "1433/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	sa, err := openPlain(ctx, hostPort, "sa", saPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sa.Close() })
	mock, err := os.ReadFile(filepath.Join("..", "testdata", "mock-rds.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runScript(ctx, sa, string(mock)); err != nil {
		t.Fatal(err)
	}
	godPassword := throwawayPassword()
	if _, err := sa.ExecContext(ctx, `DECLARE @s nvarchar(400) =
		N'ALTER LOGIN god WITH PASSWORD = ' + QUOTENAME(@pw, '''') + N';'; EXEC (@s);`, sql.Named("pw", godPassword)); err != nil {
		t.Fatal(err)
	}
	god, err := openPlain(ctx, hostPort, "god", godPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = god.Close() })
	return instance{sa: sa, god: god}
}

func startMinIO(t *testing.T) *s3.Client {
	t.Helper()
	ctx := context.Background()
	ctr, err := minio.Run(ctx, minioImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("http://" + endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(ctr.Username, ctr.Password, ""),
	})
	if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(itBucket)}); err != nil {
		t.Fatal(err)
	}
	return c
}

var itARN = regexp.MustCompile(`^arn:aws:s3:::` + itBucket + `/(.+)$`)

// playRDS runs the queued native tasks of the mock until ctx ends.
func playRDS(ctx context.Context, t *testing.T, in instance, c *s3.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		rows, err := in.sa.QueryContext(ctx, `SELECT task_id, task_type, database_name, S3_object_arn
			FROM msdb.dbo.mock_tasks WHERE lifecycle = 'CREATED'`)
		if err != nil {
			continue
		}
		type task struct {
			id           int
			typ, db, arn string
		}
		var todo []task
		for rows.Next() {
			var tk task
			if rows.Scan(&tk.id, &tk.typ, &tk.db, &tk.arn) == nil {
				todo = append(todo, tk)
			}
		}
		_ = rows.Close()
		for _, tk := range todo {
			lifecycle, info := "SUCCESS", "[mock] done"
			if err := playTask(ctx, in, c, tk.typ, tk.db, tk.arn); err != nil {
				lifecycle, info = "ERROR", "[mock] "+err.Error()
			}
			if _, err := in.sa.ExecContext(ctx, `UPDATE msdb.dbo.mock_tasks SET lifecycle = @l, pct = 100,
				task_info = @i, last_updated = GETDATE() WHERE task_id = @id`,
				sql.Named("l", lifecycle), sql.Named("i", info), sql.Named("id", tk.id)); err != nil && ctx.Err() == nil {
				t.Errorf("task %d: %v", tk.id, err)
			}
		}
	}
}

func playTask(ctx context.Context, in instance, c *s3.Client, typ, db, arn string) error {
	m := itARN.FindStringSubmatch(arn)
	if m == nil {
		return fmt.Errorf("not in the bucket of the target: %s", arn)
	}
	key := m[1]
	switch typ {
	case "RESTORE_DB":
		// RDS reads the backup from the bucket: it must be uploaded first.
		if _, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(itBucket), Key: aws.String(key)}); err != nil {
			return fmt.Errorf("backup not in S3: %w", err)
		}
		_, err := in.sa.ExecContext(ctx, fmt.Sprintf(`RESTORE DATABASE [%s] FROM DISK = N'/bak/app_db_2022.bak' WITH
			MOVE N'app_db' TO N'/var/opt/mssql/data/%s.mdf', MOVE N'app_db_log' TO N'/var/opt/mssql/data/%s_log.ldf'`, db, db, db))
		return err
	case "BACKUP_DB":
		_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(itBucket), Key: aws.String(key),
			Body: bytes.NewReader([]byte("backup of " + db)), ChecksumAlgorithm: "CRC32C"})
		return err
	}
	return fmt.Errorf("unknown task type %s", typ)
}

func TestIntegrationImportThenExportJobs(t *testing.T) {
	in := startSQLServer(t)
	c := startMinIO(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go playRDS(ctx, t, in, c)

	target := testTarget
	target.Bucket = itBucket
	env := Env{
		Name: "it", Target: target, S3: c, TaskInterval: 300 * time.Millisecond,
		OpenOps: func(context.Context) (*ops.Session, func(), error) {
			return &ops.Session{Name: "it", Target: target, SQL: mssql.Session{DB: in.god},
				Bucket: awsx.Bucket{Client: c, Name: itBucket}}, func() {}, nil
		},
	}
	dir := t.TempDir()
	big := make([]byte, 12<<20+5)
	_, _ = rand.Read(big)
	sales := filepath.Join(dir, "sales.bak")
	hr := filepath.Join(dir, "hr.bak")
	if err := os.WriteFile(sales, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hr, []byte("small backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	importReq := Request{Kind: jobs.Import, Import: []ImportItem{
		{Files: []string{sales}, Database: "it_sales"},
		{Files: []string{hr}, Database: "it_hr"},
	}}
	job := runJobFor(t, env, importReq, 3*time.Minute)
	for _, it := range job.Items {
		if it.State != jobs.Succeeded || !strings.HasPrefix(it.Result, it.Name+" restored: ONLINE") {
			t.Fatalf("%s: %s, %q %q", it.Name, it.State, it.Result, it.Error)
		}
	}
	// The post-restore warning (the orphaned user of the fixture) is logged.
	if !logged(job, "it_sales", "Orphaned users") {
		t.Fatal("no post-restore warning logged")
	}
	for _, db := range []string{"it_sales", "it_hr"} {
		state, err := mssql.Session{DB: in.sa}.DatabaseState(context.Background(), db)
		if err != nil || state != "ONLINE" {
			t.Fatalf("%s is %q, %v", db, state, err)
		}
	}
	head, err := c.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(itBucket), Key: aws.String("import/sales.bak")})
	if err != nil || aws.ToInt64(head.ContentLength) != int64(len(big)) {
		t.Fatalf("uploaded backup: %v", err)
	}

	// The same plan again: the objects and the databases exist now.
	if p, err := Build(context.Background(), env, importReq); err != nil || p.Ready || len(p.Failed()) != 4 {
		t.Fatalf("second import: ready %v, %v, failed %v", p.Ready, err, p.Failed())
	}

	out := t.TempDir()
	exportReq := Request{Kind: jobs.Export, Export: []ExportItem{
		{Database: "it_sales", Key: "export/it_sales.bak", Destination: out},
		{Database: "it_hr", Key: "export/it_hr.bak"},
	}}
	job = runJobFor(t, env, exportReq, 3*time.Minute)
	if job.State != jobs.Succeeded {
		t.Fatalf("export job %s: %+v", job.State, job.Items)
	}
	got, err := os.ReadFile(filepath.Join(out, "it_sales.bak"))
	if err != nil || string(got) != "backup of it_sales" || !strings.Contains(job.Items[0].Steps[2].Detail, "CRC32C verified") {
		t.Fatalf("download %q, %v, %q", got, err, job.Items[0].Steps[2].Detail)
	}
	if job.Items[1].Result != "it_hr exported to s3://"+itBucket+"/export/it_hr.bak" {
		t.Fatalf("export without download: %q", job.Items[1].Result)
	}
}

func runJobFor(t *testing.T, env Env, req Request, limit time.Duration) jobs.Job {
	t.Helper()
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready {
		t.Fatalf("pre-flight: %v", p.Failed())
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, p.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	job, err = m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatalf("job %s: %v", job.ID, err)
	}
	return job
}

func logged(job jobs.Job, item, text string) bool {
	for _, ev := range job.Events {
		if ev.Type == jobs.EventLog && ev.Item == item && strings.Contains(ev.Message, text) {
			return true
		}
	}
	return false
}

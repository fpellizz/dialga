//go:build integration

package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// partStall is the S3 client of the target, except that while stall is set
// the upload of part 2 waits for the context to end (the engine stops there).
type partStall struct {
	transfer.Client
	mu      sync.Mutex
	stall   bool
	stalled chan struct{}
	parts   map[int32]int // UploadPart calls by part number
	lists   int
}

func (p *partStall) UploadPart(ctx context.Context, in *s3.UploadPartInput, o ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	n := aws.ToInt32(in.PartNumber)
	p.mu.Lock()
	p.parts[n]++
	stall := p.stall && n == 2
	if stall && p.stalled != nil {
		close(p.stalled)
		p.stalled = nil
	}
	p.mu.Unlock()
	if stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.Client.UploadPart(ctx, in, o...)
}

func (p *partStall) ListParts(ctx context.Context, in *s3.ListPartsInput, o ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	p.mu.Lock()
	p.lists++
	p.mu.Unlock()
	return p.Client.ListParts(ctx, in, o...)
}

func TestIntegrationResumeInterruptedJobs(t *testing.T) {
	in := startSQLServer(t)
	c := startMinIO(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go playRDS(ctx, t, in, c)

	target := testTarget
	target.Bucket = itBucket
	s3c := &partStall{Client: c, parts: map[int32]int{}}
	env := Env{
		Name: "it", Target: target, S3: s3c, TaskInterval: 300 * time.Millisecond,
		OpenOps: func(context.Context) (*ops.Session, func(), error) {
			return &ops.Session{Name: "it", Target: target, SQL: mssql.Session{DB: in.admin},
				Bucket: awsx.Bucket{Client: c, Name: itBucket}}, func() {}, nil
		},
	}
	file := func(name string, size int) string {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// start plans and starts an import of db, waits for cond, then stops
	// the engine and opens its journal again, as the next run would.
	start := func(t *testing.T, e Env, path, db string, cond func(jobs.Job) bool) (*jobs.Manager, jobs.Job) {
		t.Helper()
		p, err := Build(context.Background(), e, importOf(ImportItem{Files: []string{path}, Database: db}))
		if err != nil || !p.Ready {
			t.Fatalf("plan: %v, %v", err, p.Failed())
		}
		dir := t.TempDir()
		m, err := jobs.Open(dir, jobs.Options{})
		if err != nil {
			t.Fatal(err)
		}
		job, err := p.Start(m, p.Confirmation)
		if err != nil {
			t.Fatal(err)
		}
		waitJob(t, "the interrupt point", m, job.ID, cond)
		return restartOn(t, dir, m), job
	}
	end := func(t *testing.T, m *jobs.Manager, id string, e Env) jobs.Job {
		t.Helper()
		if _, err := m.Resume(id, jobs.ResumeOptions{Env: e, InstanceKey: instanceKeyOf(e)}); err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		job, err := m.Wait(wctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != jobs.Succeeded || !strings.HasPrefix(job.Items[0].Result, job.Items[0].Name+" restored: ONLINE") {
			t.Fatalf("resumed job %s: %+v", job.State, job.Items)
		}
		return job
	}
	tasksOf := func(t *testing.T, db string) int {
		t.Helper()
		var n int
		if err := in.sa.QueryRow(`SELECT COUNT(*) FROM msdb.dbo.mock_tasks WHERE database_name = @db`, sql.Named("db", db)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("mid-upload", func(t *testing.T) {
		s3c.mu.Lock()
		s3c.stall, s3c.stalled = true, make(chan struct{})
		stalled := s3c.stalled
		s3c.mu.Unlock()
		path := file("up.bak", 20<<20) // two parts of 16 MiB
		p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{path}, Database: "it_up"}))
		if err != nil || !p.Ready {
			t.Fatalf("plan: %v, %v", err, p.Failed())
		}
		dir := t.TempDir()
		m, err := jobs.Open(dir, jobs.Options{})
		if err != nil {
			t.Fatal(err)
		}
		job, err := p.Start(m, p.Confirmation)
		if err != nil {
			t.Fatal(err)
		}
		<-stalled
		// Part 1 is on the journal before the engine stops.
		waitJob(t, "part 1", m, job.ID, func(j jobs.Job) bool {
			return j.Items[0].Upload != nil && slices.Equal(j.Items[0].Upload.PartsDone, []int{1})
		})
		m = restartOn(t, dir, m)
		before, _ := m.Get(job.ID)
		s3c.mu.Lock()
		s3c.stall = false
		s3c.mu.Unlock()
		done := end(t, m, job.ID, env)
		head, err := c.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(itBucket), Key: aws.String("import/up.bak")})
		s3c.mu.Lock()
		defer s3c.mu.Unlock()
		// Part 1 was not sent again; part 2 was, after ListParts.
		if err != nil || aws.ToInt64(head.ContentLength) != 20<<20 || s3c.parts[1] != 1 || s3c.parts[2] != 2 || s3c.lists != 1 ||
			done.Items[0].Upload == nil || done.Items[0].Upload.UploadID != before.Items[0].Upload.UploadID {
			t.Fatalf("head %v, %v, parts %v, ListParts %d, upload %+v", head, err, s3c.parts, s3c.lists, done.Items[0].Upload)
		}
		if n := tasksOf(t, "it_up"); n != 1 {
			t.Fatalf("%d restore task(s)", n)
		}
	})

	t.Run("mid-wait", func(t *testing.T) {
		rdsHold.Store("it_wait", true)
		m, job := start(t, env, file("wait.bak", 1<<20), "it_wait", func(j jobs.Job) bool {
			return len(j.Items[0].TaskIDs) == 1 && j.Items[0].Steps[2].Status == jobs.StepRunning
		})
		before, _ := m.Get(job.ID)
		rdsHold.Delete("it_wait")
		done := end(t, m, job.ID, env)
		// The same task, followed to its end.
		if !slices.Equal(done.Items[0].TaskIDs, before.Items[0].TaskIDs) || done.Items[0].Task.Lifecycle != mssql.Success || tasksOf(t, "it_wait") != 1 {
			t.Fatalf("tasks %v then %v, %d in the instance", before.Items[0].TaskIDs, done.Items[0].TaskIDs, tasksOf(t, "it_wait"))
		}
	})

	t.Run("after-restore", func(t *testing.T) {
		// The session of the wait never opens: the engine stops with the
		// restore queued and nothing read yet.
		stopped := env
		stopped.OpenOps = func(ctx context.Context) (*ops.Session, func(), error) {
			var n int
			if err := in.sa.QueryRow(`SELECT COUNT(*) FROM msdb.dbo.mock_tasks WHERE database_name = 'it_queued'`).Scan(&n); err == nil && n > 0 {
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			return env.OpenOps(ctx)
		}
		m, job := start(t, stopped, file("queued.bak", 1<<20), "it_queued", func(j jobs.Job) bool {
			return len(j.Items[0].TaskIDs) == 1 && j.Items[0].Steps[2].Status == jobs.StepRunning
		})
		before, _ := m.Get(job.ID)
		if st := before.Items[0].Steps[1]; st.Status != jobs.StepSucceeded || st.TaskID == 0 {
			t.Fatalf("restore step %+v", st)
		}
		done := end(t, m, job.ID, env)
		if n := tasksOf(t, "it_queued"); n != 1 || !slices.Equal(done.Items[0].TaskIDs, before.Items[0].TaskIDs) {
			t.Fatalf("%d restore task(s), tasks %v then %v", n, before.Items[0].TaskIDs, done.Items[0].TaskIDs)
		}
	})
}

//go:build integration

package pipeline

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// A single restore and a single drop run as jobs of kind operation against
// SQL Server (the rds_* procedures mocked) and MinIO.
func TestIntegrationOperationsRunAsJobs(t *testing.T) {
	in := startSQLServer(t)
	c := startMinIO(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go playRDS(ctx, t, in, c)

	target := testTarget
	target.Bucket = itBucket
	open := func(context.Context) (*ops.Session, func(), error) {
		return &ops.Session{Name: "it", Target: target, SQL: mssql.Session{DB: in.admin},
			Bucket: awsx.Bucket{Client: c, Name: itBucket}}, func() {}, nil
	}
	env := Env{Name: "it", Target: target, S3: c, TaskInterval: 300 * time.Millisecond, OpenOps: open}
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(itBucket), Key: aws.String("import/op_sales.bak"),
		Body: bytes.NewReader([]byte("a backup"))}); err != nil {
		t.Fatal(err)
	}
	run := func(op string, p ops.Params, confirm string) jobs.Job {
		t.Helper()
		sess, closeOps, _ := open(ctx)
		defer closeOps()
		prep, err := sess.Build(ctx, op, p)
		if err != nil || !prep.Ready() {
			t.Fatalf("%s pre-flight: %v %+v", op, err, prep.Checks)
		}
		if want := prep.Plan.Confirmation(); want != confirm {
			t.Fatalf("%s confirmation %q, want %q", op, want, confirm)
		}
		spec, err := OperationSpec(env, prep, false)
		if err != nil {
			t.Fatal(err)
		}
		m := jobs.New(jobs.Options{})
		job, err := m.Start(spec)
		if err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		end, err := m.Wait(wctx, job.ID)
		if err != nil {
			t.Fatalf("%s job: %v", op, err)
		}
		return end
	}

	restore := run(ops.OpRestore, ops.Params{Database: "op_sales", Key: "op_sales.bak"}, "")
	if restore.State != jobs.Succeeded || restore.Kind != jobs.Operation || restore.Operation != "restore" ||
		restore.Items[0].Task == nil || restore.Items[0].Task.Lifecycle != mssql.Success {
		t.Fatalf("restore job %s: %+v", restore.State, restore.Items)
	}
	if state, err := (mssql.Session{DB: in.sa}).DatabaseState(ctx, "op_sales"); err != nil || state != "ONLINE" {
		t.Fatalf("op_sales is %q, %v", state, err)
	}

	drop := run(ops.OpDrop, ops.Params{Database: "op_sales"}, "op_sales")
	if drop.State != jobs.Succeeded || drop.Operation != "drop" || drop.Items[0].Result != "op_sales dropped" {
		t.Fatalf("drop job %s: %+v", drop.State, drop.Items)
	}
	if state, err := (mssql.Session{DB: in.sa}).DatabaseState(ctx, "op_sales"); err != nil || state != "" {
		t.Fatalf("op_sales after the drop is %q, %v", state, err)
	}
}

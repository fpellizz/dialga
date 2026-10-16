//go:build integration

package inspect

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
)

// update rewrites testdata/*.json from the real backups:
//
//	go test -tags integration -run TestInspectRealBackups ./internal/inspect -update
var update = flag.Bool("update", false, "rewrite the testdata fixtures from the real backups")

const (
	image2017 = "mcr.microsoft.com/mssql/server:2017-latest"
	image2022 = "mcr.microsoft.com/mssql/server:2022-latest"
)

// backupDir is where the test backups go: DIALGA_TEST_BACKUP_DIR keeps them;
// in Bitbucket the docker service mounts only paths under the clone.
func backupDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("DIALGA_TEST_BACKUP_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	parent := os.Getenv("BITBUCKET_CLONE_DIR")
	dir, err := os.MkdirTemp(parent, "dialga-inspect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// The SQL Server of the container may not be the owner of the directory.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ensureImage pulls the image when it is not local (inspect itself never does).
func ensureImage(ctx context.Context, t *testing.T, in *inspector, rt Runtime, image string) {
	t.Helper()
	if in.imageExists(ctx, rt, image) == nil {
		return
	}
	t.Logf("pulling %s", image)
	if _, err := in.run(ctx, nil, rt.Path, "pull", image); err != nil {
		t.Fatalf("pull %s: %v", image, err)
	}
}

// withServer runs fn on a SQL Server of image with dir mounted read-write
// on /backup, then removes the container.
func withServer(ctx context.Context, t *testing.T, in *inspector, rt Runtime, image, dir string, fn func(querier)) {
	t.Helper()
	ensureImage(ctx, t, in, rt, image)
	c := &container{in: in, rt: rt, name: "dialga-inspect-test-" + strings.ToLower(in.password()[2:10])}
	defer func() {
		if err := c.remove(ctx); err != nil {
			t.Errorf("remove %s: %v", c.name, err)
		}
	}()
	password := in.password()
	addr, err := c.start(ctx, image, []mount{{host: dir, container: "/backup"}}, password)
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}
	q, version, err := c.waitReady(ctx, addr, password)
	if err != nil {
		t.Fatalf("%s not ready: %v", image, err)
	}
	defer func() { _ = q.Close() }()
	t.Logf("%s: SQL Server %s", image, version)
	fn(q)
}

func mustExec(ctx context.Context, t *testing.T, q querier, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := q.query(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// makeDB creates a small database with some rows.
func makeDB(ctx context.Context, t *testing.T, q querier, db string) {
	t.Helper()
	mustExec(ctx, t, q,
		"CREATE DATABASE ["+db+"]",
		"CREATE TABLE ["+db+"].dbo.items (id int PRIMARY KEY, name nvarchar(50) NOT NULL)",
		"INSERT INTO ["+db+"].dbo.items (id, name) SELECT TOP (500) ROW_NUMBER() OVER (ORDER BY a.object_id), N'item' "+
			"FROM sys.all_objects a CROSS JOIN sys.all_objects b",
	)
}

func writeFixture(t *testing.T, name string, rows []row) {
	t.Helper()
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", name), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectRealBackups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	in := newInspector()
	rt, err := in.detectRuntime(ctx)
	if err != nil {
		t.Skipf("no container runtime: %v", err)
	}
	t.Logf("runtime %s", rt)
	dir := backupDir(t)
	bak := func(name string) string { return filepath.Join(dir, name) }
	for _, f := range []string{"t2017.bak", "t2022.bak", "s1.bak", "s2.bak"} {
		_ = os.Remove(bak(f)) // BACKUP … WITH INIT keeps the media set of an old file
	}

	// One SQL Server at a time: the CI docker service has 4 GB.
	withServer(ctx, t, in, rt, image2017, dir, func(q querier) {
		makeDB(ctx, t, q, "t2017")
		mustExec(ctx, t, q, "BACKUP DATABASE [t2017] TO DISK = N'/backup/t2017.bak' WITH INIT, FORMAT")
	})
	withServer(ctx, t, in, rt, image2022, dir, func(q querier) {
		makeDB(ctx, t, q, "t2022")
		mustExec(ctx, t, q,
			"BACKUP DATABASE [t2022] TO DISK = N'/backup/t2022.bak' WITH INIT, FORMAT, COMPRESSION, CHECKSUM",
			"BACKUP DATABASE [t2022] TO DISK = N'/backup/s1.bak', DISK = N'/backup/s2.bak' WITH INIT, FORMAT, COPY_ONLY",
		)
		if !*update {
			return
		}
		for _, f := range []struct{ name, stmt string }{
			{"labelonly-2017.json", "RESTORE LABELONLY FROM DISK = N'/backup/t2017.bak'"},
			{"headeronly-2017.json", "RESTORE HEADERONLY FROM DISK = N'/backup/t2017.bak'"},
			{"filelistonly-2017.json", "RESTORE FILELISTONLY FROM DISK = N'/backup/t2017.bak' WITH FILE = 1"},
			{"headeronly-2022.json", "RESTORE HEADERONLY FROM DISK = N'/backup/t2022.bak'"},
			{"filelistonly-2022.json", "RESTORE FILELISTONLY FROM DISK = N'/backup/t2022.bak' WITH FILE = 1"},
			{"labelonly-striped.json", "RESTORE LABELONLY FROM DISK = N'/backup/s2.bak'"},
		} {
			rows, err := q.query(ctx, f.stmt)
			if err != nil {
				t.Fatalf("%s: %v", f.stmt, err)
			}
			writeFixture(t, f.name, rows)
		}
	})
	junk := bak("junk.bak")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a backup\n", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}

	free := int64(100 << 30)
	target := func(context.Context) (Target, error) {
		return Target{Name: "test", Instance: "test-sqlserver", Engine: "sqlserver-se",
			EngineVersion: "14.00.3540.1.v1", Major: 14, FreeBytes: &free}, nil
	}
	var events []check.Event
	emit := func(e check.Event) { events = append(events, e) }

	// All the files at once: two backups, one striped over two files given
	// out of order, one file that is not a backup.
	res, err := in.inspect(ctx, Request{Paths: []string{bak("t2017.bak"), bak("t2022.bak"), bak("s2.bak"), bak("s1.bak"), junk},
		Target: target}, emit)
	if err != nil {
		t.Fatalf("inspect: %v\nevents: %+v", err, events)
	}
	if res.Outcome != Unreadable || len(res.Backups) != 4 {
		t.Fatalf("outcome %s, %d backups: %+v", res.Outcome, len(res.Backups), res.Backups)
	}
	b17, b22, striped, bad := res.Backups[0], res.Backups[1], res.Backups[2], res.Backups[3]
	if b17.Outcome != Restorable || b17.Header.Major != 14 || b17.Header.DatabaseName != "t2017" ||
		b17.Header.Product != "SQL Server 2017" || b17.Header.Compressed || b17.RequiredBytes == 0 {
		t.Errorf("2017 backup: %+v %+v", b17, b17.Header)
	}
	if b22.Outcome != NotRestorable || b22.Header.Major != 16 || !b22.Header.Compressed || !b22.Header.Checksums ||
		!strings.Contains(b22.Remedy, ".bacpac") {
		t.Errorf("2022 backup: %+v %+v", b22, b22.Header)
	}
	if len(striped.Files) != 2 || striped.Files[0] != bak("s1.bak") || striped.Outcome != NotRestorable ||
		!striped.Header.CopyOnly {
		t.Errorf("striped backup: %+v", striped)
	}
	if bad.Outcome != Unreadable || bad.Files[0] != junk || !strings.Contains(bad.Error, "junk.bak") || bad.Remedy == "" {
		t.Errorf("junk file: %+v", bad)
	}
	if res.Space == nil || res.Space.Status != check.OK || res.RequiredBytes != b17.RequiredBytes+b22.RequiredBytes+striped.RequiredBytes {
		t.Errorf("space %+v, required %d", res.Space, res.RequiredBytes)
	}
	if last := events[len(events)-1]; last.Step != "cleanup" || last.Status != check.OK {
		t.Errorf("last event %+v", last)
	}

	// The 2017 backup alone, without a target: headers only.
	events = nil
	res, err = in.inspect(ctx, Request{Paths: []string{bak("t2017.bak")}}, emit)
	if err != nil || res.Outcome != Read || res.Backups[0].Findings != nil {
		t.Fatalf("headers only: %v %+v", err, res)
	}

	if out, err := in.run(ctx, nil, rt.Path, "ps", "-a", "-q", "--filter", "label=io.dialga.inspect=true"); err != nil || out != "" {
		t.Errorf("containers left: %q %v", out, err)
	}
}

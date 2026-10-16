package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTarget() Target {
	return Target{
		AccountID: "123456789012", Region: "us-east-1", AWSProfile: "sales", Instance: "prod-sqlserver",
		Bucket: "prod-sqlserver-backups", SQL: SQL{User: "admin", Password: "env:PROD_SQL_PASSWORD"},
		Connect: Connect{Mode: ModeDirect}, Protected: true,
	}
}

func writeDoc(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestUpdateAddReplaceRemoveRoundTrip(t *testing.T) {
	path := writeDoc(t, validDoc)
	if err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Names(), ","); got != "dev-sql,prod-sql" {
		t.Fatalf("Names() = %s", got)
	}
	want, _ := Check("prod-sql", newTarget())
	if cfg.Targets["prod-sql"] != want || cfg.Targets["dev-sql"].Instance != "dev-sqlserver" {
		t.Fatalf("targets after add: %+v", cfg.Targets)
	}
	if m := mode(t, path); m != 0o600 {
		t.Fatalf("mode %v, want 0600", m)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), fileHeader+"apiVersion: v1\n") || strings.Contains(string(data), "bridge_tag") {
		t.Fatalf("written file:\n%s", data)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != validDoc {
		t.Fatalf("backup:\n%s", bak)
	}

	// Without replace the same name is refused; with replace it changes.
	err = Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) })
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("add twice: %v", err)
	}
	changed := newTarget()
	changed.Bucket = "other-bucket"
	if err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", changed, true) }); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = Load(path); cfg.Targets["prod-sql"].Bucket != "other-bucket" {
		t.Fatalf("replace: %+v", cfg.Targets["prod-sql"])
	}

	if err := Update(path, func(c *Config) error { return c.RemoveTarget("nope") }); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("remove unknown: %v", err)
	}
	if err := Update(path, func(c *Config) error { return c.RemoveTarget("dev-sql") }); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = Load(path); strings.Join(cfg.Names(), ",") != "prod-sql" {
		t.Fatalf("after remove: %v", cfg.Names())
	}
}

func TestRemovingTheLastTargetRemovesTheFile(t *testing.T) {
	path := writeDoc(t, validDoc)
	if err := Update(path, func(c *Config) error { return c.RemoveTarget("dev-sql") }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the file is still there: %v", err)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != validDoc {
		t.Fatalf("backup:\n%s", bak)
	}
}

func TestUpdateCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "dialga", "config.yaml")
	if err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) }); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Dir(path)); m != 0o700 {
		t.Fatalf("directory mode %v, want 0700", m)
	}
	if m := mode(t, path); m != 0o600 {
		t.Fatalf("file mode %v, want 0600", m)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a first write needs no backup: %v", err)
	}
}

func TestUpdateRefusesInvalidInput(t *testing.T) {
	path := writeDoc(t, validDoc)
	bad := newTarget()
	bad.SQL.Password = "hunter2"
	bad.Connect = Connect{Mode: ModeExternal}
	err := Update(path, func(c *Config) error { return c.SetTarget("Prod", bad, false) })
	for _, want := range []string{"name must be", "sql.password", "local_port"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v, want it to mention %q", err, want)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != validDoc {
		t.Fatalf("the file changed:\n%s", data)
	}
}

func TestUpdateNeverOverwritesAnInvalidFile(t *testing.T) {
	broken := strings.Replace(validDoc, "region:", "regoin:", 1)
	path := writeDoc(t, broken)
	err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) })
	var invalid *InvalidFileError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "fix it first") {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != broken {
		t.Fatalf("the file changed:\n%s", data)
	}
}

func TestUpdateBuildsOnAnEmptyDocument(t *testing.T) {
	path := writeDoc(t, "apiVersion: v1\ntargets: {}\n")
	if err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) }); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(path); err != nil || len(cfg.Targets) != 1 {
		t.Fatalf("Load: %v", err)
	}
}

func TestFailedWriteLeavesTheOldFile(t *testing.T) {
	path := writeDoc(t, validDoc)
	renameFile = func(oldpath, newpath string) error {
		if newpath == path {
			return errors.New("disk full")
		}
		return os.Rename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = os.Rename })
	err := Update(path, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) })
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != validDoc {
		t.Fatalf("the file changed:\n%s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestSaveFollowsASymlink(t *testing.T) {
	target := writeDoc(t, validDoc)
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := Update(link, func(c *Config) error { return c.SetTarget("prod-sql", newTarget(), false) }); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if cfg, err := Load(target); err != nil || len(cfg.Targets) != 2 {
		t.Fatalf("Load(target): %v", err)
	}
}

func TestValidators(t *testing.T) {
	if !ValidTargetName("mssql-dev") || ValidTargetName("Mssql") || ValidTargetName("") {
		t.Error("ValidTargetName")
	}
	if !ValidProfile("sales") || ValidProfile("a b") {
		t.Error("ValidProfile")
	}
	if !ValidRegion("us-east-1") || ValidRegion("us-east") {
		t.Error("ValidRegion")
	}
}

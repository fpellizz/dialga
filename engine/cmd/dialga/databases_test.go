package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// fakeCatalog serves a fixed list of databases.
type fakeCatalog struct {
	list []mssql.Database
	err  error
}

func (f fakeCatalog) Databases(context.Context) ([]mssql.Database, error) { return f.list, f.err }

func (f fakeCatalog) Usage(context.Context) (mssql.Usage, error) { return mssql.Usage{}, f.err }

// withCatalog makes a read the databases from f; opened reports whether a
// session was opened and with which password.
func withCatalog(a *app, f fakeCatalog) (opened *bool, password *string) {
	opened, password = new(bool), new(string)
	a.openDatabases = func(_ context.Context, _ config.Target, pw string) (mssql.DatabaseReader, func(), error) {
		*opened, *password = true, pw
		return f, func() {}, nil
	}
	return opened, password
}

var sampleDatabases = []mssql.Database{
	{Name: "app_db", State: "ONLINE", Recovery: "FULL", Compatibility: 140, Owner: "rdsa",
		UserAccess: "MULTI_USER", CreatedAt: "2026-10-15T13:05:40", DataMB: 16, LogMB: 8},
	{Name: "archive", State: "ONLINE", Recovery: "SIMPLE", Compatibility: 130, UserAccess: "SINGLE_USER",
		ReadOnly: true, CreatedAt: "2026-10-01T09:00:00", DataMB: 2048, LogMB: 512},
	{Name: "sales_copy", State: "RESTORING", Recovery: "FULL", Compatibility: 140, Owner: "rdsa",
		UserAccess: "MULTI_USER", CreatedAt: "2026-10-16T08:00:00", DataMB: 8, LogMB: 8},
}

func TestDatabasesText(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	_, password := withCatalog(a, fakeCatalog{list: sampleDatabases})
	code, out, _ := run(a, "databases")
	if code != 0 || *password != "s3cret" {
		t.Fatalf("code %d, password %q:\n%s", code, *password, out)
	}
	for _, want := range []string{"User databases on dev-sql (instance dev-sqlserver):", "NAME", "OWNER",
		"app_db", "16.0 MiB", "ONLINE (READ_ONLY, SINGLE_USER)", "2.0 GiB", "RESTORING",
		"3 databases, 2.5 GiB in total."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	// No owner login: a dash, not an empty column.
	if line := lineWith(out, "archive"); !strings.Contains(line, " -") {
		t.Fatalf("archive line %q", line)
	}
}

func lineWith(out, s string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, s) {
			return line
		}
	}
	return ""
}

func TestDatabasesJSONAndAlias(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCatalog(a, fakeCatalog{list: sampleDatabases[:1]})
	code, out, _ := run(a, "dbs", "dev-sql", "-o", "json")
	var doc struct {
		Target    string           `json:"target"`
		Instance  string           `json:"instance"`
		Databases []mssql.Database `json:"databases"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 {
		t.Fatalf("code %d, json %v:\n%s", code, err, out)
	}
	if doc.Target != "dev-sql" || doc.Instance != "dev-sqlserver" || len(doc.Databases) != 1 ||
		doc.Databases[0] != sampleDatabases[0] {
		t.Fatalf("doc %+v", doc)
	}
	if code, out, _ := run(a, "dbs"); code != 0 || !strings.Contains(out, "1 database, 24.0 MiB in total.") {
		t.Fatalf("one database: code %d:\n%s", code, out)
	}
}

func TestDatabasesEmptyAndErrors(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCatalog(a, fakeCatalog{list: []mssql.Database{}})
	if code, out, _ := run(a, "databases"); code != 0 || !strings.Contains(out, "No user databases on dev-sql") {
		t.Fatalf("empty: code %d, out %q", code, out)
	}
	failing, _, _ := testApp(t, testConfig)
	withCatalog(failing, fakeCatalog{err: errors.New("permission denied")})
	if code, _, errOut := run(failing, "databases"); code != exitFailure || !strings.Contains(errOut, "permission denied") {
		t.Fatalf("query fails: code %d, stderr %q", code, errOut)
	}
	unknown, _, _ := testApp(t, testConfig)
	opened, _ := withCatalog(unknown, fakeCatalog{})
	if code, _, _ := run(unknown, "databases", "nope"); code != exitFailure || *opened {
		t.Fatalf("unknown target: code %d, opened %v", code, *opened)
	}
	// connect.mode tunnel opens the integrated tunnel through the session.
	tunnel, _, _ := testApp(t, strings.Replace(testConfig, "mode: external", "mode: tunnel", 1))
	opened, _ = withCatalog(tunnel, fakeCatalog{})
	if code, _, errOut := run(tunnel, "databases"); code != 0 || !*opened {
		t.Fatalf("tunnel: code %d, opened %v, stderr %q", code, *opened, errOut)
	}
}

func TestSizeMiB(t *testing.T) {
	for mb, want := range map[float64]string{
		8: "8.0 MiB", 1023.9: "1023.9 MiB", 1024: "1.0 GiB", 1536: "1.5 GiB", 3 << 20: "3.0 TiB",
	} {
		if got := sizeMiB(mb); got != want {
			t.Errorf("sizeMiB(%v) = %q, want %q", mb, got, want)
		}
	}
}

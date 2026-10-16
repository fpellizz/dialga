//go:build integration

package mssql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// catalogServer starts SQL Server 2022 with a throwaway sa password and
// returns a plain session to it (the container has no RDS certificate).
func catalogServer(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	password := "Dg1!" + rand.Text() // upper, lower, digit and symbol: the sa policy
	c, err := testcontainers.Run(ctx, "mcr.microsoft.com/mssql/server:2022-latest",
		testcontainers.WithExposedPorts("1433/tcp"),
		testcontainers.WithEnv(map[string]string{"ACCEPT_EULA": "Y", "MSSQL_SA_PASSWORD": password}),
		testcontainers.WithWaitStrategy(wait.ForLog("Recovery is complete.").WithStartupTimeout(3*time.Minute)),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := c.PortEndpoint(ctx, "1433/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword("sa", password), Host: addr,
		RawQuery: url.Values{"database": {"master"}, "encrypt": {"disable"}}.Encode()}
	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	// The log line can come a moment before logins are accepted.
	deadline := time.Now().Add(time.Minute)
	for err = db.PingContext(ctx); err != nil; err = db.PingContext(ctx) {
		if time.Now().After(deadline) {
			t.Fatalf("SQL Server login: %v", err)
		}
		time.Sleep(time.Second)
	}
	return db
}

func execAll(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestCatalogOnSQLServer(t *testing.T) {
	db := catalogServer(t)
	const data = "/var/opt/mssql/data/"
	execAll(t, db,
		// sales: FULL, the server collation, one data and one log file, owned by sa.
		`CREATE DATABASE sales ON (NAME = sales, FILENAME = '`+data+`sales.mdf', SIZE = 16MB)
			LOG ON (NAME = sales_log, FILENAME = '`+data+`sales_log.ldf', SIZE = 8MB)`,
		`ALTER DATABASE sales SET RECOVERY FULL`,
		`ALTER DATABASE sales SET COMPATIBILITY_LEVEL = 140`,
		`CREATE TABLE sales.dbo.orders (id int PRIMARY KEY, customer nvarchar(50))`,
		`INSERT sales.dbo.orders VALUES (1, N'a'), (2, N'b'), (3, N'c')`,
		// archive: SIMPLE, own collation, two data files, another owner, read-only, restricted.
		`CREATE DATABASE archive ON
			(NAME = archive, FILENAME = '`+data+`archive.mdf', SIZE = 8MB),
			(NAME = archive_2, FILENAME = '`+data+`archive_2.ndf', SIZE = 8MB)
			LOG ON (NAME = archive_log, FILENAME = '`+data+`archive_log.ldf', SIZE = 9MB)
			COLLATE Latin1_General_100_CI_AS`,
		`ALTER DATABASE archive SET RECOVERY SIMPLE`,
		`ALTER DATABASE archive SET COMPATIBILITY_LEVEL = 130`,
		`CREATE TABLE archive.dbo.items (id int PRIMARY KEY, note nvarchar(50))`,
		`INSERT archive.dbo.items VALUES (1, N'kept')`,
		`CREATE LOGIN appowner WITH PASSWORD = '`+"Dg1!"+rand.Text()+`', CHECK_POLICY = OFF`,
		`ALTER AUTHORIZATION ON DATABASE::archive TO appowner`,
		`ALTER DATABASE archive SET READ_ONLY`,
		`ALTER DATABASE archive SET RESTRICTED_USER`,
		// sales_copy: a restore left WITH NORECOVERY, as during a differential chain.
		`BACKUP DATABASE sales TO DISK = '`+data+`sales.bak' WITH INIT`,
		`RESTORE DATABASE sales_copy FROM DISK = '`+data+`sales.bak' WITH NORECOVERY,
			MOVE 'sales' TO '`+data+`sales_copy.mdf', MOVE 'sales_log' TO '`+data+`sales_copy.ldf'`,
		// legacy: taken offline.
		`CREATE DATABASE legacy ON (NAME = legacy, FILENAME = '`+data+`legacy.mdf', SIZE = 8MB)
			LOG ON (NAME = legacy_log, FILENAME = '`+data+`legacy_log.ldf', SIZE = 8MB)`,
		`ALTER DATABASE legacy SET OFFLINE`,
		// rdsadmin is the RDS database: never listed.
		`CREATE DATABASE rdsadmin`,
		// The native tasks, as the rds_* mock has them.
		`CREATE TABLE msdb.dbo.mock_tasks (task_id int IDENTITY PRIMARY KEY, lifecycle nvarchar(20))`,
		`INSERT msdb.dbo.mock_tasks (lifecycle) VALUES (N'SUCCESS'), (N'IN_PROGRESS'), (N'CREATED'), (N'ERROR')`,
		`EXEC msdb.sys.sp_executesql N'CREATE FUNCTION dbo.rds_fn_task_status(@db sysname, @id int)
			RETURNS TABLE AS RETURN SELECT task_id, lifecycle FROM dbo.mock_tasks WHERE @id = 0 OR task_id = @id'`,
	)
	var serverCollation string
	if err := db.QueryRow(`SELECT CAST(SERVERPROPERTY('Collation') AS nvarchar(128))`).Scan(&serverCollation); err != nil {
		t.Fatal(err)
	}

	c := Catalog{DB: db}
	list, err := c.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Database{
		{Name: "archive", State: "ONLINE", Recovery: "SIMPLE", Compatibility: 130, Collation: "Latin1_General_100_CI_AS",
			Owner: "appowner", UserAccess: "RESTRICTED_USER", ReadOnly: true, DataMB: 16, LogMB: 9},
		// Offline: no collation.
		{Name: "legacy", State: "OFFLINE", Recovery: "FULL", Compatibility: 160,
			Owner: "sa", UserAccess: "MULTI_USER", DataMB: 8, LogMB: 8},
		{Name: "sales", State: "ONLINE", Recovery: "FULL", Compatibility: 140, Collation: serverCollation,
			Owner: "sa", UserAccess: "MULTI_USER", DataMB: 16, LogMB: 8},
		{Name: "sales_copy", State: "RESTORING", Recovery: "FULL", Compatibility: 140,
			Collation: serverCollation, Owner: "sa", UserAccess: "MULTI_USER", DataMB: 16, LogMB: 8},
	}
	if len(list) != len(want) {
		t.Fatalf("got %d databases, want %d: %+v", len(list), len(want), list)
	}
	for i, got := range list {
		if _, err := time.Parse("2006-01-02T15:04:05", got.CreatedAt); err != nil {
			t.Errorf("%s: createdAt %q: %v", got.Name, got.CreatedAt, err)
		}
		got.CreatedAt = ""
		if got != want[i] {
			t.Errorf("database %d\n got  %+v\n want %+v", i, got, want[i])
		}
	}

	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u != (Usage{UserDatabases: 4, ActiveTasks: 2}) {
		t.Fatalf("usage %+v, want 4 user databases and 2 active tasks", u)
	}
}

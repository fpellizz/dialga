//go:build integration

package mssql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net/url"
	"testing"
)

func TestCollationCommonOnSQLServer(t *testing.T) {
	db := catalogServer(t)
	execAll(t, db,
		`CREATE DATABASE coll0 COLLATE Latin1_General_100_CS_AS`,
		`CREATE DATABASE coll0_ro COLLATE Latin1_General_100_CI_AS_SC_UTF8`,
		`ALTER DATABASE coll0_ro SET READ_ONLY`,
		`ALTER DATABASE coll0_ro SET RESTRICTED_USER`,
	)
	ctx := context.Background()
	s := Session{DB: db}

	server, err := s.ServerCollation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if server.Server == "" || server.Tempdb != server.Server || server.Model == "" || server.ProductVersion == "" || server.Major < 16 {
		t.Fatalf("ServerCollation = %+v", server)
	}

	info, found, err := s.Collation(ctx, "Latin1_General_100_CI_AS_SC_UTF8")
	if err != nil || !found {
		t.Fatalf("Collation: found %v, err %v", found, err)
	}
	want := TraitsFromStyle("Latin1_General_100_CI_AS_SC_UTF8", 196609)
	want.Description, want.CodePage, want.Version = info.Description, 65001, 2
	if info != want || info.Description == "" {
		t.Fatalf("Collation = %+v, want %+v", info, want)
	}
	if _, found, err := s.Collation(ctx, "No_Such_Collation"); err != nil || found {
		t.Fatalf("unknown collation: found %v, err %v", found, err)
	}

	st, err := s.CollationState(ctx, "coll0")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Collation != "Latin1_General_100_CS_AS" || st.State != "ONLINE" || st.Containment != "NONE" ||
		st.UserAccess != "MULTI_USER" || st.ReadOnly || !st.CanAlter || st.AvailabilityReplica || st.Mirrored {
		t.Fatalf("CollationState(coll0) = %+v", st)
	}
	st, err = s.CollationState(ctx, "coll0_ro")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || !st.ReadOnly || st.UserAccess != "RESTRICTED_USER" || st.Collation != "Latin1_General_100_CI_AS_SC_UTF8" {
		t.Fatalf("CollationState(coll0_ro) = %+v", st)
	}
	if st, err := s.CollationState(ctx, "missing"); err != nil || st.Exists {
		t.Fatalf("CollationState(missing) = %+v, %v", st, err)
	}

	// A login without VIEW SERVER STATE (the review of the collation work:
	// sys.dm_hadr_database_replica_states refused it) reads the database it
	// owns.
	password := "Dg1!" + rand.Text()
	execAll(t, db,
		`CREATE LOGIN plain WITH PASSWORD = '`+password+`', CHECK_POLICY = OFF`,
		`CREATE DATABASE coll0_own COLLATE Latin1_General_100_CS_AS`,
		`ALTER AUTHORIZATION ON DATABASE::coll0_own TO plain`,
	)
	u := catalogURL
	u.User = url.UserPassword("plain", password)
	plain, err := sql.Open("sqlserver", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	var perm int
	if err := plain.QueryRowContext(ctx, `SELECT HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE')`).Scan(&perm); err != nil || perm != 0 {
		t.Fatalf("the login has VIEW SERVER STATE: %d %v", perm, err)
	}
	st, err = Session{DB: plain}.CollationState(ctx, "coll0_own")
	if err != nil || !st.Exists || !st.CanAlter || st.AvailabilityReplica || st.Mirrored || st.Collation != "Latin1_General_100_CS_AS" {
		t.Fatalf("CollationState without VIEW SERVER STATE = %+v, %v", st, err)
	}
}

package api

import (
	"context"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/session"
)

func init() { extraRoutes = append(extraRoutes, (*Server).databaseRoutes) }

func (s *Server) databaseRoutes() []route {
	return []route{
		{http.MethodGet, "/api/v1/targets/{name}/databases", s.listDatabases},
		{http.MethodGet, "/api/v1/targets/{name}/overview", s.targetOverview},
	}
}

// DatabaseList is the answer of GET /api/v1/targets/{name}/databases.
type DatabaseList struct {
	Databases []mssql.Database `json:"databases"`
}

// What the SQL part of an overview did.
const (
	overviewSQLOK      = "ok"
	overviewSQLSkipped = "skipped" // no password, or connect.mode tunnel
	overviewSQLFailed  = "failed"  // the session or the query failed
)

// overviewSQLTimeout bounds the SQL part, so a stuck tunnel does not hold
// the AWS part back.
const overviewSQLTimeout = 30 * time.Second

// Overview is the answer of GET /api/v1/targets/{name}/overview: the RDS
// instance as AWS describes it and, when a SQL session opens, its user
// databases and unfinished native tasks. The AWS part needs no SQL
// password: without one SQL is "skipped" and SQLDetail says why.
type Overview struct {
	Target        string `json:"target"`
	Instance      string `json:"instance"`
	Status        string `json:"status"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion"`
	Class         string `json:"class"`
	AllocatedGiB  int    `json:"allocatedGiB"`
	// MaxAllocatedGiB is the storage autoscaling limit (absent: none).
	MaxAllocatedGiB int `json:"maxAllocatedGiB,omitempty"`
	// FreeBytes is the free storage from CloudWatch; FreeError says why it
	// is missing.
	FreeBytes *int64 `json:"freeBytes,omitempty"`
	FreeError string `json:"freeError,omitempty"`
	// SQL is ok, skipped or failed; SQLDetail says why it is not ok.
	SQL           string `json:"sql"`
	SQLDetail     string `json:"sqlDetail,omitempty"`
	UserDatabases *int   `json:"userDatabases,omitempty"`
	// ActiveTasks counts the native tasks queued, running or being cancelled.
	ActiveTasks *int `json:"activeTasks,omitempty"`
}

// databaseTarget resolves the target of the request, answering 409 (no
// configuration) or 404 (unknown target) itself.
func (s *Server) databaseTarget(w http.ResponseWriter, r *http.Request) (string, config.Target, bool) {
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return "", config.Target{}, false
	}
	name, t, err := cfg.Target(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return "", config.Target{}, false
	}
	return name, t, true
}

// listDatabases answers the user databases of a target, by name. Each call
// opens its own SQL session.
func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.databaseTarget(w, r)
	if !ok {
		return
	}
	if t.Connect.Mode == config.ModeTunnel {
		http.Error(w, config.ErrTunnelNotYet.Error(), http.StatusUnprocessableEntity)
		return
	}
	password, err := session.PasswordFromEnv(t, s.opts.Getenv)
	if err != nil {
		http.Error(w, "SQL password: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	dbs, closeDBs, err := s.opts.OpenDatabases(r.Context(), t, password)
	if err != nil {
		if r.Context().Err() == nil { // not a client that went away
			s.opts.Log.Warn("databases: session failed", "target", name, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer closeDBs()
	list, err := dbs.Databases(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, DatabaseList{Databases: list})
}

// targetOverview answers the state of a target's instance. The AWS and SQL
// parts run side by side; only an AWS failure fails the call (502), a SQL
// one comes back in the answer.
func (s *Server) targetOverview(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.databaseTarget(w, r)
	if !ok {
		return
	}
	var (
		wg        sync.WaitGroup
		info      session.InstanceInfo
		awsErr    error
		sqlResult Overview
	)
	wg.Go(func() { info, awsErr = s.opts.DescribeTarget(r.Context(), t) })
	wg.Go(func() { sqlResult = s.overviewSQL(r.Context(), name, t) })
	wg.Wait()
	if awsErr != nil {
		http.Error(w, awsErr.Error(), http.StatusBadGateway)
		return
	}
	inst := info.Instance
	o := Overview{
		Target: name, Instance: t.Instance, Status: inst.Status, Engine: inst.Engine,
		EngineVersion: inst.EngineVersion, Class: inst.Class,
		AllocatedGiB: inst.AllocatedGiB, MaxAllocatedGiB: inst.MaxAllocatedGiB,
		SQL: sqlResult.SQL, SQLDetail: sqlResult.SQLDetail,
		UserDatabases: sqlResult.UserDatabases, ActiveTasks: sqlResult.ActiveTasks,
	}
	if info.FreeErr != nil {
		o.FreeError = "free space not read from CloudWatch: " + info.FreeErr.Error()
	} else {
		free := int64(math.Round(info.FreeBytes))
		o.FreeBytes = &free
	}
	writeJSON(w, o)
}

// overviewSQL fills the SQL fields of an overview.
func (s *Server) overviewSQL(ctx context.Context, name string, t config.Target) Overview {
	if t.Connect.Mode == config.ModeTunnel {
		return Overview{SQL: overviewSQLSkipped, SQLDetail: config.ErrTunnelNotYet.Error()}
	}
	password, err := session.PasswordFromEnv(t, s.opts.Getenv)
	if err != nil {
		return Overview{SQL: overviewSQLSkipped, SQLDetail: "SQL password: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, overviewSQLTimeout)
	defer cancel()
	dbs, closeDBs, err := s.opts.OpenDatabases(ctx, t, password)
	if err != nil {
		s.opts.Log.Warn("overview: session failed", "target", name, "err", err)
		return Overview{SQL: overviewSQLFailed, SQLDetail: err.Error()}
	}
	defer closeDBs()
	u, err := dbs.Usage(ctx)
	if err != nil {
		return Overview{SQL: overviewSQLFailed, SQLDetail: err.Error()}
	}
	return Overview{SQL: overviewSQLOK, UserDatabases: &u.UserDatabases, ActiveTasks: &u.ActiveTasks}
}

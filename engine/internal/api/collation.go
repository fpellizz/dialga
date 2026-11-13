package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() { extraRoutes = append(extraRoutes, (*Server).collationRoutes) }

// collationRoutes are the collation views (read-only, no job). The
// operations (set-collation, convert-columns) are plans like the others
// (ops.go).
func (s *Server) collationRoutes() []route {
	return []route{
		{http.MethodGet, "/api/v1/targets/{name}/collation", s.targetCollation},
		{http.MethodGet, "/api/v1/targets/{name}/collations", s.targetCollations},
		{http.MethodGet, "/api/v1/targets/{name}/databases/{db}/collation", s.databaseCollation},
	}
}

// CollationList is the answer of GET .../collations.
type CollationList struct {
	Collations []mssql.CollationInfo `json:"collations"`
}

// maxCollationFilter bounds the ?q= of the list of collations.
const maxCollationFilter = 128

// targetCollation answers the collation overview of a target.
func (s *Server) targetCollation(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.opTarget(w, r, func(config.Target) error { return nil })
	if !ok {
		return
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return
	}
	defer closeOps()
	o, err := sess.CollationOverview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, o)
}

// targetCollations lists the collations of the instance (?q= filters them
// by a case-insensitive part of the name).
func (s *Server) targetCollations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	name, t, ok := s.opTarget(w, r, func(config.Target) error {
		if len(q) > maxCollationFilter {
			return fmt.Errorf("q is longer than %d characters", maxCollationFilter)
		}
		return nil
	})
	if !ok {
		return
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return
	}
	defer closeOps()
	all, err := sess.Collations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	list := []mssql.CollationInfo{}
	needle := strings.ToLower(q)
	for _, c := range all {
		if needle == "" || strings.Contains(strings.ToLower(c.Name), needle) {
			list = append(list, c)
		}
	}
	writeJSON(w, CollationList{Collations: list})
}

// databaseCollation answers the collation view of a database (read-only,
// no job).
func (s *Server) databaseCollation(w http.ResponseWriter, r *http.Request) {
	db := r.PathValue("db")
	name, t, ok := s.opTarget(w, r, func(config.Target) error { return validate.DBName(db) })
	if !ok {
		return
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return
	}
	defer closeOps()
	v, err := sess.DatabaseCollation(r.Context(), db)
	switch {
	case errors.Is(err, mssql.ErrNoDatabase):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ops.ErrNotOnline):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, v)
	}
}

// Work package A: see the collation design.

package api

import (
	"net/http"

	"github.com/decisyon/dialga/engine/internal/mssql"
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

const collationNotYet = "collation: not implemented yet"

// targetCollation answers the collation overview of a target.
func (s *Server) targetCollation(w http.ResponseWriter, r *http.Request) {
	http.Error(w, collationNotYet, http.StatusNotImplemented)
}

// targetCollations lists the collations of the instance (?q= filters them).
func (s *Server) targetCollations(w http.ResponseWriter, r *http.Request) {
	http.Error(w, collationNotYet, http.StatusNotImplemented)
}

// databaseCollation answers the collation view of a database.
func (s *Server) databaseCollation(w http.ResponseWriter, r *http.Request) {
	http.Error(w, collationNotYet, http.StatusNotImplemented)
}

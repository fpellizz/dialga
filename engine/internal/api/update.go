package api

import (
	"net/http"

	"github.com/decisyon/dialga/engine/internal/update"
)

// GET /api/v1/update: is a newer release published on GitHub? The answer
// comes from a 12 hour cache; ?force=1 asks GitHub again. A failed check is
// not an HTTP error: the last good answer comes back with the reason in
// "error".

func init() { extraRoutes = append(extraRoutes, (*Server).updateRoutes) }

func (s *Server) updateRoutes() []route {
	return []route{{http.MethodGet, "/api/v1/update", s.updateInfo}}
}

// UpdateDisabledEnv, set to 1 in the engine's environment, turns the check
// off: the endpoint then answers without any network access.
const UpdateDisabledEnv = "DIALGA_NO_UPDATE_CHECK"

func (s *Server) updateInfo(w http.ResponseWriter, r *http.Request) {
	if s.opts.Getenv(UpdateDisabledEnv) == "1" {
		writeJSON(w, update.Info{Current: s.opts.Version, Error: "update check disabled by " + UpdateDisabledEnv})
		return
	}
	writeJSON(w, s.opts.Updates.Check(r.Context(), r.URL.Query().Get("force") == "1"))
}

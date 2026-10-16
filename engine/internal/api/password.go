package api

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/session"
)

// maxPasswordBytes is the longest password PUT accepts.
const maxPasswordBytes = 1024

// sqlLoginFailed is the SQL Server error number of a failed login.
const sqlLoginFailed = 18456

// PasswordInfo is the password block of a target: where it comes from and
// whether the engine can use it without asking. The password itself never
// leaves the engine.
type PasswordInfo struct {
	// Source is prompt, env, keyring or secretsmanager.
	Source string `json:"source"`
	// Available: the engine can resolve it now (memory, environment
	// variable, keyring entry; secretsmanager is read when used).
	Available bool `json:"available"`
	// CanRemember: a system keyring is usable on this machine.
	CanRemember bool `json:"canRemember"`
}

// PasswordRequest is the body of PUT /api/v1/targets/{name}/password.
type PasswordRequest struct {
	Password string `json:"password"`
	// Remember also stores it in the system keyring.
	Remember bool `json:"remember"`
}

func init() { extraRoutes = append(extraRoutes, (*Server).passwordRoutes) }

func (s *Server) passwordRoutes() []route {
	return []route{
		{http.MethodPut, "/api/v1/targets/{name}/password", s.putPassword},
		{http.MethodDelete, "/api/v1/targets/{name}/password", s.deletePassword},
	}
}

func (s *Server) passwordInfo(name string, t config.Target) PasswordInfo {
	st := s.opts.Passwords.Status(name, t)
	return PasswordInfo{Source: st.Source, Available: st.Available, CanRemember: st.CanRemember}
}

// writePasswordNeeded answers 428: the client asks the user for the password
// of the target in the header and sends it with PUT .../password.
func writePasswordNeeded(w http.ResponseWriter, name string) {
	w.Header().Set("Dialga-Password-Target", name)
	http.Error(w, "SQL password needed for target "+name, http.StatusPreconditionRequired)
}

// sqlPassword resolves the SQL password of target name, answering itself
// when it cannot: 428 when the user has to supply it, 422 when its source
// (an environment variable, Secrets Manager) did not give one.
func (s *Server) sqlPassword(ctx context.Context, w http.ResponseWriter, name string, t config.Target) (string, bool) {
	password, err := s.opts.Passwords.Resolve(ctx, name, t)
	switch {
	case err == nil:
		return password, true
	case errors.Is(err, session.ErrNeeded):
		writePasswordNeeded(w, name)
	default:
		http.Error(w, "SQL password: "+err.Error(), http.StatusUnprocessableEntity)
	}
	return "", false
}

func (s *Server) putPassword(w http.ResponseWriter, r *http.Request) {
	var body PasswordRequest
	if !readBodyMax(w, r, &body, 1<<14) {
		return
	}
	if body.Password == "" || len(body.Password) > maxPasswordBytes || !utf8.ValidString(body.Password) {
		http.Error(w, "password must be 1 to 1024 bytes of text", http.StatusBadRequest)
		return
	}
	name, t, ok := s.databaseTarget(w, r)
	if !ok {
		return
	}
	if err := s.opts.Passwords.CheckSettable(t); err != nil {
		code := http.StatusUnprocessableEntity
		if errors.Is(err, session.ErrFixedSource) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	if body.Remember && !s.opts.Passwords.Status(name, t).CanRemember {
		http.Error(w, "no system keyring is usable on this machine: the password cannot be remembered",
			http.StatusNotImplemented)
		return
	}
	if r.URL.Query().Get("verify") == "1" {
		dbs, closeDBs, err := s.opts.OpenDatabases(r.Context(), t, body.Password)
		if err != nil {
			if r.Context().Err() == nil {
				s.opts.Log.Warn("password: verification failed", "target", name, "err", err)
			}
			code := http.StatusBadGateway
			var sqlErr mssql.Error
			if errors.As(err, &sqlErr) && sqlErr.Number == sqlLoginFailed {
				code = http.StatusUnprocessableEntity
			}
			http.Error(w, err.Error(), code)
			return
		}
		_ = dbs
		closeDBs()
	}
	if err := s.opts.Passwords.Set(name, t, body.Password, body.Remember); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, session.ErrNoKeyring) {
			code = http.StatusNotImplemented
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePassword(w http.ResponseWriter, r *http.Request) {
	name, _, ok := s.databaseTarget(w, r)
	if !ok {
		return
	}
	if err := s.opts.Passwords.Forget(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

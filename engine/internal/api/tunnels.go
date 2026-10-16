package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnels"
)

func init() { extraRoutes = append(extraRoutes, (*Server).tunnelRoutes) }

func (s *Server) tunnelRoutes() []route {
	return []route{
		{http.MethodGet, "/api/v1/tunnels", s.listTunnels},
		{http.MethodGet, "/api/v1/targets/{name}/tunnel", s.getTunnel},
		{http.MethodPost, "/api/v1/targets/{name}/tunnel", s.openTunnel},
		{http.MethodDelete, "/api/v1/targets/{name}/tunnel", s.closeTunnel},
		{http.MethodGet, "/api/v1/targets/{name}/tunnel/events", s.tunnelEvents},
	}
}

// tunnelKeepAlive is how often an idle event stream sends a comment, so
// nothing in between drops it.
const tunnelKeepAlive = 20 * time.Second

// TunnelState is the shared tunnel of a tunnel-mode target.
type TunnelState struct {
	Target string `json:"target"`
	// Status is connecting, ready, reconnecting or closed.
	Status    string `json:"status"`
	LocalPort int    `json:"localPort,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Bridge    string `json:"bridge,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// Since is when Status began; absent for a tunnel this engine never opened.
	Since *time.Time `json:"since,omitempty"`
	// Users are the SQL sessions going through it now.
	Users int `json:"users"`
	// KeepOpen: opened on request, it stays until closed (no idle close).
	KeepOpen bool `json:"keepOpen"`
}

// TunnelList is the answer of GET /api/v1/tunnels: one entry per
// tunnel-mode target of the configuration.
type TunnelList struct {
	Tunnels []TunnelState `json:"tunnels"`
}

func tunnelState(name string, st tunnels.State) TunnelState {
	out := TunnelState{
		Target: name, Status: string(st.Status), LocalPort: st.LocalPort, Endpoint: st.Endpoint,
		Bridge: st.Bridge, Detail: st.Detail, Users: st.Users, KeepOpen: st.KeepOpen,
	}
	if !st.Since.IsZero() {
		since := st.Since
		out.Since = &since
	}
	return out
}

func (s *Server) listTunnels(w http.ResponseWriter, _ *http.Request) {
	resp := TunnelList{Tunnels: []TunnelState{}}
	cfg, err := config.Load(s.opts.ConfigPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First run: no targets, no tunnels.
	case err != nil:
		http.Error(w, err.Error(), http.StatusConflict)
		return
	default:
		for _, name := range cfg.Names() {
			if t := cfg.Targets[name]; t.Connect.Mode == config.ModeTunnel {
				resp.Tunnels = append(resp.Tunnels, tunnelState(name, s.opts.Tunnels.State(t)))
			}
		}
	}
	writeJSON(w, resp)
}

// tunnelTarget resolves the target of the request, which must be in tunnel
// mode, answering 409, 404 or 422 itself.
func (s *Server) tunnelTarget(w http.ResponseWriter, r *http.Request) (string, config.Target, bool) {
	name, t, ok := s.databaseTarget(w, r)
	if ok && t.Connect.Mode != config.ModeTunnel {
		http.Error(w, fmt.Sprintf("connect.mode of %s is %s: the engine manages tunnels of mode tunnel only",
			name, t.Connect.Mode), http.StatusUnprocessableEntity)
		return "", config.Target{}, false
	}
	return name, t, ok
}

func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request) {
	if name, t, ok := s.tunnelTarget(w, r); ok {
		writeJSON(w, tunnelState(name, s.opts.Tunnels.State(t)))
	}
}

// openTunnel opens the tunnel now and keeps it open until DELETE; it
// answers once the tunnel is ready (or failed), the events show the way.
func (s *Server) openTunnel(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.tunnelTarget(w, r)
	if !ok {
		return
	}
	st, err := s.opts.Tunnels.Open(r.Context(), t)
	switch {
	case errors.Is(err, tunnels.ErrClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case err != nil:
		if r.Context().Err() == nil { // not a client that went away
			s.opts.Log.Warn("tunnel: open failed", "target", name, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, tunnelState(name, st))
	}
}

func (s *Server) closeTunnel(w http.ResponseWriter, r *http.Request) {
	if name, t, ok := s.tunnelTarget(w, r); ok {
		writeJSON(w, tunnelState(name, s.opts.Tunnels.CloseTarget(t)))
	}
}

// tunnelEvents streams the state of the tunnel as "state" events: the
// current one first, then every change, until the client goes away or the
// engine stops.
func (s *Server) tunnelEvents(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.tunnelTarget(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	states, cancel := s.opts.Tunnels.Subscribe(t)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	keepAlive := time.NewTicker(tunnelKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case st, ok := <-states:
			if !ok {
				return
			}
			data, _ := json.Marshal(tunnelState(name, st))
			_, _ = fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		case <-r.Context().Done():
			return
		}
		flusher.Flush()
	}
}

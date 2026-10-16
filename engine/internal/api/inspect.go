package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/inspect"
)

func init() { extraRoutes = append(extraRoutes, (*Server).inspectRoutes) }

// InspectFunc runs an inspection (default inspect.Run; tests replace it).
type InspectFunc func(context.Context, inspect.Request, check.Emitter) (*inspect.Result, error)

func (s *Server) inspectRoutes() []route {
	return []route{{http.MethodPost, "/api/v1/inspect", s.inspect}}
}

// InspectRequest is the body of POST /api/v1/inspect. The engine runs on the
// workstation: the paths are local files the user chose in the GUI.
type InspectRequest struct {
	Paths []string `json:"paths"`
	// Target judges the backups against its engine; empty reads the headers only.
	Target string `json:"target,omitempty"`
	// Image replaces the SQL Server image that reads the backups.
	Image string `json:"image,omitempty"`
}

// inspect streams an inspection as Server-Sent Events: "step" while it runs,
// "result" (inspect.Result) when it completes, or "notice" with the error
// when it stops, then "done". The body is checked before the stream starts:
// 400 for bad paths, 404/409 for the target.
func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	var body InspectRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, p := range body.Paths {
		if !filepath.IsAbs(p) {
			http.Error(w, fmt.Sprintf("%s: paths must be absolute", p), http.StatusBadRequest)
			return
		}
	}
	if _, err := inspect.ResolvePaths(body.Paths); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Image != "" {
		if err := inspect.ValidateImage(body.Image); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	req := inspect.Request{Paths: body.Paths, Image: body.Image}
	if body.Target != "" {
		cfg, err := config.Load(s.opts.ConfigPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		name, t, err := cfg.Target(body.Target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		req.Target = inspect.TargetFor(name, t)
	}
	run := s.opts.RunInspect
	if run == nil {
		run = inspect.Run
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	s.opts.Log.Info("inspect started", "files", len(body.Paths), "target", body.Target)
	res, err := run(r.Context(), req, func(e check.Event) { send("step", e) })
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.opts.Log.Warn("inspect failed", "err", err)
		}
		send("notice", Notice{Message: "inspection stopped: " + err.Error()})
		send("done", Done{OK: false})
		return
	}
	send("result", res)
	send("done", Done{OK: res.Outcome == inspect.Restorable || res.Outcome == inspect.Read})
	s.opts.Log.Info("inspect finished", "outcome", res.Outcome)
}

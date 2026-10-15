// Package api serves the engine's local HTTP API to the desktop shell.
//
// Hardening, because any local process could try to reach it:
//   - it listens on 127.0.0.1 only, on a random port;
//   - every request needs the per-session bearer token the shell generated
//     and passed on stdin (constant-time comparison);
//   - the Host header must be a loopback name (DNS rebinding) and a
//     browser Origin, when present, must be in the allowlist (CORS).
//
// The configuration file is read on every request, so edits show up without
// restarting the app. Passwords never travel in requests or responses: the
// engine takes them from its own environment.
//
// Long operations stream their progress as Server-Sent Events over the
// response of a POST, read with fetch() (EventSource cannot send headers).
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
)

// DefaultOrigins are the webview origins of the Tauri shell: Linux/macOS
// (custom scheme) and Windows.
var DefaultOrigins = []string{"tauri://localhost", "http://tauri.localhost"}

// Options configures the server.
type Options struct {
	Token          string
	AllowedOrigins []string
	Version        string
	// ConfigPath is the targets file, read on every request.
	ConfigPath string
	// Getenv resolves the SQL password references (default os.Getenv).
	Getenv func(string) string
	// RunCheck runs a check (default check.Run; tests replace it).
	RunCheck func(context.Context, check.Request, string, check.Emitter) bool
	Log      *slog.Logger
}

// Server is the HTTP API of the engine.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// TargetSummary is a target as the GUI sees it: no password reference.
type TargetSummary struct {
	Name        string `json:"name"`
	Instance    string `json:"instance"`
	Region      string `json:"region"`
	Profile     string `json:"profile"`
	AccountID   string `json:"accountId"`
	Bucket      string `json:"bucket"`
	SQLUser     string `json:"sqlUser"`
	ConnectMode string `json:"connectMode"`
	LocalPort   int    `json:"localPort,omitempty"`
	Protected   bool   `json:"protected"`
}

// TargetList is the answer of GET /api/v1/targets. A missing file is not an
// error (first run): Targets is empty and ConfigPath says where to create it.
type TargetList struct {
	ConfigPath string          `json:"configPath"`
	Error      string          `json:"error,omitempty"`
	Targets    []TargetSummary `json:"targets"`
}

// New builds a server; Token must be at least 32 characters.
func New(opts Options) (*Server, error) {
	if len(opts.Token) < 32 {
		return nil, errors.New("api: token too short")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.RunCheck == nil {
		opts.RunCheck = check.Run
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	s.mux.HandleFunc("GET /api/v1/targets", s.listTargets)
	s.mux.HandleFunc("POST /api/v1/targets/{name}/checks", s.checkTarget)
	return s, nil
}

// Handler returns the guarded handler.
func (s *Server) Handler() http.Handler { return s.guard(s.mux) }

// Serve listens on 127.0.0.1 (random port), writes one JSON ready line to
// ready ({"event":"ready","port":N,"version":"…"}) and serves until ctx ends.
func Serve(ctx context.Context, opts Options, ready io.Writer) error {
	s, err := New(opts)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	port := ln.Addr().(*net.TCPAddr).Port
	line, _ := json.Marshal(map[string]any{"event": "ready", "port": port, "version": opts.Version})
	if _, err := fmt.Fprintf(ready, "%s\n", line); err != nil {
		_ = ln.Close()
		return err
	}
	s.opts.Log.Info("api listening", "addr", ln.Addr().String(), "config", opts.ConfigPath)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !slices.Contains(s.opts.AllowedOrigins, origin) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) == 1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "version": s.opts.Version})
}

func (s *Server) listTargets(w http.ResponseWriter, _ *http.Request) {
	resp := TargetList{ConfigPath: s.opts.ConfigPath, Targets: []TargetSummary{}}
	cfg, err := config.Load(s.opts.ConfigPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First run: no file yet.
	case err != nil:
		resp.Error = err.Error()
	default:
		for _, name := range cfg.Names() {
			t := cfg.Targets[name]
			resp.Targets = append(resp.Targets, TargetSummary{
				Name: name, Instance: t.Instance, Region: t.Region, Profile: t.AWSProfile,
				AccountID: t.AccountID, Bucket: t.Bucket, SQLUser: t.SQL.User,
				ConnectMode: t.Connect.Mode, LocalPort: t.Connect.LocalPort, Protected: t.Protected,
			})
		}
	}
	writeJSON(w, resp)
}

func (s *Server) checkTarget(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	name, t, err := cfg.Target(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	req, err := check.RequestFor(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
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
	// Without a password the SQL steps are skipped; say why up front.
	password, perr := check.PasswordFromEnv(t, s.opts.Getenv)
	if perr != nil {
		send("notice", map[string]string{"message": "SQL login will be skipped: " + perr.Error()})
	}
	s.opts.Log.Info("check started", "target", name, "instance", t.Instance)
	passed := s.opts.RunCheck(r.Context(), req, password, func(e check.Event) { send("step", e) })
	send("done", map[string]bool{"ok": passed})
	s.opts.Log.Info("check finished", "target", name, "ok", passed)
}

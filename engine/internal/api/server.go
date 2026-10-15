// Package api serves the engine's local HTTP API to the desktop shell.
//
// Hardening, because any local process could try to reach it:
//   - it listens on 127.0.0.1 only, on a random port;
//   - every request needs the per-session bearer token the shell generated
//     and passed on stdin (constant-time comparison);
//   - the Host header must be a loopback name (DNS rebinding) and a
//     browser Origin, when present, must be in the allowlist (CORS).
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
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
)

// DefaultOrigins are the webview origins of the Tauri shell: Linux/macOS
// (custom scheme) and Windows.
var DefaultOrigins = []string{"tauri://localhost", "http://tauri.localhost"}

// Options configures the server.
type Options struct {
	Token          string
	AllowedOrigins []string
	Version        string
	// SQLPassword is read from the engine environment, never from requests.
	SQLPassword string
	Log         *slog.Logger
}

// Server is the HTTP API of the engine.
type Server struct {
	opts Options
	mux  *http.ServeMux
}

// New builds a server; Token must not be empty.
func New(opts Options) (*Server, error) {
	if len(opts.Token) < 32 {
		return nil, errors.New("api: token too short")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	s.mux.HandleFunc("POST /api/v1/checks", s.runCheck)
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
	s.opts.Log.Info("api listening", "addr", ln.Addr().String())

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

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": s.opts.Version})
}

func (s *Server) runCheck(w http.ResponseWriter, r *http.Request) {
	var req check.Request
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
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
	s.opts.Log.Info("check started", "instance", req.Instance, "profile", req.Profile)
	passed := check.Run(r.Context(), req, s.opts.SQLPassword, func(e check.Event) { send("step", e) })
	send("done", map[string]bool{"ok": passed})
	s.opts.Log.Info("check finished", "instance", req.Instance, "ok", passed)
}

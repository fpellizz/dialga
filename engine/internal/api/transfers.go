package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

func init() { extraRoutes = append(extraRoutes, (*Server).transferRoutes) }

func (s *Server) transferRoutes() []route {
	return []route{
		{http.MethodPost, "/api/v1/targets/{name}/uploads", s.createUploads},
		{http.MethodPost, "/api/v1/targets/{name}/downloads", s.createDownload},
	}
}

// UploadsRequest is the body of POST /api/v1/targets/{name}/uploads. The
// paths are absolute, on the machine of the engine.
type UploadsRequest struct {
	Files       []string `json:"files"`
	Key         string   `json:"key,omitempty"`
	Prefix      *string  `json:"prefix,omitempty"`
	Overwrite   bool     `json:"overwrite,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	Apply       bool     `json:"apply,omitempty"`
	Confirm     string   `json:"confirm,omitempty"`
}

// DownloadRequest is the body of POST /api/v1/targets/{name}/downloads.
type DownloadRequest struct {
	// Key is a key, s3://<bucket of the target>/<key>, or a bare name
	// under the export prefix.
	Key string `json:"key"`
	// Dest is the absolute path of the local file, or of an existing folder.
	Dest        string `json:"dest"`
	Overwrite   bool   `json:"overwrite,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Apply       bool   `json:"apply,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
}

// TransferDone is the data of the final "done" event of a transfer: ok
// when every file of the plan was transferred and none was rejected.
type TransferDone struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Results []transfer.Result `json:"results"`
}

const maxTransferBody = 1 << 20

func (s *Server) createUploads(w http.ResponseWriter, r *http.Request) {
	var req UploadsRequest
	if !decodeTransferBody(w, r, &req) {
		return
	}
	if len(req.Files) == 0 {
		http.Error(w, "files is empty", http.StatusBadRequest)
		return
	}
	for _, f := range req.Files {
		if !filepath.IsAbs(f) {
			http.Error(w, fmt.Sprintf("files must be absolute paths (got %q)", f), http.StatusBadRequest)
			return
		}
	}
	s.runTransfer(w, r, req.Apply, req.Confirm,
		func(ctx context.Context, c transfer.Client, name string, t config.Target, o transfer.Options) (*transfer.Batch, error) {
			o.Concurrency = req.Concurrency
			return transfer.PlanUploads(ctx, c, name, t, transfer.UploadRequest{
				Files: req.Files, Key: req.Key, Prefix: req.Prefix, Overwrite: req.Overwrite, Options: o,
			})
		})
}

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	var req DownloadRequest
	if !decodeTransferBody(w, r, &req) {
		return
	}
	switch {
	case req.Key == "":
		http.Error(w, "key is empty", http.StatusBadRequest)
		return
	case !filepath.IsAbs(req.Dest):
		http.Error(w, fmt.Sprintf("dest must be an absolute path (got %q)", req.Dest), http.StatusBadRequest)
		return
	}
	s.runTransfer(w, r, req.Apply, req.Confirm,
		func(ctx context.Context, c transfer.Client, name string, t config.Target, o transfer.Options) (*transfer.Batch, error) {
			o.Concurrency = req.Concurrency
			return transfer.PlanDownload(ctx, c, name, t, transfer.DownloadRequest{
				Source: req.Key, Dest: req.Dest, Overwrite: req.Overwrite, Options: o,
			})
		})
}

// decodeTransferBody reads the JSON body to its end: the server notices a
// client that goes away (and cancels the transfer) only once the body is
// consumed.
func decodeTransferBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, maxTransferBody)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		_, err = io.Copy(io.Discard, body)
	}
	if err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

type transferPlanner func(context.Context, transfer.Client, string, config.Target, transfer.Options) (*transfer.Batch, error)

// runTransfer plans a transfer and answers the plan (apply false: nothing
// changes) or runs it as an event stream (apply true): "plan", then "step"
// and "progress", then "done". A client that goes away cancels it.
func (s *Server) runTransfer(w http.ResponseWriter, r *http.Request, apply bool, confirm string, build transferPlanner) {
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
	open := s.opts.OpenS3
	if open == nil {
		open = transfer.Open
	}
	ctx := r.Context()
	client, err := open(ctx, t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Progress and steps come from other goroutines than this one.
	var mu sync.Mutex
	var flusher http.Flusher
	send := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	b, err := build(ctx, client, name, t, transfer.Options{OnProgress: func(p transfer.Progress) { send("progress", p) }})
	switch {
	case errors.Is(err, transfer.ErrInvalidRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	case !apply:
		writeJSON(w, b)
		return
	case b.Confirmation != "" && confirm != b.Confirmation:
		http.Error(w, fmt.Sprintf("not confirmed: this %s asks to retype %q in confirm", b.Plan.Operation, b.Confirmation),
			http.StatusUnprocessableEntity)
		return
	}
	var ok bool
	if flusher, ok = w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	send("plan", b)
	s.opts.Log.Info("transfer started", "target", name, "operation", b.Plan.Operation,
		"files", len(b.Items), "rejected", len(b.Rejected))
	err = b.Execute(ctx, confirm, func(e core.Event) { send("step", e) })
	done := TransferDone{OK: err == nil && len(b.Rejected) == 0, Results: b.Results()}
	if err != nil {
		done.Error = err.Error()
	}
	send("done", done)
	s.opts.Log.Info("transfer finished", "target", name, "operation", b.Plan.Operation, "ok", done.OK)
}

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

func init() { extraRoutes = append(extraRoutes, (*Server).jobRoutes) }

// sseKeepAlive is how often an idle event stream sends a comment line.
var sseKeepAlive = 15 * time.Second

// JobList is the answer of GET /api/v1/jobs.
type JobList struct {
	Jobs []jobs.Summary `json:"jobs"`
}

func (s *Server) jobRoutes() []route {
	return []route{
		{http.MethodGet, "/api/v1/jobs", s.listJobs},
		{http.MethodGet, "/api/v1/jobs/{id}", s.getJob},
		{http.MethodGet, "/api/v1/jobs/{id}/events", s.jobEvents},
		{http.MethodPost, "/api/v1/jobs/{id}/cancel", s.cancelJob},
	}
}

// jobID is the {id} of the path; an id that cannot exist is unknown too.
func jobID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !jobs.ValidID(id) {
		http.Error(w, jobs.ErrNotFound.Error(), http.StatusNotFound)
		return "", false
	}
	return id, true
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, JobList{Jobs: s.opts.Jobs.List()})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	job, err := s.opts.Jobs.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, job)
}

// cancelJob asks a job to stop. The answer comes before the steps have
// stopped (202): the job ends with a job.cancelled event.
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	sum, err := s.opts.Jobs.Cancel(id)
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, jobs.ErrEnded):
		http.Error(w, fmt.Sprintf("%v (%s)", err, sum.State), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.opts.Log.Info("job cancel requested", "job", id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(sum)
}

// jobEvents streams the events of a job as Server-Sent Events: the logged
// ones after Last-Event-ID (or ?after=), then the live ones until the job
// ends. The stream closes after the final job.* event; a stream that closes
// without it (the client fell behind, the engine stops) is resumed with
// Last-Event-ID.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	after := r.Header.Get("Last-Event-ID")
	if after == "" {
		after = r.URL.Query().Get("after")
	}
	var seq int64
	if after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "Last-Event-ID and after must be an event seq (0 or more)", http.StatusBadRequest)
			return
		}
		seq = n
	}
	sub, err := s.opts.Jobs.Subscribe(id, seq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer sub.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	send := func(ev jobs.Event) {
		data, _ := json.Marshal(ev)
		_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
	}
	for _, ev := range sub.Replay {
		send(ev)
	}
	flusher.Flush()
	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case ev, open := <-sub.C:
			if !open {
				return
			}
			send(ev)
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

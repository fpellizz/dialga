package api

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/jobs"
)

// A request to run something that is turned down leaves a failed job in the
// journal, so Activity shows what was tried and why it did not run: the
// pre-flight failed or the plan changed (412), the confirmation or the
// password is wrong or missing (422, 428), AWS or SQL Server did not answer
// (502). The handlers answer as they always did; the writer below sees the
// answer and the deferred recordRefusal writes the job.

// refusalStatuses are the answers that are recorded.
func refusalStatus(code int) bool {
	switch code {
	case http.StatusPreconditionFailed, http.StatusUnprocessableEntity,
		http.StatusPreconditionRequired, http.StatusBadGateway:
		return true
	}
	return false
}

// refusalWriter passes everything through and remembers the status and the
// start of the body of an error answer.
type refusalWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	// reason replaces the body as the reason; checks go with the refusal.
	reason string
	checks []check.Event
}

func (w *refusalWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *refusalWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && w.body.Len() < 2048 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *refusalWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection.
func (w *refusalWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// refused is what the request was about, filled in as it is known.
type refused struct {
	kind      jobs.Kind
	operation string
	items     []string
	// target is empty until the target is resolved: a request that failed
	// before (bad body, unknown target) is not a refusal.
	target, instance string
	// done: the handler recorded the job itself.
	done bool
}

// nonOK keeps the checks that failed or warn.
func nonOK(checks []check.Event) []check.Event {
	var out []check.Event
	for _, c := range checks {
		if c.Status != check.OK {
			out = append(out, c)
		}
	}
	return out
}

// recordRefusal writes the failed job of a refused request; deferred by the
// handler. A job that was started, or an answer that is not a refusal,
// records nothing.
func (s *Server) recordRefusal(w *refusalWriter, ref *refused) {
	if ref.done || ref.target == "" || !refusalStatus(w.status) || s.opts.Jobs == nil {
		return
	}
	reason := w.reason
	if reason == "" {
		reason = strings.TrimSpace(w.body.String())
	}
	f := jobs.Finished{
		Kind: ref.kind, Operation: ref.operation, Target: ref.target, Instance: ref.instance, State: jobs.Failed,
		Refusal: &jobs.Refusal{Status: w.status, Reason: reason, Checks: w.checks},
	}
	for _, name := range ref.items {
		f.Items = append(f.Items, jobs.FinishedItem{Name: name, Error: reason})
	}
	if _, err := s.opts.Jobs.Record(f); err != nil {
		s.opts.Log.Warn("job: cannot record a refused request", "target", ref.target, "err", err)
	}
}

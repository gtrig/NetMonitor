package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

// kind is the job's kind of run.
func (j Job) kind() string {
	switch {
	case j.Details != nil:
		return model.RunDetails
	case j.Ping != nil:
		return model.RunPing
	}
	return model.RunPresence
}

// trigger is what started a scan of this priority.
func (p Priority) trigger() string {
	switch p {
	case PrioManual:
		return "manual"
	case PrioAuto:
		return "automatic"
	}
	return "scheduled"
}

// record keeps a finished run for the Jobs page; start is zero for a run that never started.
// Outcome and message follow from err when it is set.
func (s *Scheduler) record(ctx context.Context, r model.JobRun, start time.Time, err error) {
	r.At = time.Now().UTC()
	if !start.IsZero() {
		r.Duration = r.At.Sub(start)
	}
	switch {
	case errors.Is(err, ErrCancelled):
		r.Outcome, r.Message = model.OutcomeCancelled, ""
	case err != nil && ctx.Err() != nil:
		if start.IsZero() {
			return // was still waiting; it is queued again after the restart
		}
		r.Outcome, r.Message = model.OutcomeInterrupted, "netmon was stopped or its settings were saved"
	case err != nil:
		r.Outcome, r.Message = model.OutcomeFailed, err.Error()
	case r.Outcome == "":
		r.Outcome = model.OutcomeOK
	}
	// Also while stopping, so not under ctx.
	if err := s.st.AddJobRun(context.WithoutCancel(ctx), r); err != nil {
		slog.Warn("storing job run failed", "network", r.Network, "source", r.Source, "err", err)
	}
}

// count is "1 host" or "3 hosts".
func count(n int, what string) string {
	if n != 1 {
		what += "s"
	}
	return fmt.Sprintf("%d %s", n, what)
}

func deviceSummary(h model.HostDetails) string {
	out := count(len(h.Ports), "open port")
	if h.OS != "" {
		out += ", " + h.OS
	}
	return out
}

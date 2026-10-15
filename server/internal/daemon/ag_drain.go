package daemon

// AG patch: daemon drain mode. The daemon stops claiming new tasks, lets the
// running ones finish, reports when it is idle, and resumes on request.
//
// Everything lives here so the upstream files only carry one-line hooks:
//   - daemon.go: the agDrain field, tryEnterClaim, tryBeginServerUpdate,
//     pollLoop (wakeup channel) and the handleUpdate switch;
//   - auto_update.go: trySetClaimBarrier;
//   - health.go: route registration and the /health fields.
//
// Drain is deliberately its own flag rather than a use of pauseClaims: several
// paths clear pauseClaims unconditionally (releaseClaimBarrier, the failure
// branch of tryBeginServerUpdate), and any of them would silently end a drain.
// The flag is held in memory only, so a restart always comes back claiming.
// A restart is what the flag guards against, which is why the restart paths
// refuse while it is set instead of the other way round.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// agDrainDeferredNote is appended to reload_pending_reason while a restart is
// being held back by a drain.
const agDrainDeferredNote = "restart deferred: daemon is draining"

// serverUpdateDraining is returned by tryBeginServerUpdate while draining. It
// extends the upstream serverUpdateAcquireResult enum without editing it.
const serverUpdateDraining serverUpdateAcquireResult = 100

// agDrainState is guarded by Daemon.claimMu, the same lock tryEnterClaim and
// the restart barriers take, so a drain and a claim decision can never
// interleave.
type agDrainState struct {
	draining  bool
	startedAt time.Time
	// wakeup is the batch poller's wakeup channel, registered by pollLoop so
	// resume can start a claim cycle at once instead of after PollInterval.
	wakeup chan struct{}
}

// agDrainHealth is embedded in HealthResponse; its fields are flattened into
// the /health JSON.
type agDrainHealth struct {
	Draining bool `json:"draining"`
	// DrainStartedAt is RFC3339 and omitted when the daemon is not draining.
	DrainStartedAt string `json:"drain_started_at,omitempty"`
	// Idle is true when draining and nothing is left: no active tasks
	// (including tasks parked on a local-directory lock) and no claim in flight.
	Idle bool `json:"idle"`
}

// agDrainingLocked reports whether the daemon is draining. claimMu must be held.
func (d *Daemon) agDrainingLocked() bool {
	return d.agDrain.draining
}

// agDraining reports whether the daemon is draining.
func (d *Daemon) agDraining() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	return d.agDrain.draining
}

// agSetPollerWakeup registers the batch poller's wakeup channel for resume.
func (d *Daemon) agSetPollerWakeup(wakeup chan struct{}) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.agDrain.wakeup = wakeup
}

// agStartDrain flips the daemon into drain mode. Idempotent: a second call
// keeps the original start time.
func (d *Daemon) agStartDrain() {
	d.claimMu.Lock()
	if d.agDrain.draining {
		d.claimMu.Unlock()
		return
	}
	d.agDrain.draining = true
	d.agDrain.startedAt = time.Now().UTC()
	d.claimMu.Unlock()
	d.logger.Info("drain: not claiming new tasks; running tasks will finish", "active_tasks", d.activeTasks.Load())
}

// agResume ends drain mode and wakes the poller so queued work is claimed
// immediately. Idempotent.
func (d *Daemon) agResume() {
	d.claimMu.Lock()
	was := d.agDrain.draining
	d.agDrain.draining = false
	d.agDrain.startedAt = time.Time{}
	wakeup := d.agDrain.wakeup
	d.claimMu.Unlock()
	if !was {
		return
	}
	d.agClearDrainNote()
	if wakeup != nil {
		signalPollerWakeup(wakeup)
	}
	d.logger.Info("drain: resumed claiming")
}

func (d *Daemon) agDrainHealth() agDrainHealth {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if !d.agDrain.draining {
		return agDrainHealth{}
	}
	return agDrainHealth{
		Draining:       true,
		DrainStartedAt: d.agDrain.startedAt.Format(time.RFC3339),
		Idle:           d.claimsInFlight == 0 && d.activeTasks.Load() == 0,
	}
}

// agDeferRestartLocked is the restart barriers' drain check (claimMu held).
// It returns true, and records why in reload_pending_reason, when the restart
// must not go ahead.
func (d *Daemon) agDeferRestartLocked() bool {
	if !d.agDrain.draining {
		return false
	}
	d.agAppendDrainNote()
	return true
}

// agDeferServerUpdate reports a server-triggered update as deferred by drain.
func (d *Daemon) agDeferServerUpdate(ctx context.Context, runtimeID string, update *PendingUpdate) {
	d.logger.Info("update deferred: daemon is draining", "runtime_id", runtimeID, "update_id", update.ID)
	d.agAppendDrainNote()
	d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
		"status": "failed",
		"error":  "runtime update deferred because the daemon is draining; resume it, then retry",
	})
}

// agAppendDrainNote adds the drain note to the pending-restart reason, keeping
// any version-change text already there.
func (d *Daemon) agAppendDrainNote() {
	base := strings.TrimSuffix(strings.TrimSuffix(d.reloadPending(), " ("+agDrainDeferredNote+")"), agDrainDeferredNote)
	reason := agDrainDeferredNote
	if base != "" {
		reason = fmt.Sprintf("%s (%s)", base, agDrainDeferredNote)
	}
	d.reloadPendingReason.Store(&reason)
}

// agClearDrainNote removes the drain note on resume, restoring the underlying
// version-change reason if there was one.
func (d *Daemon) agClearDrainNote() {
	current := d.reloadPending()
	if !strings.HasSuffix(current, agDrainDeferredNote) && !strings.HasSuffix(current, agDrainDeferredNote+")") {
		return
	}
	base := strings.TrimSuffix(strings.TrimSuffix(current, " ("+agDrainDeferredNote+")"), agDrainDeferredNote)
	if base == "" {
		d.reloadPendingReason.Store(nil)
		return
	}
	d.reloadPendingReason.Store(&base)
}

// agDrainHandler serves POST /drain (drain=true) and POST /resume (drain=false)
// on the localhost-only health listener. Both return the resulting state.
func (d *Daemon) agDrainHandler(drain bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if drain {
			d.agStartDrain()
		} else {
			d.agResume()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			agDrainHealth
			ActiveTaskCount int64 `json:"active_task_count"`
		}{d.agDrainHealth(), d.activeTasks.Load()})
	}
}

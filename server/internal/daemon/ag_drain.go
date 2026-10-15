package daemon

// AG patch: daemon drain mode. The daemon stops claiming new tasks, lets the
// running ones finish, reports when it is idle, and resumes on request.
//
// Drain is its own flag, not pauseClaims: releaseClaimBarrier and the failure
// branch of tryBeginServerUpdate clear pauseClaims unconditionally and would
// silently end a drain. It is in memory only, so a restart comes back claiming;
// that is why every restart path refuses while it is set (see triggerRestart).

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

// serverUpdateDraining extends the upstream serverUpdateAcquireResult enum
// without editing it.
const serverUpdateDraining serverUpdateAcquireResult = 100

// agDrainState is guarded by Daemon.claimMu, so a drain and a claim decision
// never interleave.
type agDrainState struct {
	draining  bool
	startedAt time.Time
	// wakeup is the batch poller's wakeup channel, so resume can claim at once.
	wakeup chan struct{}
}

// agDrainHealth is embedded in HealthResponse and flattened into /health.
type agDrainHealth struct {
	Draining       bool   `json:"draining"`
	DrainStartedAt string `json:"drain_started_at,omitempty"`
	// Idle: draining with no active tasks (including ones parked on a
	// local-directory lock) and no claim in flight.
	Idle bool `json:"idle"`
}

// agDrainingLocked reports whether the daemon is draining. claimMu must be held.
func (d *Daemon) agDrainingLocked() bool {
	return d.agDrain.draining
}

func (d *Daemon) agDraining() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	return d.agDrain.draining
}

func (d *Daemon) agSetPollerWakeup(wakeup chan struct{}) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	d.agDrain.wakeup = wakeup
}

// agStartDrain is idempotent: a second call keeps the original start time.
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

// agResume ends drain mode and wakes the poller. Idempotent.
func (d *Daemon) agResume() {
	d.claimMu.Lock()
	was := d.agDrain.draining
	d.agDrain.draining = false
	d.agDrain.startedAt = time.Time{}
	wakeup := d.agDrain.wakeup
	if was {
		d.agClearDrainNoteLocked()
	}
	d.claimMu.Unlock()
	if !was {
		return
	}
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

// agDeferRestart is triggerRestart's gate. While draining it records why in
// reload_pending_reason and returns true: the restart must not go ahead, since
// the new process would come back claiming.
func (d *Daemon) agDeferRestart() bool {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	if !d.agDrain.draining {
		return false
	}
	d.agAppendDrainNoteLocked()
	d.logger.Info("restart deferred: daemon is draining")
	return true
}

// agDeferServerUpdate reports a server-triggered update as deferred by drain.
func (d *Daemon) agDeferServerUpdate(ctx context.Context, runtimeID string, update *PendingUpdate) {
	d.logger.Info("update deferred: daemon is draining", "runtime_id", runtimeID, "update_id", update.ID)
	d.reportUpdateResult(ctx, runtimeID, update.ID, map[string]any{
		"status": "failed",
		"error":  "runtime update deferred because the daemon is draining; resume it, then retry",
	})
}

// agUpdateOutput tells the server when an update was installed but its restart
// is being held back by a drain that started mid-update.
func (d *Daemon) agUpdateOutput(output string) string {
	if d.agDraining() {
		return output + " (" + agDrainDeferredNote + ")"
	}
	return output
}

// agStripDrainNote removes the drain note, leaving any version-change reason.
func agStripDrainNote(reason string) string {
	return strings.TrimSuffix(strings.TrimSuffix(reason, " ("+agDrainDeferredNote+")"), agDrainDeferredNote)
}

// The note helpers run under claimMu so a note cannot outlive its drain.
func (d *Daemon) agAppendDrainNoteLocked() {
	reason := agDrainDeferredNote
	if base := agStripDrainNote(d.reloadPending()); base != "" {
		reason = fmt.Sprintf("%s (%s)", base, agDrainDeferredNote)
	}
	d.setReloadPending(reason)
}

func (d *Daemon) agClearDrainNoteLocked() {
	current := d.reloadPending()
	base := agStripDrainNote(current)
	switch {
	case base == current:
	case base == "":
		d.clearReloadPending()
	default:
		d.setReloadPending(base)
	}
}

// agDrainHandler serves POST /drain (drain=true) and POST /resume (drain=false)
// and returns the resulting state.
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

package daemon

// AG patch: daemon drain mode.
//
// Drain is its own flag, not pauseClaims: releaseClaimBarrier and the failure
// branch of tryBeginServerUpdate clear pauseClaims unconditionally and would
// silently end a drain. It is in memory only, so a restart comes back claiming;
// that is why every restart path refuses while it is set (see triggerRestart).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agDrainDeferredNote is appended to reload_pending_reason while a restart is
// being held back by a drain.
const agDrainDeferredNote = "restart deferred: daemon is draining"

const agDrainDeferredSuffix = " (" + agDrainDeferredNote + ")"

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
	// local-directory lock), no claim in flight and no update installing.
	Idle bool `json:"idle"`
}

// agDrainingLocked: claimMu must be held.
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
	if d.agHandoffActive() { // one-way; the handler already refuses
		return
	}
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
	h, _ := d.agDrainSnapshot()
	return h
}

// agDrainSnapshot reads the drain state and the active task count together, so
// a /drain response cannot show idle with a non-zero count.
func (d *Daemon) agDrainSnapshot() (agDrainHealth, int64) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	active := d.activeTasks.Load()
	if !d.agDrain.draining {
		return agDrainHealth{}, active
	}
	return agDrainHealth{
		Draining:       true,
		DrainStartedAt: d.agDrain.startedAt.Format(time.RFC3339),
		Idle:           d.claimsInFlight == 0 && active == 0 && !d.updating.Load(),
	}, active
}

// agDrainReport is attached to every heartbeat. Always non-nil: a
// not-draining report is what clears the stored one.
func (d *Daemon) agDrainReport() *protocol.DaemonDrainReport {
	h, active := d.agDrainSnapshot()
	return &protocol.DaemonDrainReport{Draining: h.Draining, Idle: h.Idle, ActiveTasks: active}
}

// SendHeartbeatWithDrain is SendHeartbeat plus the AG patch drain report, which
// an older server ignores. A nil report is omitted.
func (c *Client) SendHeartbeatWithDrain(ctx context.Context, runtimeID string, drain *protocol.DaemonDrainReport) (*HeartbeatResponse, error) {
	return c.SendHeartbeatWithReports(ctx, runtimeID, drain)
}

// agHeartbeatBody adds the AG patch drain report to SendHeartbeat's body. A nil
// report is omitted.
func agHeartbeatBody(body map[string]any, drain *protocol.DaemonDrainReport) map[string]any {
	if drain != nil {
		body["ag_drain"] = drain
	}
	return body
}

// agDeferRestart is triggerRestart's gate. While draining it records why in
// reload_pending_reason and returns true: the restart must not go ahead.
func (d *Daemon) agDeferRestart() bool {
	if d.agOverlapActive() { // a self-restart would RecoverOrphans the predecessor's tasks
		d.logger.Info("restart deferred: predecessor daemon is still running")
		return true
	}
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
		return output + agDrainDeferredSuffix
	}
	return output
}

func agStripDrainNote(reason string) string {
	return strings.TrimSuffix(strings.TrimSuffix(reason, agDrainDeferredSuffix), agDrainDeferredNote)
}

// The note helpers run under claimMu so a note cannot outlive its drain.
func (d *Daemon) agAppendDrainNoteLocked() {
	reason := agDrainDeferredNote
	if base := agStripDrainNote(d.reloadPending()); base != "" {
		reason = base + agDrainDeferredSuffix
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
		if !drain && d.agRefuseResume(w) {
			return
		}
		if drain {
			d.agStartDrain()
		} else {
			d.agResume()
		}
		h, active := d.agDrainSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			agDrainHealth
			ActiveTaskCount int64 `json:"active_task_count"`
		}{h, active})
	}
}

package daemon

// AG patch: zero-downtime daemon handoff.
//
// Two modes cooperate:
//
//   - handoff, on the OLD daemon (POST /handoff). It implies drain, then hands
//     everything a daemon owns over to a successor: it stops heartbeating and
//     registering (liveness and heartbeat-carried requests are the successor's),
//     releases the profile lock and moves its logging aside (the CLI's hook),
//     skips deregistering on exit, and exits once it has no task left. It keeps
//     reporting for its own tasks and keeps serving /health and /repo/checkout,
//     because MULTICA_DAEMON_PORT is fixed per task. It is one-way: resume is
//     refused.
//   - overlap, on the NEW daemon (AGEnableOverlap before Run). It starts while
//     the predecessor still has tasks. It must not RecoverOrphans (that would
//     fail the predecessor's running tasks), and it watches the predecessor: if
//     the predecessor dies with tasks left, only those task IDs are failed.
//
// Upstream's MULTICA_DAEMON_HANDOFF / self-spawn restart path is a different
// mechanism (binary-update handoff) and is deliberately not touched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

var (
	// agHandoffMinHold keeps an idle handoff daemon alive long enough for the
	// successor to start and read its /health; without it an idle daemon would
	// exit before the successor found a predecessor. Tests shorten it.
	agHandoffMinHold = 30 * time.Second
	// agHandoffExitPoll is how often a handoff daemon re-checks for idle.
	agHandoffExitPoll = time.Second
	// agHandoffClaimSettleTimeout bounds how long /handoff waits for claims in
	// flight; it stays under the CLI's request timeout.
	agHandoffClaimSettleTimeout = 3 * time.Second
	// agOverlapPollInterval is how often the successor checks the predecessor.
	agOverlapPollInterval = 2 * time.Second
	// agOverlapHealthTimeout bounds one /health read of the predecessor.
	agOverlapHealthTimeout = 2 * time.Second
	// agOverlapNoPIDProbeFailures is how many consecutive failed /health reads
	// end overlap on a platform that cannot probe a PID.
	agOverlapNoPIDProbeFailures = 5
)

// AGPIDAlive is agPIDAlive for the CLI: known is false where the platform
// cannot probe a PID.
func AGPIDAlive(pid int) (alive, known bool) { return agPIDAlive(pid) }

// errAGHandoff is returned by registration paths on a handoff daemon.
var errAGHandoff = errors.New("daemon is in handoff mode; runtimes belong to its successor")

// AGPredecessor describes the daemon an overlap start takes over from.
type AGPredecessor struct {
	PID                int
	Port               int
	TaskIDs            []string
	MaxConcurrentTasks int
}

type agHandoffState struct {
	active atomic.Bool // handoff mode (old daemon)

	mu        sync.Mutex
	startedAt time.Time
	onHandoff func() // set by the CLI: switch logging, release the profile lock
	taskIDs   map[string]struct{}

	// overlap (new daemon). overlapping is true from AGEnableOverlap until the
	// predecessor has exited cleanly or its leftovers were failed.
	overlapping atomic.Bool
	pred        AGPredecessor
	predTasks   map[string]struct{}
}

// agHandoffHealth is embedded in HealthResponse and flattened into /health.
type agHandoffHealth struct {
	Handoff bool `json:"handoff"`
	// MaxConcurrentTasks lets an overlap start refuse a mismatched cap.
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
	// ActiveTaskIDs are the tasks this process is running; a successor reads
	// them from the predecessor.
	ActiveTaskIDs []string `json:"active_task_ids"`
	// PredecessorPID is set on an overlap daemon while its predecessor lives.
	PredecessorPID int `json:"predecessor_pid,omitempty"`
}

func (d *Daemon) agHandoffHealth() agHandoffHealth {
	h := agHandoffHealth{
		Handoff:            d.agHandoffActive(),
		MaxConcurrentTasks: d.cfg.MaxConcurrentTasks,
		ActiveTaskIDs:      d.agActiveTaskIDs(),
	}
	if d.agOverlapActive() {
		d.agHandoff.mu.Lock()
		h.PredecessorPID = d.agHandoff.pred.PID
		d.agHandoff.mu.Unlock()
	}
	return h
}

func (d *Daemon) agHandoffActive() bool { return d.agHandoff.active.Load() }

// agOverlapActive reports whether the predecessor may still be running.
func (d *Daemon) agOverlapActive() bool { return d.agHandoff.overlapping.Load() }

// agTrackTask and agUntrackTask keep the set of task IDs this process runs, for
// /health. They bracket the same lifetime as activeTasks.
func (d *Daemon) agTrackTask(id string) {
	d.agHandoff.mu.Lock()
	defer d.agHandoff.mu.Unlock()
	if d.agHandoff.taskIDs == nil {
		d.agHandoff.taskIDs = make(map[string]struct{})
	}
	d.agHandoff.taskIDs[id] = struct{}{}
}

func (d *Daemon) agUntrackTask(id string) {
	d.agHandoff.mu.Lock()
	defer d.agHandoff.mu.Unlock()
	delete(d.agHandoff.taskIDs, id)
}

func (d *Daemon) agActiveTaskIDs() []string {
	d.agHandoff.mu.Lock()
	defer d.agHandoff.mu.Unlock()
	return sortedIDs(d.agHandoff.taskIDs)
}

func sortedIDs(set map[string]struct{}) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// AGSetHandoffHook registers what the CLI does when handoff begins: move
// logging to its own file, then release the profile lock. It runs once, after
// the daemon has stopped claiming and heartbeating.
func (d *Daemon) AGSetHandoffHook(fn func()) {
	d.agHandoff.mu.Lock()
	d.agHandoff.onHandoff = fn
	d.agHandoff.mu.Unlock()
}

// agStartHandoff is idempotent. It implies drain.
func (d *Daemon) agStartHandoff() {
	d.agStartDrain()
	if !d.agHandoff.active.CompareAndSwap(false, true) {
		return
	}
	d.agHandoff.mu.Lock()
	d.agHandoff.startedAt = time.Now()
	hook := d.agHandoff.onHandoff
	d.agHandoff.mu.Unlock()
	d.logger.Info("handoff: no longer heartbeating or registering; exiting once idle", "active_tasks", d.activeTasks.Load())
	if hook != nil {
		hook()
	}
	go d.agHandoffExitLoop()
}

// agHandoffExitLoop cancels the daemon once the drain is idle and the hold has
// elapsed.
func (d *Daemon) agHandoffExitLoop() {
	ctx := d.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(agHandoffExitPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		d.agHandoff.mu.Lock()
		held := time.Since(d.agHandoff.startedAt) >= agHandoffMinHold
		d.agHandoff.mu.Unlock()
		if h, _ := d.agDrainSnapshot(); !held || !h.Idle {
			continue
		}
		d.logger.Info("handoff: idle; exiting")
		if d.cancelFunc != nil {
			d.cancelFunc()
		}
		return
	}
}

// agHandoffHandler serves POST /handoff and returns the resulting state.
func (d *Daemon) agHandoffHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.agOverlapActive() {
			// Its predecessor's leftovers are only watched while this daemon
			// lives; a second handoff would leave them unwatched.
			http.Error(w, fmt.Sprintf("daemon is still overlapping its predecessor (pid %d); retry the handoff once that process has exited", d.agPredecessorPID()), http.StatusConflict)
			return
		}
		d.agStartHandoff()
		d.agWaitClaimsSettled(r.Context())
		h, active := d.agDrainSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			agDrainHealth
			agHandoffHealth
			ActiveTaskCount int64 `json:"active_task_count"`
		}{h, d.agHandoffHealth(), active})
	}
}

// agGCSkipped is true on both sides of a handoff: the successor sees the
// predecessor's task dirs and the handoff daemon the successor's, and neither
// can tell them from orphans.
func (d *Daemon) agGCSkipped() bool { return d.agOverlapActive() || d.agHandoffActive() }

func (d *Daemon) agPredecessorPID() int {
	d.agHandoff.mu.Lock()
	defer d.agHandoff.mu.Unlock()
	return d.agHandoff.pred.PID
}

// agWaitClaimsSettled waits, bounded, for claims that were already in flight
// when drain began to become tracked tasks, so the successor's first read of
// /health lists them.
func (d *Daemon) agWaitClaimsSettled(ctx context.Context) {
	deadline := time.Now().Add(agHandoffClaimSettleTimeout)
	for time.Now().Before(deadline) {
		d.claimMu.Lock()
		n := d.claimsInFlight
		d.claimMu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// agRefuseResume writes the refusal for a resume on a handoff daemon and
// reports whether it did.
func (d *Daemon) agRefuseResume(w http.ResponseWriter) bool {
	if !d.agHandoffActive() {
		return false
	}
	http.Error(w, "daemon is in handoff mode and will exit once idle; handoff cannot be resumed, start a new daemon instead", http.StatusConflict)
	return true
}

// agOrphanScope narrows the runtimes RecoverOrphans is called for: none while
// the predecessor lives, because recovery fails every running task on the
// runtime, including the predecessor's.
func (d *Daemon) agOrphanScope(runtimeIDs []string) []string {
	if d.agOverlapActive() {
		d.logger.Info("overlap: predecessor still running; skipping recover-orphans", "predecessor_pid", d.agPredecessorPID())
		return nil
	}
	return runtimeIDs
}

// AGEnableOverlap puts a not-yet-running daemon into overlap mode. Call before
// Run.
func (d *Daemon) AGEnableOverlap(p AGPredecessor) {
	d.agHandoff.mu.Lock()
	d.agHandoff.pred = p
	d.agHandoff.predTasks = make(map[string]struct{}, len(p.TaskIDs))
	for _, id := range p.TaskIDs {
		d.agHandoff.predTasks[id] = struct{}{}
	}
	d.agHandoff.mu.Unlock()
	d.agHandoff.overlapping.Store(true)
	d.logger.Info("overlap: predecessor daemon still running; not recovering orphans",
		"predecessor_pid", p.PID, "predecessor_port", p.Port, "predecessor_tasks", len(p.TaskIDs))
}

// agPredecessorHealth is the part of the predecessor's /health the successor
// reads.
type agPredecessorHealth struct {
	PID           int      `json:"pid"`
	ActiveTaskIDs []string `json:"active_task_ids"`
}

func agFetchPredecessorHealth(ctx context.Context, port int) (agPredecessorHealth, error) {
	var h agPredecessorHealth
	ctx, cancel := context.WithTimeout(ctx, agOverlapHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", nil)
	if err != nil {
		return h, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("health returned status %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&h)
	return h, err
}

// agOverlapLoop follows the predecessor until it is gone, then ends overlap.
func (d *Daemon) agOverlapLoop(ctx context.Context) {
	if !d.agOverlapActive() {
		return
	}
	ticker := time.NewTicker(agOverlapPollInterval)
	defer ticker.Stop()
	failures := 0
	for {
		if d.agOverlapStep(ctx, &failures) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// agOverlapStep does one poll and reports whether overlap has ended.
func (d *Daemon) agOverlapStep(ctx context.Context, failures *int) bool {
	d.agHandoff.mu.Lock()
	pred := d.agHandoff.pred
	d.agHandoff.mu.Unlock()

	alive, known := agPIDAlive(pred.PID)
	h, err := agFetchPredecessorHealth(ctx, pred.Port)
	healthOK := err == nil && h.PID == pred.PID
	switch {
	case healthOK:
		*failures = 0
		d.agShrinkPredecessorTasks(h.ActiveTaskIDs)
	case ctx.Err() != nil:
		return true
	default:
		*failures++
		d.logger.Debug("overlap: predecessor health unreadable", "predecessor_pid", pred.PID, "error", err)
	}
	if known {
		if alive {
			return false // a transient health failure keeps the last known set
		}
	} else if *failures < agOverlapNoPIDProbeFailures {
		return false
	}

	d.agFinishOverlap(ctx)
	return true
}

// agShrinkPredecessorTasks intersects the tracked set with what the
// predecessor reports: a handoff daemon never claims, so the set only shrinks.
func (d *Daemon) agShrinkPredecessorTasks(reported []string) {
	keep := make(map[string]struct{}, len(reported))
	for _, id := range reported {
		keep[id] = struct{}{}
	}
	d.agHandoff.mu.Lock()
	defer d.agHandoff.mu.Unlock()
	for id := range d.agHandoff.predTasks {
		if _, ok := keep[id]; !ok {
			delete(d.agHandoff.predTasks, id)
		}
	}
}

// agFinishOverlap runs once the predecessor's process is gone: any task it
// still had is failed, then the daemon behaves as a normal one.
func (d *Daemon) agFinishOverlap(ctx context.Context) {
	d.agHandoff.mu.Lock()
	pred := d.agHandoff.pred
	leftovers := sortedIDs(d.agHandoff.predTasks)
	d.agHandoff.mu.Unlock()

	if len(leftovers) == 0 {
		d.logger.Info("overlap: predecessor exited; resuming normal operation", "predecessor_pid", pred.PID)
	} else {
		d.logger.Warn("overlap: predecessor exited with tasks still running; failing them",
			"predecessor_pid", pred.PID, "tasks", len(leftovers))
		msg := fmt.Sprintf("the previous daemon (pid %d) exited mid-handoff before this task finished", pred.PID)
		for _, id := range leftovers {
			// Best effort per task: one that already finished server-side may be
			// rejected, and must not stop the rest.
			if err := d.reportTerminalTask(ctx, terminalTaskReport{
				kind:          terminalTaskReportFail,
				taskID:        id,
				errorMessage:  msg,
				failureReason: taskfailure.ReasonRuntimeOffline.String(),
			}); err != nil && !errors.Is(err, context.Canceled) {
				d.logger.Warn("overlap: failing leftover task failed", "task", id, "error", err)
			}
		}
	}
	d.agHandoff.overlapping.Store(false)
}

// agGCProbeInterval is how often a running heavy GC step re-checks whether
// another daemon has taken a task slot.
var agGCProbeInterval = 500 * time.Millisecond

// agGCStepContext derives the context for one heavy repo-maintenance step. The
// dispatch-time CancelMaintenance reaches only this process, and in overlap
// mode both daemons share one repo cache, so while the step runs the slot
// probe is watched and the step is cancelled with ErrMaintenancePreempted as
// soon as another daemon holds a slot. A probe error counts as busy, as it
// does for the check before each step. release must be called once the step has
// returned and its cause has been read.
func (d *Daemon) agGCStepContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(agGCProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if d.agOtherDaemonBusy() {
					cancel(repocache.ErrMaintenancePreempted)
					return
				}
			}
		}
	}()
	return ctx, func() {
		close(stop)
		cancel(context.Canceled)
	}
}

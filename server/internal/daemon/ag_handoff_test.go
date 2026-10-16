package daemon

// AG patch: daemon handoff and overlap tests (ag_handoff.go); TestAGPatch* with
// "AG patch:" subtests, as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// agHandoffRecorder is a fake multica server that records every request path
// and body. A path in status answers with that code instead of 200.
type agHandoffRecorder struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	bodies map[string]string
	status map[string]int
}

func newAGHandoffRecorder(t *testing.T) *agHandoffRecorder {
	t.Helper()
	r := &agHandoffRecorder{bodies: map[string]string{}, status: map[string]int{}}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.bodies[req.URL.Path] = string(body)
		code := r.status[req.URL.Path]
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"injected"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *agHandoffRecorder) setStatus(path string, code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status[path] = code
}

func (r *agHandoffRecorder) count(suffix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

func (r *agHandoffRecorder) body(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[path]
}

// failed returns the task IDs POSTed to /api/daemon/tasks/<id>/fail, in order.
func (r *agHandoffRecorder) failed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, p := range r.paths {
		if rest, ok := strings.CutPrefix(p, "/api/daemon/tasks/"); ok {
			if id, ok := strings.CutSuffix(rest, "/fail"); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// agFakePredecessor serves the predecessor's /health: pid, active task IDs and
// the cap. down makes it answer 500.
type agFakePredecessor struct {
	*httptest.Server
	mu    sync.Mutex
	pid   int
	tasks []string
	down  bool
}

func newAGFakePredecessor(t *testing.T, pid int, tasks ...string) *agFakePredecessor {
	t.Helper()
	p := &agFakePredecessor{pid: pid, tasks: tasks}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.down {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "running", "pid": p.pid, "handoff": true,
			"max_concurrent_tasks": 2, "active_task_ids": p.tasks,
		})
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *agFakePredecessor) port() int { return p.Listener.Addr().(*net.TCPAddr).Port }

func (p *agFakePredecessor) set(pid int, down bool, tasks ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pid, p.down, p.tasks = pid, down, tasks
}

// agDeadPID returns the PID of a process that has already exited and been reaped.
func agDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}
	return cmd.Process.Pid
}

// The handoff loops read these package-level timings from goroutines that can
// outlive the test that started them, so they are set once here and never
// written again.
func init() {
	agHandoffMinHold = 300 * time.Millisecond
	agHandoffExitPoll = 10 * time.Millisecond
	agOverlapPollInterval = 20 * time.Millisecond
}

// agHandoffDaemon is a daemon with one workspace and runtime, talking to srv.
func agHandoffDaemon(t *testing.T, srvURL string) *Daemon {
	t.Helper()
	d := freshDaemon(srvURL)
	// agStartHandoff's exit loop runs until this context ends.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.rootCtx = ctx
	d.cfg.MaxConcurrentTasks = 3
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1"}
	return d
}

// agCountRecoverOrphans routes d's client through a proxy to upstream that
// counts recover-orphans calls.
func agCountRecoverOrphans(t *testing.T, d *Daemon, upstream string) *atomic.Int64 {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/recover-orphans") {
			n.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d.client = NewClient(srv.URL)
	return &n
}

func TestAGPatchHandoffStopsClaimingAndHeartbeating(t *testing.T) {
	t.Run("AG patch: handoff implies drain and runs the hook once", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		var hooks atomic.Int32
		d.AGSetHandoffHook(func() { hooks.Add(1) })

		d.agStartHandoff()
		d.agStartHandoff()

		if !d.agHandoffActive() || !d.agDraining() {
			t.Fatalf("handoff=%v draining=%v, want both true", d.agHandoffActive(), d.agDraining())
		}
		if got := hooks.Load(); got != 1 {
			t.Fatalf("handoff hook ran %d times, want 1", got)
		}
	})

	t.Run("AG patch: a handoff daemon sends no heartbeat and a normal one does", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		d := agHandoffDaemon(t, rec.URL)
		d.runHeartbeatTick(context.Background(), "rt-1")
		if got := rec.count("/api/daemon/heartbeat"); got != 1 {
			t.Fatalf("control: heartbeats = %d, want 1", got)
		}

		d.agHandoff.active.Store(true)
		d.runHeartbeatTick(context.Background(), "rt-1")
		d.sendWSHeartbeats(context.Background(), []string{"rt-1"}, make(chan *wsOutbound, 4))
		if got := rec.count("/api/daemon/heartbeat"); got != 1 {
			t.Fatalf("heartbeats after handoff = %d, want still 1", got)
		}
	})

	t.Run("AG patch: a handoff daemon does not register, sync workspaces or react to runtime_gone", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		d := agHandoffDaemon(t, rec.URL)
		d.agHandoff.active.Store(true)

		if _, err := d.registerBuiltinRuntimesForWorkspaceLocked(context.Background(), "ws-1", []map[string]string{{"type": "claude"}}); err == nil {
			t.Error("registerBuiltinRuntimesForWorkspaceLocked succeeded on a handoff daemon")
		}
		if _, _, err := d.registerRuntimesForWorkspaceBatchLocked(context.Background(), "ws-1", []map[string]string{{"type": "claude"}}); err == nil {
			t.Error("registerRuntimesForWorkspaceBatchLocked succeeded on a handoff daemon")
		}
		if err := d.syncWorkspacesFromAPI(context.Background(), true); err != nil {
			t.Errorf("syncWorkspacesFromAPI = %v, want a quiet no-op", err)
		}
		d.handleRuntimeGone("rt-1")
		d.handlePendingWorkHint("rt-1", "update")

		rec.mu.Lock()
		defer rec.mu.Unlock()
		if len(rec.paths) != 0 {
			t.Errorf("handoff daemon called the server: %v", rec.paths)
		}
		if _, ok := d.runtimeIndex["rt-1"]; !ok {
			t.Error("runtime_gone pruned the successor's runtime from the handoff daemon")
		}
	})

	t.Run("AG patch: a handoff daemon claims nothing", func(t *testing.T) {
		s := newAGDrainClaimServer(t, false)
		d := newAGDrainPollDaemon(t, s, 10*time.Millisecond)
		agDrainCompletingRunner(d)
		d.agHandoff.active.Store(true)
		d.agStartDrain()
		startAGDrainPollLoop(t, d, nil)

		time.Sleep(200 * time.Millisecond)
		if got := s.claims.Load(); got != 0 {
			t.Fatalf("handoff daemon claimed %d times", got)
		}
	})
}

func TestAGPatchHandoffNeverDeregisters(t *testing.T) {
	t.Run("AG patch: a normal daemon deregisters its runtimes on exit", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		d := agHandoffDaemon(t, rec.URL)
		d.deregisterRuntimes()
		if got := rec.count("/api/daemon/deregister"); got != 1 {
			t.Fatalf("control: deregister calls = %d, want 1", got)
		}
	})

	t.Run("AG patch: a handoff daemon leaves its runtimes registered for the successor", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		d := agHandoffDaemon(t, rec.URL)
		d.agStartHandoff()
		d.deregisterRuntimes()
		if got := rec.count("/api/daemon/deregister"); got != 0 {
			t.Fatalf("handoff daemon deregistered %d times", got)
		}
	})
}

func TestAGPatchHandoffExitsWhenIdle(t *testing.T) {
	t.Run("AG patch: waits for active tasks and the minimum hold before exiting", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		exited := make(chan time.Time, 1)
		d.cancelFunc = func() { exited <- time.Now() }
		d.activeTasks.Add(1)

		start := time.Now()
		d.agStartHandoff()
		select {
		case <-exited:
			t.Fatal("handoff daemon exited with a task still running")
		case <-time.After(400 * time.Millisecond):
		}
		d.activeTasks.Add(-1)
		select {
		case at := <-exited:
			if at.Sub(start) < agHandoffMinHold {
				t.Fatalf("exited after %s, before the %s hold", at.Sub(start), agHandoffMinHold)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("handoff daemon did not exit once idle")
		}
	})

	t.Run("AG patch: an idle handoff daemon stays up through the hold so the successor can read it", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		exited := make(chan struct{}, 1)
		d.cancelFunc = func() { exited <- struct{}{} }
		d.agStartHandoff()
		select {
		case <-exited:
			t.Fatal("an idle handoff daemon exited inside the hold")
		case <-time.After(150 * time.Millisecond):
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Fatal("handoff daemon did not exit after the hold")
		}
	})
}

func TestAGPatchHandoffResumeRefused(t *testing.T) {
	d := agHandoffDaemon(t, "")
	d.agStartHandoff()

	rec := httptest.NewRecorder()
	d.agDrainHandler(false).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resume", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /resume on a handoff daemon = %d, want 409", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "handoff") || !strings.Contains(body, "start a new daemon") {
		t.Fatalf("refusal %q does not explain what to do", body)
	}
	d.agResume()
	if !d.agDraining() || !d.agHandoffActive() {
		t.Fatal("resume undid a handoff")
	}

	// Drain still works on a handoff daemon (idempotent) and a normal daemon can resume.
	n := agHandoffDaemon(t, "")
	n.agStartDrain()
	rec = httptest.NewRecorder()
	n.agDrainHandler(false).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resume", nil))
	if rec.Code != http.StatusOK || n.agDraining() {
		t.Fatalf("resume on a draining, non-handoff daemon = %d draining=%v, want 200/false", rec.Code, n.agDraining())
	}
}

func TestAGPatchHandoffEndpointAndHealth(t *testing.T) {
	t.Run("AG patch: POST /handoff starts handoff and reports the state; GET is refused", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		d.activeTasks.Store(1)
		d.agTrackTask("t1")

		rec := httptest.NewRecorder()
		d.agHandoffHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/handoff", nil))
		if rec.Code != http.StatusMethodNotAllowed || d.agHandoffActive() {
			t.Fatalf("GET /handoff = %d handoff=%v, want 405/false", rec.Code, d.agHandoffActive())
		}

		rec = httptest.NewRecorder()
		d.agHandoffHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/handoff", nil))
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if rec.Code != http.StatusOK || got["handoff"] != true || got["draining"] != true || got["idle"] != false || got["active_task_count"] != float64(1) {
			t.Fatalf("POST /handoff = %d %v", rec.Code, got)
		}
	})

	t.Run("AG patch: /health reports the cap, the active task IDs and handoff", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		raw := agDrainHealthJSON(t, d)
		if raw["max_concurrent_tasks"] != float64(3) {
			t.Errorf("max_concurrent_tasks = %v, want 3", raw["max_concurrent_tasks"])
		}
		if ids, ok := raw["active_task_ids"].([]any); !ok || len(ids) != 0 {
			t.Errorf("active_task_ids = %#v, want an empty list (not null)", raw["active_task_ids"])
		}
		if raw["handoff"] != false {
			t.Errorf("handoff = %v, want false", raw["handoff"])
		}
		if _, present := raw["predecessor_pid"]; present {
			t.Error("predecessor_pid reported on a daemon without a predecessor")
		}

		d.agTrackTask("t2")
		d.agTrackTask("t1")
		d.agUntrackTask("t2")
		d.agTrackTask("t3")
		d.agStartHandoff()
		raw = agDrainHealthJSON(t, d)
		ids, _ := raw["active_task_ids"].([]any)
		if len(ids) != 2 || ids[0] != "t1" || ids[1] != "t3" {
			t.Errorf("active_task_ids = %v, want [t1 t3]", ids)
		}
		if raw["handoff"] != true {
			t.Errorf("handoff = %v after handoff, want true", raw["handoff"])
		}
	})

	t.Run("AG patch: an overlap daemon reports its predecessor's PID until overlap ends", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		d.AGEnableOverlap(AGPredecessor{PID: 4242, Port: 1, MaxConcurrentTasks: 3})
		if raw := agDrainHealthJSON(t, d); raw["predecessor_pid"] != float64(4242) {
			t.Errorf("predecessor_pid = %v, want 4242", raw["predecessor_pid"])
		}
		d.agHandoff.overlapping.Store(false)
		if raw := agDrainHealthJSON(t, d); raw["predecessor_pid"] != nil {
			t.Errorf("predecessor_pid = %v after overlap ended, want omitted", raw["predecessor_pid"])
		}
	})
}

func TestAGPatchHandoffDaemonFinishesItsOwnTasks(t *testing.T) {
	s := newAGDrainClaimServer(t, false)
	d := newAGDrainPollDaemon(t, s, 10*time.Millisecond)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
		started <- struct{}{}
		<-release
		return TaskResult{Status: "completed"}, nil
	})
	exited := make(chan struct{}, 1)
	d.cancelFunc = func() { exited <- struct{}{} }
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	t.Cleanup(cancelRoot)
	d.rootCtx = rootCtx
	startAGDrainPollLoop(t, d, nil)

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	d.agStartHandoff()

	if ids, _ := agDrainHealthJSON(t, d)["active_task_ids"].([]any); len(ids) != 1 || ids[0] != "t1" {
		t.Fatalf("active_task_ids while running = %v, want [t1]", ids)
	}
	select {
	case <-exited:
		t.Fatal("handoff daemon exited with its task still running")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	waitFor(t, func() bool { return s.completed.Load() == 1 }, "the handoff daemon to report its task complete")
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("handoff daemon did not exit after its last task")
	}
	if ids, _ := agDrainHealthJSON(t, d)["active_task_ids"].([]any); len(ids) != 0 {
		t.Fatalf("active_task_ids after completion = %v, want none", ids)
	}
	if got := s.claims.Load(); got > 1 {
		// The first claim returned t1; nothing may be claimed once handoff began.
		before := got
		time.Sleep(100 * time.Millisecond)
		if after := s.claims.Load(); after != before {
			t.Fatalf("claims grew from %d to %d after handoff", before, after)
		}
	}
}

func TestAGPatchOverlapNeverRecoversOrphans(t *testing.T) {
	pred := AGPredecessor{PID: os.Getpid(), Port: 1, TaskIDs: []string{"t1"}, MaxConcurrentTasks: 2}

	syncOnce := func(t *testing.T, overlap bool) int64 {
		fx := newBatchFixture(t)
		d := fx.daemon
		d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
		fx.setWorkspaces(WorkspaceInfo{ID: "ws-1", Name: "one"})
		stubAgentProbe(t, d.cfg.Agents)
		n := agCountRecoverOrphans(t, d, fx.server.URL)
		if overlap {
			d.AGEnableOverlap(pred)
		}
		if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
			t.Fatalf("syncWorkspacesFromAPI: %v", err)
		}
		if fx.registerCallCount() == 0 {
			t.Fatal("the daemon registered nothing, so the test proves nothing")
		}
		return n.Load()
	}

	t.Run("AG patch: startup sync recovers orphans on a normal daemon", func(t *testing.T) {
		if got := syncOnce(t, false); got == 0 {
			t.Fatal("control: a normal daemon never called recover-orphans, so the next case proves nothing")
		}
	})

	t.Run("AG patch: startup sync does not call recover-orphans in overlap mode", func(t *testing.T) {
		if got := syncOnce(t, true); got != 0 {
			t.Fatalf("overlap daemon called recover-orphans %d times, which fails the predecessor's tasks", got)
		}
	})

	reregister := func(t *testing.T, overlap bool) int64 {
		fx := newHandleRuntimeGoneFixture(t)
		d := fx.daemon
		d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-old"}}
		d.runtimeIndex["rt-old"] = Runtime{ID: "rt-old"}
		n := agCountRecoverOrphans(t, d, fx.server.URL)
		if overlap {
			d.AGEnableOverlap(pred)
		}
		d.handleRuntimeGone("rt-old")
		if fx.registerCount.Load() != 1 {
			t.Fatalf("re-register calls = %d, want 1", fx.registerCount.Load())
		}
		return n.Load()
	}

	t.Run("AG patch: runtime_gone re-register recovers orphans on a normal daemon", func(t *testing.T) {
		if got := reregister(t, false); got != 1 {
			t.Fatalf("control: recover-orphans calls = %d, want 1", got)
		}
	})

	t.Run("AG patch: runtime_gone re-register does not call recover-orphans in overlap mode", func(t *testing.T) {
		if got := reregister(t, true); got != 0 {
			t.Fatalf("overlap daemon called recover-orphans %d times after runtime_gone", got)
		}
	})

	t.Run("AG patch: recovery returns once overlap has ended", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		ids := []string{"rt-1", "rt-2"}
		if got := d.agOrphanScope(ids); !slices.Equal(got, ids) {
			t.Fatalf("scope without overlap = %v, want %v", got, ids)
		}
		d.AGEnableOverlap(pred)
		if got := d.agOrphanScope(ids); len(got) != 0 {
			t.Fatalf("scope in overlap = %v, want none", got)
		}
		d.agHandoff.overlapping.Store(false)
		if got := d.agOrphanScope(ids); !slices.Equal(got, ids) {
			t.Fatalf("scope after overlap = %v, want %v", got, ids)
		}
	})

	t.Run("AG patch: an overlap daemon does not garbage-collect the predecessor's task dirs or self-restart", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		d.cfg.WorkspacesRoot = filepath.Join(t.TempDir(), "missing")
		d.AGEnableOverlap(pred)
		if !d.agDeferRestart() {
			t.Error("a self-restart was not deferred while the predecessor lives; its RecoverOrphans would fail the predecessor's tasks")
		}
		d.agHandoff.overlapping.Store(false)
		if d.agDeferRestart() {
			t.Error("restart still deferred after overlap ended")
		}
	})
}

func TestAGPatchOverlapFollowsPredecessor(t *testing.T) {
	step := func(d *Daemon) (ended bool, failures int) {
		ended = d.agOverlapStep(context.Background(), &failures)
		return
	}

	t.Run("AG patch: the tracked task set only shrinks as the predecessor finishes tasks", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		pred := newAGFakePredecessor(t, os.Getpid(), "a", "b", "c")
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: pred.port(), TaskIDs: []string{"a", "b", "c"}, MaxConcurrentTasks: 2})

		pred.set(os.Getpid(), false, "a", "c")
		if ended, _ := step(d); ended {
			t.Fatal("overlap ended while the predecessor is alive")
		}
		// A task ID the predecessor never listed at startup must not appear.
		pred.set(os.Getpid(), false, "a", "z")
		step(d)

		d.agHandoff.mu.Lock()
		got := sortedIDs(d.agHandoff.predTasks)
		d.agHandoff.mu.Unlock()
		if !slices.Equal(got, []string{"a"}) {
			t.Fatalf("tracked tasks = %v, want [a]", got)
		}
		if len(rec.failed()) != 0 {
			t.Fatalf("failed tasks %v while the predecessor is alive", rec.failed())
		}
	})

	t.Run("AG patch: a transient health failure with a live PID keeps the last task set and fails nothing", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		pred := newAGFakePredecessor(t, os.Getpid(), "a", "b")
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: pred.port(), TaskIDs: []string{"a", "b"}, MaxConcurrentTasks: 2})

		pred.set(os.Getpid(), true) // /health answers 500
		failures := 0
		for i := 0; i < 3; i++ {
			if d.agOverlapStep(context.Background(), &failures) {
				t.Fatal("overlap ended on a health failure with a live PID")
			}
		}
		// A different PID answering on the port is not the predecessor either.
		pred.set(os.Getpid()+1, false)
		if d.agOverlapStep(context.Background(), &failures) {
			t.Fatal("overlap ended because another process answered on the port")
		}

		d.agHandoff.mu.Lock()
		got := sortedIDs(d.agHandoff.predTasks)
		d.agHandoff.mu.Unlock()
		if !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("tracked tasks = %v, want the last known [a b]", got)
		}
		if !d.agOverlapActive() || len(rec.failed()) != 0 {
			t.Fatalf("overlap=%v failed=%v, want overlap still on and nothing failed", d.agOverlapActive(), rec.failed())
		}
	})

	t.Run("AG patch: a predecessor that dies with tasks left has only those tasks failed", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		dead := agDeadPID(t)
		pred := newAGFakePredecessor(t, dead, "a")
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: dead, Port: pred.port(), TaskIDs: []string{"a", "b"}, MaxConcurrentTasks: 2})

		// The last health read shows only "a" left; "b" finished earlier.
		ended, _ := step(d)
		if !ended {
			t.Fatal("overlap did not end after the predecessor's PID died")
		}
		if got := rec.failed(); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("failed tasks = %v, want only [a]", got)
		}
		body := rec.body("/api/daemon/tasks/a/fail")
		if !strings.Contains(body, "exited mid-handoff") || !strings.Contains(body, "failure_reason") {
			t.Fatalf("fail body %q should say the predecessor exited mid-handoff and carry a failure reason", body)
		}
		if d.agOverlapActive() {
			t.Fatal("still in overlap mode after the leftovers were failed")
		}
	})

	t.Run("AG patch: a predecessor that exits cleanly fails nothing and ends overlap", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		dead := agDeadPID(t)
		pred := newAGFakePredecessor(t, os.Getpid()) // reports no tasks
		pred.Close()                                 // and is gone
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: dead, Port: pred.port(), TaskIDs: []string{"a"}, MaxConcurrentTasks: 2})
		// It finished "a" before it exited: the last health read said so.
		d.agShrinkPredecessorTasks(nil)

		if ended, _ := step(d); !ended {
			t.Fatal("overlap did not end")
		}
		if len(rec.failed()) != 0 {
			t.Fatalf("failed %v after a clean exit", rec.failed())
		}
		if d.agOverlapActive() {
			t.Fatal("still in overlap mode")
		}
		ids := []string{"rt-1"}
		if got := d.agOrphanScope(ids); !slices.Equal(got, ids) {
			t.Fatalf("recover-orphans scope after overlap = %v, want normal behaviour", got)
		}
	})

	t.Run("AG patch: a task the server already finished does not stop the rest being failed", func(t *testing.T) {
		rec := newAGHandoffRecorder(t)
		rec.setStatus("/api/daemon/tasks/a/fail", http.StatusConflict)
		dead := agDeadPID(t)
		pred := newAGFakePredecessor(t, dead, "a", "b", "c")
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: dead, Port: pred.port(), TaskIDs: []string{"a", "b", "c"}, MaxConcurrentTasks: 2})

		if ended, _ := step(d); !ended {
			t.Fatal("overlap did not end")
		}
		if got := rec.failed(); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Fatalf("fail calls = %v, want all of [a b c] attempted", got)
		}
		if d.agOverlapActive() {
			t.Fatal("still in overlap mode after a failed fail call")
		}
	})

	t.Run("AG patch: the poll loop notices a predecessor process being killed", func(t *testing.T) {
		sleep, err := exec.LookPath("sleep")
		if err != nil || runtime.GOOS == "windows" {
			t.Skip("needs a sleep binary and PID probing")
		}
		proc := exec.Command(sleep, "60")
		if err := proc.Start(); err != nil {
			t.Fatal(err)
		}
		reaped := make(chan struct{})
		go func() { _ = proc.Wait(); close(reaped) }()
		t.Cleanup(func() { _ = proc.Process.Kill(); <-reaped })

		rec := newAGHandoffRecorder(t)
		pred := newAGFakePredecessor(t, proc.Process.Pid, "a", "b")
		d := agHandoffDaemon(t, rec.URL)
		d.AGEnableOverlap(AGPredecessor{PID: proc.Process.Pid, Port: pred.port(), TaskIDs: []string{"a", "b"}, MaxConcurrentTasks: 2})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { d.agOverlapLoop(ctx); close(done) }()

		// Alive: it finishes "b" and keeps overlapping.
		pred.set(proc.Process.Pid, false, "a")
		time.Sleep(150 * time.Millisecond)
		if !d.agOverlapActive() || len(rec.failed()) != 0 {
			t.Fatalf("overlap=%v failed=%v while the predecessor is alive", d.agOverlapActive(), rec.failed())
		}

		_ = proc.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("overlap loop did not end after the predecessor died")
		}
		if got := rec.failed(); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("failed tasks = %v, want [a]", got)
		}
		if d.agOverlapActive() {
			t.Fatal("still overlapping after the predecessor died")
		}
	})

	t.Run("AG patch: the loop is a no-op outside overlap mode", func(t *testing.T) {
		d := agHandoffDaemon(t, "")
		done := make(chan struct{})
		go func() { d.agOverlapLoop(context.Background()); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("agOverlapLoop blocked on a daemon that is not overlapping")
		}
	})
}

func TestAGPatchOverlapSharesTheTaskCap(t *testing.T) {
	skipWithoutFlock(t)
	const total = 2
	dir := filepath.Join(t.TempDir(), "slots")

	// The predecessor (a handoff daemon) is running one task: it holds slot 0.
	predSlots := newTaskSlotSemaphore(dir, total, nil)
	if _, ok, err := predSlots.tryAcquire(); !ok || err != nil {
		t.Fatalf("predecessor slot: ok=%v err=%v", ok, err)
	}

	var (
		mu      sync.Mutex
		queue   = []string{"t1", "t2", "t3", "t4"}
		done    atomic.Int64
		running atomic.Int64
		maxSeen atomic.Int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim"):
			mu.Lock()
			defer mu.Unlock()
			if len(queue) == 0 {
				_, _ = w.Write([]byte(`{"tasks":[]}`))
				return
			}
			id := queue[0]
			queue = queue[1:]
			_, _ = w.Write([]byte(`{"tasks":[{"id":"` + id + `","runtime_id":"rt-1","issue_id":"i-` + id + `","agent":{"name":"a"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/complete"):
			done.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	d := New(Config{ServerBaseURL: srv.URL, HeartbeatInterval: time.Hour, PollInterval: 10 * time.Millisecond, MaxConcurrentTasks: total}, discardLogger())
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1"}
	d.cancelPollInterval = time.Hour
	d.agSlotDir = dir
	d.AGEnableOverlap(AGPredecessor{PID: os.Getpid(), Port: 1, TaskIDs: []string{"p1"}, MaxConcurrentTasks: total})
	d.runner = taskRunnerFunc(func(ctx context.Context, task Task, provider string, slot int, log *slog.Logger) (TaskResult, error) {
		now := running.Add(1)
		for {
			prev := maxSeen.Load()
			if now <= prev || maxSeen.CompareAndSwap(prev, now) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		running.Add(-1)
		return TaskResult{Status: "completed"}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); d.pollLoop(ctx, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("pollLoop did not stop")
		}
	})

	waitFor(t, func() bool { return done.Load() == 4 }, "all four tasks to complete")
	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("overlap daemon ran %d tasks at once while the predecessor held 1 of %d slots, want 1", got, total)
	}
	runtime.KeepAlive(predSlots) // an unreachable semaphore's files are closed by their finalizers, dropping the locks
}

func TestAGPatchGCStepPreemptedByOtherDaemon(t *testing.T) {
	skipWithoutFlock(t)
	orig := agGCProbeInterval
	agGCProbeInterval = 20 * time.Millisecond
	t.Cleanup(func() { agGCProbeInterval = orig })

	newDaemon := func(t *testing.T, dir string) *Daemon {
		d := agHandoffDaemon(t, "")
		d.cfg.MaxConcurrentTasks = 2
		d.agSlotDir = dir
		return d
	}

	t.Run("AG patch: a heavy step is cancelled as soon as another daemon takes a slot", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "slots")
		d := newDaemon(t, dir)
		ctx, release := d.agGCStepContext(context.Background())
		defer release()
		select {
		case <-ctx.Done():
			t.Fatal("step cancelled while no other daemon holds a slot")
		case <-time.After(100 * time.Millisecond):
		}

		startAGSlotHelper(t, dir, 2, 1) // the successor's task starts
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("step was not cancelled when another daemon took a slot")
		}
		if cause := context.Cause(ctx); cause != repocache.ErrMaintenancePreempted {
			t.Fatalf("cause = %v, want ErrMaintenancePreempted", cause)
		}
	})

	t.Run("AG patch: the daemon's own slots do not preempt its step", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "slots")
		d := newDaemon(t, dir)
		sem := newTaskSlotSemaphore(dir, 2, nil)
		d.agSlots.Store(sem)
		if _, ok, err := sem.tryAcquire(); !ok || err != nil {
			t.Fatalf("own slot: ok=%v err=%v", ok, err)
		}
		ctx, release := d.agGCStepContext(context.Background())
		defer release()
		select {
		case <-ctx.Done():
			t.Fatalf("own slot preempted the step: %v", context.Cause(ctx))
		case <-time.After(150 * time.Millisecond):
		}
	})

	t.Run("AG patch: a failing slot probe cancels the step (fail closed)", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "slots")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		d := newDaemon(t, blocker)
		ctx, release := d.agGCStepContext(context.Background())
		defer release()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("a probe error did not cancel the step")
		}
		if cause := context.Cause(ctx); cause != repocache.ErrMaintenancePreempted {
			t.Fatalf("cause = %v, want ErrMaintenancePreempted", cause)
		}
	})

	t.Run("AG patch: release and a cancelled parent stop the watcher without preempting", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "slots")
		d := newDaemon(t, dir)

		ctx, release := d.agGCStepContext(context.Background())
		release()
		if cause := context.Cause(ctx); cause != context.Canceled {
			t.Fatalf("cause after release = %v, want context.Canceled", cause)
		}

		parent, cancelParent := context.WithCancel(context.Background())
		child, release2 := d.agGCStepContext(parent)
		defer release2()
		cancelParent()
		select {
		case <-child.Done():
		case <-time.After(time.Second):
			t.Fatal("step outlived its parent")
		}
		if cause := context.Cause(child); cause == repocache.ErrMaintenancePreempted {
			t.Fatal("a parent cancellation was reported as a preemption")
		}
	})

	t.Run("AG patch: a running git gc is killed and left pending when another daemon takes a slot", func(t *testing.T) {
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skip("git not available")
		}
		bin := t.TempDir()
		started := filepath.Join(bin, "gc-started")
		script := "#!/bin/sh\ncase \"$*\" in\n  *\" gc \"*) : > '" + started + "'; exec sleep 60 ;;\nesac\nexec '" + realGit + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		d := newGCTestDaemon(t, http.NewServeMux())
		d.agSlotDir = filepath.Join(t.TempDir(), "slots")
		d.cfg.MaxConcurrentTasks = 2

		sourceRepo := createGCGitRepo(t)
		barePath := filepath.Join(t.TempDir(), "cache.git")
		runGitForGC(t, "", "clone", "--bare", sourceRepo, barePath)
		runGitForGC(t, "", "-C", barePath, "branch", "agent/stale/87654321", "HEAD")

		finished := make(chan struct{})
		begin := time.Now()
		go func() {
			defer close(finished)
			d.maintainRepoCache(context.Background(), barePath, &gcStats{})
		}()
		waitFor(t, func() bool { _, err := os.Stat(started); return err == nil }, "git gc to start")
		startAGSlotHelper(t, d.agSlotDir, 2, 1)
		select {
		case <-finished:
		case <-time.After(20 * time.Second):
			t.Fatal("maintenance kept running after another daemon took a slot")
		}
		if elapsed := time.Since(begin); elapsed > 30*time.Second {
			t.Fatalf("maintenance took %s", elapsed)
		}
		if _, err := os.Stat(filepath.Join(barePath, repoMaintenanceMarker)); err != nil {
			t.Fatalf("preempted maintenance must stay pending: %v", err)
		}
	})
}

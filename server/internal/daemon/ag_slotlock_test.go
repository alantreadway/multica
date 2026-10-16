package daemon

// AG patch: machine-wide task slot cap tests; TestAGPatch* as counted by
// ag-patches/test.sh.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	agSlotHelperDirEnv   = "AG_SLOT_HELPER_DIR"
	agSlotHelperTotalEnv = "AG_SLOT_HELPER_TOTAL"
	agSlotHelperHoldEnv  = "AG_SLOT_HELPER_HOLD"
	agSlotHelperPrefix   = "AGSLOTS "
)

func skipWithoutFlock(t *testing.T) {
	t.Helper()
	if !agSlotFlockSupported {
		t.Skip("flock not available on this platform")
	}
}

// TestAGPatchSlotHelperProcess is the body of the helper process the two-process
// tests re-execute the test binary into. In a normal run it does nothing.
func TestAGPatchSlotHelperProcess(t *testing.T) {
	dir := os.Getenv(agSlotHelperDirEnv)
	if dir == "" {
		t.Skip("helper process body")
	}
	total, _ := strconv.Atoi(os.Getenv(agSlotHelperTotalEnv))
	hold, _ := strconv.Atoi(os.Getenv(agSlotHelperHoldEnv))
	sem := newTaskSlotSemaphore(dir, total, nil)
	slots, err := sem.tryAcquireN(hold)
	if err != nil {
		fmt.Fprintf(os.Stdout, "%serror %v\n", agSlotHelperPrefix, err)
		return
	}
	parts := make([]string, len(slots))
	for i, s := range slots {
		parts[i] = strconv.Itoa(s)
	}
	fmt.Fprintf(os.Stdout, "%sheld %s\n", agSlotHelperPrefix, strings.Join(parts, ","))
	_, _ = io.Copy(io.Discard, os.Stdin) // hold until the parent closes stdin or exits
	// The flocks live in sem's open files; keep it reachable so the GC can't
	// finalise them and drop the slots early.
	runtime.KeepAlive(sem)
}

type agSlotHelper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	slots []int
	once  sync.Once
}

// startAGSlotHelper re-executes the test binary as a separate process that
// takes hold slots out of total in dir and keeps them until stop.
func startAGSlotHelper(t *testing.T, dir string, total, hold int) *agSlotHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAGPatchSlotHelperProcess$")
	cmd.Env = append(os.Environ(),
		agSlotHelperDirEnv+"="+dir,
		agSlotHelperTotalEnv+"="+strconv.Itoa(total),
		agSlotHelperHoldEnv+"="+strconv.Itoa(hold),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	h := &agSlotHelper{cmd: cmd, stdin: stdin}
	t.Cleanup(func() { h.stop() })

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, agSlotHelperPrefix) {
			continue
		}
		msg := strings.TrimPrefix(line, agSlotHelperPrefix)
		if !strings.HasPrefix(msg, "held ") {
			t.Fatalf("helper failed: %s (stderr: %s)", msg, stderr.String())
		}
		for _, p := range strings.Split(strings.TrimPrefix(msg, "held "), ",") {
			if p == "" {
				continue
			}
			n, _ := strconv.Atoi(p)
			h.slots = append(h.slots, n)
		}
		go io.Copy(io.Discard, stdout) // keep the pipe drained
		return h
	}
	t.Fatalf("helper exited without reporting slots (stderr: %s)", stderr.String())
	return nil
}

// stop makes the helper exit, which is what releases its flocks. Safe to call twice.
func (h *agSlotHelper) stop() {
	h.once.Do(func() {
		h.stdin.Close()
		_ = h.cmd.Wait()
	})
}

func acquireAll(t *testing.T, sem *agTaskSlots) []int {
	t.Helper()
	slots, err := sem.tryAcquireN(sem.n)
	if err != nil {
		t.Fatalf("tryAcquireN: %v", err)
	}
	return slots
}

func TestAGPatchSlotCapAcrossProcesses(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	const total = 4

	helper := startAGSlotHelper(t, dir, total, 3)
	if len(helper.slots) != 3 {
		t.Fatalf("helper took %v, want 3 slots", helper.slots)
	}

	sem := newTaskSlotSemaphore(dir, total, nil)
	got := acquireAll(t, sem)
	if len(got) != 1 {
		t.Fatalf("with the helper holding 3 of %d, this process got %v; want exactly 1", total, got)
	}
	if len(helper.slots)+len(got) != total {
		t.Fatalf("total running slots = %d, want the cap %d", len(helper.slots)+len(got), total)
	}
	seen := map[int]bool{}
	for _, s := range append(append([]int{}, helper.slots...), got...) {
		if seen[s] {
			t.Fatalf("slot %d handed to two processes (helper %v, ours %v)", s, helper.slots, got)
		}
		seen[s] = true
	}
	if _, ok, err := sem.tryAcquire(); ok || err != nil {
		t.Fatalf("all slots are taken, but tryAcquire ok=%v err=%v", ok, err)
	}

	// The helper exiting frees its slots without any cleanup of its own.
	helper.stop()
	more := acquireAll(t, sem)
	if len(more) != 3 {
		t.Fatalf("after the helper exited got %v, want its 3 slots", more)
	}
	all := append(append([]int{}, got...), more...)
	sort.Ints(all)
	for i, s := range all {
		if s != i {
			t.Fatalf("slots %v are not exactly [0,%d)", all, total)
		}
	}
}

func TestAGPatchWaitForTaskSlotSeesSlotFreedByOtherProcess(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	helper := startAGSlotHelper(t, dir, 1, 1)

	sem := newTaskSlotSemaphore(dir, 1, nil)
	sem.retry = 10 * time.Millisecond

	// Held elsewhere: waiting times out without a slot.
	slot, acquired, woke, err := waitForTaskSlot(context.Background(), sem, nil, 50*time.Millisecond)
	if err != nil || acquired || woke {
		t.Fatalf("while held elsewhere: slot=%d acquired=%v woke=%v err=%v", slot, acquired, woke, err)
	}

	// The other process lets go partway through the wait; no wakeup arrives, so
	// the wait must notice by re-trying.
	go func() {
		time.Sleep(100 * time.Millisecond)
		helper.stop()
	}()
	slot, acquired, _, err = waitForTaskSlot(context.Background(), sem, nil, 10*time.Second)
	if err != nil || !acquired || slot != 0 {
		t.Fatalf("after the other process released: slot=%d acquired=%v err=%v", slot, acquired, err)
	}
}

func TestAGPatchSlotLocksUniqueAndReleasable(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	sem := newTaskSlotSemaphore(dir, 3, nil)

	got := acquireAll(t, sem)
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 slots", got)
	}
	// A second semaphore on the same dir (a second daemon) gets nothing, even
	// inside this one process: flock conflicts between open file descriptions.
	other := newTaskSlotSemaphore(dir, 3, nil)
	if slots := acquireAll(t, other); len(slots) != 0 {
		t.Fatalf("second semaphore took %v while all are held", slots)
	}
	for _, k := range []int{0, 1, 2} {
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(k)+".lock")); err != nil {
			t.Fatalf("lock file for slot %d: %v", k, err)
		}
	}

	sem.release(1)
	if slots := acquireAll(t, other); len(slots) != 1 || slots[0] != 1 {
		t.Fatalf("after releasing slot 1 the second semaphore got %v, want [1]", slots)
	}
}

func TestAGPatchSlotLocksFailClosed(t *testing.T) {
	skipWithoutFlock(t)
	// A regular file where the slot dir should be: MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "slots")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sem := newTaskSlotSemaphore(blocker, 2, nil)
	if slot, ok, err := sem.tryAcquire(); ok || err == nil {
		t.Fatalf("tryAcquire with an unusable slot dir: slot=%d ok=%v err=%v; want an error and no slot", slot, ok, err)
	}
	if len(sem.held) != 0 {
		t.Fatalf("held = %v, want none (no in-process fallback)", sem.held)
	}

	// waitForTaskSlot reports "not acquired" at once, without waiting out the
	// window, so the poller's capacityBackoff paces the retry.
	start := time.Now()
	_, acquired, woke, err := waitForTaskSlot(context.Background(), sem, nil, 5*time.Second)
	if err != nil || acquired || woke {
		t.Fatalf("waitForTaskSlot: acquired=%v woke=%v err=%v", acquired, woke, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waitForTaskSlot waited %s on an unusable slot dir", time.Since(start))
	}

	// An empty dir (config root unresolved) is also refused.
	if _, ok, err := newTaskSlotSemaphore("", 2, nil).tryAcquire(); ok || err == nil {
		t.Fatalf("empty slot dir: ok=%v err=%v; want an error", ok, err)
	}
}

func TestAGPatchPollerDoesNotClaimWhenSlotsUnavailable(t *testing.T) {
	skipWithoutFlock(t)
	blocker := filepath.Join(t.TempDir(), "slots")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	claims := make(chan struct{}, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "claim") {
			claims <- struct{}{}
		}
		http.NotFound(w, r)
	})
	d := newGCTestDaemon(t, mux)
	d.cfg.MaxConcurrentTasks = 2
	d.cfg.PollInterval = 20 * time.Millisecond
	d.taskSlotWait = 20 * time.Millisecond
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}

	sem := newTaskSlotSemaphore(blocker, 2, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &wg)
	select {
	case <-claims:
		t.Fatal("poller claimed tasks without holding a slot lock")
	default:
	}
}

func TestAGPatchHeavyGCWaitsForOtherDaemonSlots(t *testing.T) {
	skipWithoutFlock(t)
	d := newGCTestDaemon(t, http.NewServeMux())
	d.agSlotDir = filepath.Join(t.TempDir(), "slots")
	d.cfg.MaxConcurrentTasks = 2

	// Another daemon holds a slot.
	helper := startAGSlotHelper(t, d.agSlotDir, 2, 1)
	if !d.agOtherDaemonBusy() {
		t.Fatal("agOtherDaemonBusy = false while another process holds a slot")
	}

	// Our own slot doesn't count: activeTasks already covers it.
	helper.stop()
	sem := newTaskSlotSemaphore(d.agSlotDir, 2, nil)
	d.agSlots.Store(sem)
	if _, ok, err := sem.tryAcquire(); !ok || err != nil {
		t.Fatalf("take own slot: ok=%v err=%v", ok, err)
	}
	if d.agOtherDaemonBusy() {
		t.Fatal("agOtherDaemonBusy = true when only this daemon holds a slot")
	}
}

func TestAGPatchHeavyGCSlotProbeFailsClosed(t *testing.T) {
	skipWithoutFlock(t)
	d := newGCTestDaemon(t, http.NewServeMux())
	// A file where the slot dir should be: ReadDir fails with ENOTDIR.
	blocker := filepath.Join(t.TempDir(), "slots")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.agSlotDir = blocker
	d.cfg.MaxConcurrentTasks = 2
	if !d.agOtherDaemonBusy() {
		t.Fatal("a failing slot probe must count as busy")
	}
}

func TestAGPatchHeavyGCSkippedWhileOtherDaemonHoldsSlot(t *testing.T) {
	skipWithoutFlock(t)
	d := newGCTestDaemon(t, http.NewServeMux())
	d.agSlotDir = filepath.Join(t.TempDir(), "slots")
	d.cfg.MaxConcurrentTasks = 2

	sourceRepo := createGCGitRepo(t)
	barePath := filepath.Join(t.TempDir(), "cache.git")
	runGitForGC(t, "", "clone", "--bare", sourceRepo, barePath)
	const staleBranch = "agent/stale/87654321"
	runGitForGC(t, "", "-C", barePath, "branch", staleBranch, "HEAD")

	helper := startAGSlotHelper(t, d.agSlotDir, 2, 1)
	d.maintainRepoCache(context.Background(), barePath, &gcStats{})
	if gitRefExists(t, barePath, "refs/heads/"+staleBranch) {
		t.Fatal("light cleanup should still delete stale branches")
	}
	marker := filepath.Join(barePath, repoMaintenanceMarker)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("heavy maintenance must stay pending while another daemon has a task: %v", err)
	}

	// Once the other daemon is idle the pending maintenance runs and clears.
	helper.stop()
	d.maintainRepoCache(context.Background(), barePath, &gcStats{})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("heavy maintenance should run once the other daemon is idle (marker stat err = %v)", err)
	}
}

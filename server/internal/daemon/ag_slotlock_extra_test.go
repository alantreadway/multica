package daemon

// AG patch: further machine-wide slot cap tests (batch claims, contention,
// probe edge cases); TestAGPatch* as counted by ag-patches/test.sh. They reuse
// the helper process from ag_slotlock_test.go.

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Batch claims take extra slots the same non-blocking way: whatever another
// daemon holds is simply not available, and slots the poller hands back (it
// claimed fewer tasks than slots) are free for the other daemon again.
func TestAGPatchBatchClaimSlotsRespectOtherProcess(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	const total = 4
	helper := startAGSlotHelper(t, dir, total, 1)

	sem := newTaskSlotSemaphore(dir, total, nil)
	slot, acquired, _, err := waitForTaskSlot(t.Context(), sem, nil, time.Second)
	if err != nil || !acquired {
		t.Fatalf("first slot: acquired=%v err=%v", acquired, err)
	}
	slots := append([]int{slot}, drainAvailableSlots(sem, total-1)...)
	if len(slots) != total-1 {
		t.Fatalf("batch of %v with the helper holding %v; want %d slots", slots, helper.slots, total-1)
	}
	for _, s := range slots {
		if s == helper.slots[0] {
			t.Fatalf("slot %d claimed by both processes (ours %v, helper %v)", s, slots, helper.slots)
		}
	}
	// Nothing left for a second poller on the same machine.
	other := newTaskSlotSemaphore(dir, total, nil)
	if extra := drainAvailableSlots(other, total); len(extra) != 0 {
		t.Fatalf("second poller drained %v while the machine is full", extra)
	}

	// The claim returned one task: the unused slots go back and the other
	// daemon can take exactly those.
	for _, s := range slots[1:] {
		sem.release(s)
	}
	extra := drainAvailableSlots(other, total)
	if len(extra) != len(slots)-1 {
		t.Fatalf("after releasing %v the second poller got %v", slots[1:], extra)
	}
	for _, s := range extra {
		if s == slots[0] || s == helper.slots[0] {
			t.Fatalf("second poller got slot %d which is still held (ours %d, helper %v)", s, slots[0], helper.slots)
		}
	}
	if len(drainAvailableSlots(sem, 0)) != 0 || len(drainAvailableSlots(sem, -1)) != 0 {
		t.Fatal("a non-positive max must take nothing")
	}
}

// The cap holds the other way round too: a process starting while this one
// holds slots only gets the remainder.
func TestAGPatchSlotCapHoldsWhenOtherProcessStartsLater(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	const total = 3
	sem := newTaskSlotSemaphore(dir, total, nil)
	mine, err := sem.tryAcquireN(2)
	if err != nil || len(mine) != 2 {
		t.Fatalf("take 2: %v %v", mine, err)
	}
	helper := startAGSlotHelper(t, dir, total, total)
	if len(helper.slots) != 1 {
		t.Fatalf("helper took %v while we hold %v of %d; want exactly 1", helper.slots, mine, total)
	}
	for _, s := range mine {
		if s == helper.slots[0] {
			t.Fatalf("slot %d held by both processes", s)
		}
	}
}

// Hammer the cap: two daemons' worth of goroutines plus another process holding
// slots. The number of simultaneous holders never exceeds the cap and a slot is
// never held twice at once.
func TestAGPatchSlotCapHoldsUnderContention(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	const total, heldElsewhere = 6, 2
	helper := startAGSlotHelper(t, dir, total, heldElsewhere)
	elsewhere := map[int]bool{}
	for _, s := range helper.slots {
		elsewhere[s] = true
	}

	var running, peak atomic.Int32
	var occupied [total]atomic.Bool
	failed := make(chan string, 1)
	fail := func(msg string) {
		select {
		case failed <- msg:
		default:
		}
	}

	var wg sync.WaitGroup
	for d := 0; d < 2; d++ { // two daemons in one process, each with its own semaphore
		sem := newTaskSlotSemaphore(dir, total, nil)
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					slots, err := sem.tryAcquireN(2)
					if err != nil {
						fail("tryAcquireN: " + err.Error())
						return
					}
					for _, s := range slots {
						if elsewhere[s] {
							fail("took slot " + strconv.Itoa(s) + " held by the other process")
						}
						if !occupied[s].CompareAndSwap(false, true) {
							fail("slot " + strconv.Itoa(s) + " held twice at once")
						}
						n := running.Add(1)
						for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
						}
						if n > total-heldElsewhere {
							fail("more holders than the cap allows")
						}
					}
					time.Sleep(time.Millisecond)
					for _, s := range slots {
						occupied[s].Store(false)
						running.Add(-1)
						sem.release(s)
					}
				}
			}()
		}
	}
	wg.Wait()
	select {
	case msg := <-failed:
		t.Fatal(msg)
	default:
	}
	if peak.Load() == 0 {
		t.Fatal("no goroutine ever got a slot")
	}
}

// A lock file that can't be opened stops that slot; slots before it are still
// returned (and must be released by the caller), and the error is reported.
func TestAGPatchSlotLockFileUnopenableFailsClosed(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	if err := os.MkdirAll(filepath.Join(dir, "1.lock"), 0o755); err != nil { // a directory where slot 1's file belongs
		t.Fatal(err)
	}
	sem := newTaskSlotSemaphore(dir, 3, nil)
	got, err := sem.tryAcquireN(3)
	if err == nil {
		t.Fatalf("tryAcquireN over an unopenable lock file returned %v with no error", got)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("got %v, want only the slot before the broken one", got)
	}
	sem.release(0)

	// With the broken file first nothing can be taken and the poller helper logs
	// and returns no slots rather than falling back.
	if err := os.Remove(filepath.Join(dir, "0.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "0.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := sem.tryAcquire(); ok || err == nil {
		t.Fatalf("tryAcquire: ok=%v err=%v; want an error and no slot", ok, err)
	}
	if extra := drainAvailableSlots(sem, 2); len(extra) != 0 {
		t.Fatalf("drainAvailableSlots over broken lock files returned %v", extra)
	}
	if len(sem.held) != 0 {
		t.Fatalf("held = %v, want none", sem.held)
	}
}

// Releasing something that isn't held is harmless and doesn't free the slot for
// anyone else.
func TestAGPatchSlotReleaseIsIdempotent(t *testing.T) {
	skipWithoutFlock(t)
	dir := filepath.Join(t.TempDir(), "slots")
	sem := newTaskSlotSemaphore(dir, 2, nil)
	other := newTaskSlotSemaphore(dir, 2, nil)
	slots := acquireAll(t, sem)
	if len(slots) != 2 {
		t.Fatalf("got %v", slots)
	}
	sem.release(0)
	sem.release(0) // double release
	sem.release(7) // never held
	got := acquireAll(t, other)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("other got %v, want only slot 0 (slot 1 is still held)", got)
	}
}

// The GC probe is about slot locks only: stray files are ignored, a missing dir
// means nobody else is running, and slots beyond this daemon's own cap (another
// daemon configured larger) still count.
func TestAGPatchSlotProbeEdgeCases(t *testing.T) {
	skipWithoutFlock(t)
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.MaxConcurrentTasks = 2

	// No slot dir yet: a single daemon that has never claimed.
	d.agSlotDir = filepath.Join(t.TempDir(), "slots")
	if d.agOtherDaemonBusy() {
		t.Fatal("a missing slot dir must not count as busy")
	}

	// Stray files and unheld lock files are not a busy daemon.
	if err := os.MkdirAll(d.agSlotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "x.lock", "3.lock.bak", "0.lock", "1.lock"} {
		if err := os.WriteFile(filepath.Join(d.agSlotDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if d.agOtherDaemonBusy() {
		t.Fatal("unheld and unrelated files must not count as busy")
	}
	// The probe must not leave a slot locked behind it.
	sem := newTaskSlotSemaphore(d.agSlotDir, 2, nil)
	if got := acquireAll(t, sem); len(got) != 2 {
		t.Fatalf("after probing, a fresh semaphore got %v; want both slots", got)
	}
	sem.release(0)
	sem.release(1)

	// Another daemon with a larger cap holds slot 4, outside our [0,2).
	startAGSlotHelper(t, d.agSlotDir, 5, 5)
	if !d.agOtherDaemonBusy() {
		t.Fatal("a slot beyond this daemon's cap, held by another process, must count as busy")
	}
}

// With no other daemon on the machine heavy GC behaves as before: the slot dir
// being absent or idle never defers maintenance.
func TestAGPatchHeavyGCRunsWhenNoOtherDaemon(t *testing.T) {
	skipWithoutFlock(t)
	d := newGCTestDaemon(t, http.NewServeMux())
	d.agSlotDir = filepath.Join(t.TempDir(), "slots")
	d.cfg.MaxConcurrentTasks = 2

	sourceRepo := createGCGitRepo(t)
	barePath := filepath.Join(t.TempDir(), "cache.git")
	runGitForGC(t, "", "clone", "--bare", sourceRepo, barePath)

	d.maintainRepoCache(t.Context(), barePath, &gcStats{})
	if _, err := os.Stat(filepath.Join(barePath, repoMaintenanceMarker)); !os.IsNotExist(err) {
		t.Fatalf("heavy maintenance should have run and cleared its marker (stat err = %v)", err)
	}
}

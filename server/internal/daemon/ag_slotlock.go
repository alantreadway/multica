package daemon

// AG patch: machine-wide task slot cap.
//
// Upstream's cap is an in-process channel of slot indices, so two daemons on
// one machine each run MULTICA_DAEMON_MAX_CONCURRENT_TASKS agents. Here a slot
// is a lock file, <config dir>/slots/<k>.lock for k in [0, N), held with a
// non-blocking exclusive flock for as long as the task runs. The kernel drops
// the flock when the holder exits, and flock conflicts between separate open
// file descriptions even inside one process, so the same file settles
// contention between this daemon's goroutines and other daemons alike.
// MULTICA_TASK_SLOT therefore stays unique across every daemon on the machine.
//
// Fail closed: when the slot dir or a lock file can't be created or opened, no
// slot is granted and the poller backs off. There is deliberately no fallback
// to an in-process counter, which would let two daemons exceed the cap again.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	agSlotDirName = "slots"
	// agSlotRetryInterval is how often waitForTaskSlot re-tries the flocks while
	// it waits: a slot freed by another process sends us no wakeup.
	agSlotRetryInterval = 100 * time.Millisecond
)

// agTaskSlots hands out slot indices. The held map is the in-process half of
// the book-keeping; on platforms without flock it is the whole mechanism.
type agTaskSlots struct {
	dir    string
	n      int
	logger *slog.Logger
	retry  time.Duration

	mu   sync.Mutex
	held map[int]*os.File // slot -> open lock file (nil without flock)
}

// agDefaultSlotDir is per machine: shared by every profile of the same config
// root, not per profile.
func agDefaultSlotDir() (string, error) {
	root, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, agSlotDirName), nil
}

// agSlotDir resolves the slot dir for this daemon. dir is a test override.
func (d *Daemon) agSlotDirPath() string {
	if d.agSlotDir != "" {
		return d.agSlotDir
	}
	dir, err := agDefaultSlotDir()
	if err != nil {
		d.logger.Warn("task slots: resolve slot dir failed", "error", err)
		return "" // acquire fails closed on an empty dir
	}
	return dir
}

// agNewTaskSlots is pollLoop's slot semaphore: machine-wide slot locks under
// the slot dir, published on d.agSlots for the heavy-GC probe.
func (d *Daemon) agNewTaskSlots() *agTaskSlots {
	sem := newTaskSlotSemaphore(d.agSlotDirPath(), d.cfg.MaxConcurrentTasks, d.logger)
	d.agSlots.Store(sem)
	return sem
}

func newTaskSlotSemaphore(dir string, maxConcurrentTasks int, logger *slog.Logger) *agTaskSlots {
	if logger == nil {
		logger = slog.Default()
	}
	return &agTaskSlots{
		dir:    dir,
		n:      maxConcurrentTasks,
		logger: logger,
		retry:  agSlotRetryInterval,
		held:   make(map[int]*os.File),
	}
}

func (s *agTaskSlots) slotPath(k int) string {
	return filepath.Join(s.dir, strconv.Itoa(k)+".lock")
}

// tryAcquire takes the lowest free slot without blocking. ok is false when
// every slot is held; err means the slot dir or a lock file is unusable.
func (s *agTaskSlots) tryAcquire() (slot int, ok bool, err error) {
	slots, err := s.tryAcquireN(1)
	if len(slots) == 0 {
		return 0, false, err
	}
	return slots[0], true, err
}

// tryAcquireN takes up to max free slots without blocking. It returns what it
// got along with the first error hit.
func (s *agTaskSlots) tryAcquireN(max int) ([]int, error) {
	if max <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if agSlotFlockSupported {
		if s.dir == "" {
			return nil, os.ErrInvalid
		}
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return nil, err
		}
	}
	var got []int
	for k := 0; k < s.n && len(got) < max; k++ {
		if _, mine := s.held[k]; mine {
			continue
		}
		if !agSlotFlockSupported {
			s.held[k] = nil
			got = append(got, k)
			continue
		}
		f, err := os.OpenFile(s.slotPath(k), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return got, err
		}
		busy, err := agSlotTryFlock(f)
		if err != nil {
			f.Close()
			return got, err
		}
		if busy {
			f.Close()
			continue
		}
		s.held[k] = f
		got = append(got, k)
	}
	return got, nil
}

// release drops the flock and closes the file, freeing the slot for every
// daemon on the machine.
func (s *agTaskSlots) release(slot int) {
	s.mu.Lock()
	f, ok := s.held[slot]
	delete(s.held, slot)
	s.mu.Unlock()
	if ok && f != nil {
		f.Close() // closing releases the flock
	}
}

// otherProcessBusy reports whether any slot lock is held by someone other than
// this daemon. Slots are found by listing the dir, so a daemon configured with
// a larger cap still counts. A probe error is reported as busy (fail closed).
func (s *agTaskSlots) otherProcessBusy() (bool, error) {
	if !agSlotFlockSupported {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, err
	}
	for _, e := range entries {
		k, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".lock"))
		if err != nil || !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		if _, mine := s.held[k]; mine {
			continue
		}
		f, err := os.OpenFile(filepath.Join(s.dir, e.Name()), os.O_RDWR, 0o600)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return true, err
		}
		busy, err := agSlotTryFlock(f)
		f.Close() // also drops a lock the probe just took
		if err != nil {
			return true, err
		}
		if busy {
			return true, nil
		}
	}
	return false, nil
}

// agOtherDaemonBusy is the heavy-GC gate: another daemon holds a slot, or the
// probe failed. This daemon's own tasks are covered by activeTasks.
func (d *Daemon) agOtherDaemonBusy() bool {
	slots := d.agSlots.Load()
	if slots == nil {
		// pollLoop hasn't started, so this daemon holds nothing to exclude.
		slots = newTaskSlotSemaphore(d.agSlotDirPath(), d.cfg.MaxConcurrentTasks, d.logger)
	}
	busy, err := slots.otherProcessBusy()
	if err != nil {
		d.logger.Warn("gc: task slot probe failed; treating other daemons as busy", "error", err)
		return true
	}
	return busy
}

// waitForTaskSlot waits up to wait for a free slot. A slot freed by another
// process sends no wakeup, so it re-tries the flocks while waiting. An
// unusable slot dir returns at once as "not acquired" so the caller's
// capacityBackoff paces the retries, with one warning per cycle.
func waitForTaskSlot(ctx context.Context, sem *agTaskSlots, wakeup <-chan struct{}, wait time.Duration) (slot int, acquired, woke bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, false, err
	}
	try := func() (slot int, acquired, failed bool) {
		slot, ok, err := sem.tryAcquire()
		if err != nil {
			sem.logger.Warn("task slots: cannot take a slot lock; not claiming", "dir", sem.dir, "error", err)
			return 0, false, true
		}
		return slot, ok, false
	}
	if slot, ok, failed := try(); ok || failed {
		return slot, ok, false, nil
	}
	if wait <= 0 {
		return 0, false, false, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(sem.retry)
	defer ticker.Stop()
	for {
		select {
		case <-wakeup:
			return 0, false, true, nil
		case <-ctx.Done():
			return 0, false, false, ctx.Err()
		case <-timer.C:
			return 0, false, false, nil
		case <-ticker.C:
			if slot, ok, failed := try(); ok || failed {
				return slot, ok, false, nil
			}
		}
	}
}

// drainAvailableSlots non-blockingly takes up to max additional slots.
func drainAvailableSlots(sem *agTaskSlots, max int) []int {
	slots, err := sem.tryAcquireN(max)
	if err != nil {
		sem.logger.Warn("task slots: cannot take extra slot locks", "dir", sem.dir, "error", err)
	}
	return slots
}

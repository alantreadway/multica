package repocache

// AG patch: cross-process repo cache lock.
//
// repoLock only serialises inside one daemon. When two daemons share a cache
// root (a handoff), each repoLock also holds a flock on a lock file next to its
// bare repo, taken after the in-process lock and dropped before it, so two
// daemons never fetch, prune or GC the same bare repo at once. Fail closed: if
// the lock file can't be opened or locked the operation gets the error and git
// never runs unlocked.
//
// The in-process foreground-priority protocol is unchanged. Another daemon's
// maintenance can't be preempted, so a foreground operation waits for it to
// finish.

import (
	"context"
	"os"
	"time"
)

const (
	agRepoFlockSuffix = ".multica-lock"
	// agRepoFlockRetry is how often a contended flock is re-tried.
	agRepoFlockRetry = 50 * time.Millisecond
)

// agRepoFlockPath is the lock file for a bare repo. It sits beside the repo,
// not inside it, so a failed clone or a repo wipe never takes it away.
func agRepoFlockPath(barePath string) string {
	return barePath + agRepoFlockSuffix
}

// agRepoFlock is the cross-process half of a repoLock. The zero value is
// disabled (no path).
type agRepoFlock struct {
	path string
	file *os.File // non-nil while held
}

// openAndTry opens the lock file and tries the flock once. A nil file with
// nil error means another process holds it.
func (f *agRepoFlock) openAndTry() (*os.File, error) {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	busy, err := agRepoTryFlock(file)
	if err != nil || busy {
		file.Close()
		return nil, err
	}
	return file, nil
}

// lock waits for the flock until ctx is done. The caller already holds the
// in-process lock, so only another process can make this wait.
func (f *agRepoFlock) lock(ctx context.Context) error {
	if f.path == "" || !agRepoFlockSupported {
		return nil
	}
	for {
		file, err := f.openAndTry()
		if err != nil {
			return err
		}
		if file != nil {
			f.file = file
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(agRepoFlockRetry):
		}
	}
}

// tryLock takes the flock without waiting. ok is false when another process
// holds it.
func (f *agRepoFlock) tryLock() (ok bool, err error) {
	if f.path == "" || !agRepoFlockSupported {
		return true, nil
	}
	file, err := f.openAndTry()
	if err != nil || file == nil {
		return false, err
	}
	f.file = file
	return true, nil
}

func (f *agRepoFlock) unlock() {
	if f.file != nil {
		f.file.Close() // closing releases the flock
		f.file = nil
	}
}

// agLockFlock finishes a foreground lock: the in-process lock is held, now take
// the flock. On failure the in-process lock is given back.
func (l *repoLock) agLockFlock(ctx context.Context) error {
	if err := l.agFlock.lock(ctx); err != nil {
		l.Unlock()
		return err
	}
	return nil
}

// agTryFlock is the maintenance counterpart: false (also on error, failing
// closed) means maintenance must not start.
func (l *repoLock) agTryFlock() bool {
	ok, _ := l.agFlock.tryLock()
	return ok
}

//go:build !windows

package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

// AG patch: machine-wide task slot cap (see ag_slotlock.go).

const agSlotFlockSupported = true

// agSlotTryFlock takes a non-blocking exclusive flock on f. busy is true when
// another open file description (this process's or another's) holds it. The
// kernel drops the lock when the file is closed or the process dies, so a
// crashed daemon never strands a slot.
func agSlotTryFlock(f *os.File) (busy bool, err error) {
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return false, nil
	}
	if err == unix.EWOULDBLOCK {
		return true, nil
	}
	return false, err
}

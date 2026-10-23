//go:build !windows

package repocache

import (
	"os"

	"golang.org/x/sys/unix"
)

const agRepoFlockSupported = true

// agRepoTryFlock takes a non-blocking exclusive flock on f. busy is true when
// another open file description holds it.
func agRepoTryFlock(f *os.File) (busy bool, err error) {
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return false, nil
	}
	if err == unix.EWOULDBLOCK {
		return true, nil
	}
	return false, err
}

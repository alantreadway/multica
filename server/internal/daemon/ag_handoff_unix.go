//go:build !windows

package daemon

import "golang.org/x/sys/unix"

// AG patch: daemon handoff (see ag_handoff.go).

// agPIDAlive probes pid with signal 0. known is false when the platform cannot
// tell. EPERM means the process exists under another user.
func agPIDAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, true
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM, true
}

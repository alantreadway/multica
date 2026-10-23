//go:build windows

package daemon

// AG patch: daemon handoff (see ag_handoff.go).

// agPIDAlive cannot probe a PID here; the overlap loop falls back to counting
// failed /health reads.
func agPIDAlive(int) (alive, known bool) { return false, false }

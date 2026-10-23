package main

// AG patch: PID file owner check. Upstream files carry one-line hooks into this file.
//
// With two daemons of one profile alive at once (a handoff), the outgoing one
// must not delete the PID file the incoming one just wrote.

import (
	"os"
	"strconv"
	"strings"
)

// agRemoveDaemonPIDIfOwned removes the profile's PID file only while it still
// holds pid, in the manner of removeDaemonPortIfOwned. A missing, unreadable
// or unparsable file is left alone.
func agRemoveDaemonPIDIfOwned(profile string, pid int) {
	path := daemonPIDPathForProfile(profile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if got, err := strconv.Atoi(strings.TrimSpace(string(data))); err != nil || got != pid {
		return
	}
	_ = os.Remove(path)
}

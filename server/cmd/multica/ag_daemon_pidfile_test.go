package main

// AG patch: PID file owner check tests; TestAGPatch* as counted by
// ag-patches/test.sh.

import (
	"os"
	"strconv"
	"testing"
)

func writeAGPIDFile(t *testing.T, profile, content string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	path := daemonPIDPathForProfile(profile)
	if err := os.MkdirAll(daemonDirForProfile(profile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAGPatchPIDFileRemovedOnlyByOwner(t *testing.T) {
	path := writeAGPIDFile(t, "", strconv.Itoa(1234)+"\n")

	// An outgoing daemon (or a `daemon stop` of it) must leave the PID file
	// its successor wrote.
	agRemoveDaemonPIDIfOwned("", 999)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("PID file removed by a process that does not own it: %v", err)
	}

	agRemoveDaemonPIDIfOwned("", 1234)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("PID file should be removed by its owner (stat err = %v)", err)
	}
}

func TestAGPatchPIDFileOwnerCheckLeavesUnreadableContentAlone(t *testing.T) {
	path := writeAGPIDFile(t, "", "not-a-pid")
	agRemoveDaemonPIDIfOwned("", os.Getpid())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("an unparsable PID file must be left alone: %v", err)
	}

	// A missing file is not an error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	agRemoveDaemonPIDIfOwned("", os.Getpid())
}

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

// The owner check is per profile: another profile's PID file is never touched,
// and a trailing newline or spaces around the PID still count as ours.
func TestAGPatchPIDFileOwnerCheckPerProfile(t *testing.T) {
	path := writeAGPIDFile(t, "staging", "  4321 \n")
	if err := os.MkdirAll(daemonDirForProfile(""), 0o755); err != nil {
		t.Fatal(err)
	}
	defaultPath := daemonPIDPathForProfile("")
	if err := os.WriteFile(defaultPath, []byte("4321"), 0o644); err != nil {
		t.Fatal(err)
	}

	agRemoveDaemonPIDIfOwned("staging", 4321)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staging PID file should be removed by its owner (stat err = %v)", err)
	}
	if _, err := os.Stat(defaultPath); err != nil {
		t.Fatalf("the default profile's PID file must not be touched: %v", err)
	}
}

// A successor that rewrote the PID file after the outgoing daemon started
// stopping keeps it: removal by the old PID, then by `daemon stop` of the old
// PID, both leave the new one.
func TestAGPatchPIDFileSuccessorSurvivesOutgoingRemoval(t *testing.T) {
	path := writeAGPIDFile(t, "", "100")
	if err := os.WriteFile(path, []byte("200"), 0o644); err != nil { // successor takes over
		t.Fatal(err)
	}
	agRemoveDaemonPIDIfOwned("", 100) // outgoing daemon's deferred cleanup
	agRemoveDaemonPIDIfOwned("", 100) // `daemon stop` of the old PID
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "200" {
		t.Fatalf("successor's PID file = %q, err %v; want it left intact", data, err)
	}
}

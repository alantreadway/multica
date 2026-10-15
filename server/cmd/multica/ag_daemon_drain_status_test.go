package main

// AG patch: daemon drain status and resume edge tests. Names are TestAGPatch*
// so ag-patches/test.sh picks them up.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAGPatchDaemonStatusAgainstDrainingDaemon(t *testing.T) {
	const profile = "agdrain-status"

	t.Run("AG patch: status text shows claiming, active tasks and idle for a draining daemon", func(t *testing.T) {
		mkProfiles(t, profile)
		started := time.Now().UTC().Format(time.RFC3339)
		startAGFakeDaemon(t, profile, &agFakeDaemon{
			draining: true, startedAt: started, active: 3, running: 2, waiting: 1, idleAfterPolls: -1,
		})

		out, err := captureStdout(t, func() error {
			return runDaemonStatus(daemonStatusCmdFor(t, profile, ""), nil)
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		for _, want := range []string{
			"no (draining since " + time.Now().Local().Format("15:04") + ")",
			"3 (2 running, 1 waiting for resources)",
			"Idle",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("status output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("AG patch: status text for a claiming daemon has no Idle row", func(t *testing.T) {
		mkProfiles(t, profile)
		startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, running: 1, idleAfterPolls: -1})

		out, err := captureStdout(t, func() error {
			return runDaemonStatus(daemonStatusCmdFor(t, profile, ""), nil)
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out, "Claiming") || !strings.Contains(out, "yes") {
			t.Errorf("want Claiming: yes, got:\n%s", out)
		}
		if strings.Contains(out, "Idle") {
			t.Errorf("Idle must only be shown while draining:\n%s", out)
		}
	})

	t.Run("AG patch: status --output json carries the drain health fields", func(t *testing.T) {
		mkProfiles(t, profile)
		started := time.Now().UTC().Format(time.RFC3339)
		startAGFakeDaemon(t, profile, &agFakeDaemon{
			draining: true, startedAt: started, active: 2, running: 2, idleAfterPolls: -1,
		})

		out, err := captureStdout(t, func() error {
			return runDaemonStatus(daemonStatusCmdFor(t, profile, "json"), nil)
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("status --output json is not valid JSON: %v\n%s", err, out)
		}
		if got["draining"] != true || got["idle"] != false || got["active_task_count"] != float64(2) || got["drain_started_at"] != started {
			t.Errorf("health fields not carried through: %v", got)
		}
	})
}

func TestAGPatchDaemonResumeEdges(t *testing.T) {
	const profile = "agdrain-resume"

	t.Run("AG patch: resume on a claiming daemon succeeds", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{active: 1, idleAfterPolls: -1})

		if err := runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil); err != nil {
			t.Fatalf("resume on a daemon that is not draining: %v", err)
		}
		if f.draining {
			t.Error("daemon is draining after resume")
		}
	})

	t.Run("AG patch: resume does not drain", func(t *testing.T) {
		mkProfiles(t, profile)
		f, _ := startAGFakeDaemon(t, profile, &agFakeDaemon{draining: true, idleAfterPolls: -1})

		if err := runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if f.called("POST /drain") || f.called("POST /shutdown") {
			t.Errorf("resume made unexpected calls: %v", f.callLog())
		}
	})

	t.Run("AG patch: resume with no daemon running fails", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("error = %v, want a not-running error", err)
		}
	})
}

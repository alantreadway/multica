package main

// AG patch: daemon drain status and resume edge tests.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func parseStatusRows(out string) map[string]string {
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			rows[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return rows
}

func TestAGPatchDaemonStatusAgainstDrainingDaemon(t *testing.T) {
	const profile = "agdrain-status"

	t.Run("AG patch: status text shows claiming, active tasks and idle for a draining daemon", func(t *testing.T) {
		mkProfiles(t, profile)
		started := time.Now().UTC().Truncate(time.Second)
		startAGFakeDaemon(t, profile, &agFakeDaemon{
			draining: true, startedAt: started.Format(time.RFC3339), active: 3, running: 2, waiting: 1, idleAfterPolls: -1,
		})

		out, err := captureStdout(t, func() error {
			return runDaemonStatus(daemonStatusCmdFor(t, profile, ""), nil)
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		rows := parseStatusRows(out)
		want := map[string]string{
			"Claiming":     "no (draining since " + started.Local().Format("15:04") + ")",
			"Active tasks": "3 (2 running, 1 waiting for resources)",
			"Idle":         "no",
		}
		for k, v := range want {
			if rows[k] != v {
				t.Errorf("%s = %q, want %q\n%s", k, rows[k], v, out)
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
		rows := parseStatusRows(out)
		if rows["Claiming"] != "yes" {
			t.Errorf("Claiming = %q, want yes\n%s", rows["Claiming"], out)
		}
		if _, ok := rows["Idle"]; ok {
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

	t.Run("AG patch: resume with no daemon running fails", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonResume(agDrainCmdFor(t, "resume", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("error = %v, want a not-running error", err)
		}
	})
}

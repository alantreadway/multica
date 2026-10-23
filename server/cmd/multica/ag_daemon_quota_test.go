package main

// AG patch: daemon quota CLI tests.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// agFakeQuotaDaemon serves /health with a claude_quota block and counts
// /quota/clear calls. cleared is what the clear reports; unsupported behaves
// like a daemon built without this patch.
type agFakeQuotaDaemon struct {
	mu          sync.Mutex
	profile     string
	quota       map[string]any
	cleared     int
	unsupported bool
	clearCalls  int
}

func (f *agFakeQuotaDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/health":
		health := map[string]any{"status": "running", "profile": f.profile, "pid": os.Getpid(), "draining": false, "active_task_count": 0}
		if f.quota != nil {
			health["claude_quota"] = f.quota
		}
		_ = json.NewEncoder(w).Encode(health)
	case "/quota/clear":
		if f.unsupported {
			http.NotFound(w, r)
			return
		}
		f.clearCalls++
		_ = json.NewEncoder(w).Encode(map[string]int{"cleared": f.cleared})
	default:
		http.NotFound(w, r)
	}
}

func startAGFakeQuotaDaemon(t *testing.T, profile string, f *agFakeQuotaDaemon) {
	t.Helper()
	f.profile = profile
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: os.Getpid()}); err != nil {
		t.Fatalf("publishDaemonPort: %v", err)
	}
}

func agQuotaHealthBlock(until time.Time, reason string) map[string]any {
	return map[string]any{
		"held": true,
		"accounts": []any{
			map[string]any{"account": "/home/u/.claude", "default": true, "hold": map[string]any{
				"window": "five_hour", "until": until.UTC().Format(time.RFC3339), "reason": reason, "gating": true,
			}},
			map[string]any{"account": "/home/u/.claude-b", "hold": map[string]any{
				"window": "seven_day", "until": until.UTC().Format(time.RFC3339), "reason": "rejected", "gating": false,
			}},
			map[string]any{"account": "/home/u/.claude-c"},
		},
	}
}

func TestAGPatchDaemonQuotaClear(t *testing.T) {
	const profile = "agquota-clear"

	t.Run("AG patch: quota clear posts to the daemon and says the hold ended", func(t *testing.T) {
		mkProfiles(t, profile)
		f := &agFakeQuotaDaemon{cleared: 1}
		startAGFakeQuotaDaemon(t, profile, f)
		out, err := captureStdout(t, func() error {
			return runDaemonQuotaClear(agDrainCmdFor(t, "clear", profile, map[string]string{}), nil)
		})
		if err != nil || f.clearCalls != 1 || !strings.Contains(out, "Quota hold cleared") {
			t.Fatalf("err=%v calls=%d out=%q", err, f.clearCalls, out)
		}
	})

	t.Run("AG patch: quota clear with no hold says so", func(t *testing.T) {
		mkProfiles(t, profile)
		startAGFakeQuotaDaemon(t, profile, &agFakeQuotaDaemon{})
		out, err := captureStdout(t, func() error {
			return runDaemonQuotaClear(agDrainCmdFor(t, "clear", profile, map[string]string{}), nil)
		})
		if err != nil || !strings.Contains(out, "No quota hold was active") {
			t.Fatalf("err=%v out=%q", err, out)
		}
	})

	t.Run("AG patch: quota clear against an unpatched daemon fails with a pointer to the patched build", func(t *testing.T) {
		mkProfiles(t, profile)
		startAGFakeQuotaDaemon(t, profile, &agFakeQuotaDaemon{unsupported: true})
		err := runDaemonQuotaClear(agDrainCmdFor(t, "clear", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "does not support /quota/clear") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("AG patch: quota clear with no daemon running fails", func(t *testing.T) {
		mkProfiles(t, profile)
		err := runDaemonQuotaClear(agDrainCmdFor(t, "clear", profile, map[string]string{}), nil)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("AG patch: the command is registered as daemon quota clear", func(t *testing.T) {
		cmd, _, err := daemonCmd.Find([]string{"quota", "clear"})
		if err != nil || cmd != daemonQuotaClearCmd {
			t.Fatalf("daemon quota clear not registered: %v %v", cmd, err)
		}
	})
}

func TestAGPatchDaemonStatusShowsQuotaHold(t *testing.T) {
	const profile = "agquota-status"
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	clock := until.Local().Format("15:04")

	t.Run("AG patch: status shows the hold on the default account and on other accounts", func(t *testing.T) {
		mkProfiles(t, profile)
		startAGFakeQuotaDaemon(t, profile, &agFakeQuotaDaemon{quota: agQuotaHealthBlock(until, "rejected")})
		out, err := captureStdout(t, func() error {
			return runDaemonStatus(daemonStatusCmdFor(t, profile, ""), nil)
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		rows := parseStatusRows(out)
		if got, want := rows["Claude"], "quota hold (five_hour) until "+clock; got != want {
			t.Errorf("Claude = %q, want %q\n%s", got, want, out)
		}
		if got, want := rows["Claude (/home/u/.claude-b)"], "quota hold (seven_day) until "+clock+", not gating"; got != want {
			t.Errorf("other account = %q, want %q\n%s", got, want, out)
		}
		if strings.Contains(out, ".claude-c") {
			t.Errorf("an account with no hold must not get a row:\n%s", out)
		}
	})

	t.Run("AG patch: status marks a hold whose reset time is unknown", func(t *testing.T) {
		rows := agQuotaStatusRows(map[string]any{"claude_quota": agQuotaHealthBlock(until, "reset_unknown")})
		if len(rows) == 0 || !strings.HasSuffix(rows[0][1], ", reset time unknown") {
			t.Fatalf("rows = %v", rows)
		}
	})

	t.Run("AG patch: status has no quota rows without a hold, with no quota block or with a bad time", func(t *testing.T) {
		if rows := agQuotaStatusRows(map[string]any{}); len(rows) != 0 {
			t.Fatalf("rows = %v", rows)
		}
		if rows := agQuotaStatusRows(map[string]any{"claude_quota": map[string]any{"accounts": []any{map[string]any{"account": "x"}}}}); len(rows) != 0 {
			t.Fatalf("rows = %v", rows)
		}
		bad := map[string]any{"claude_quota": map[string]any{"accounts": []any{
			map[string]any{"account": "x", "default": true, "hold": map[string]any{"until": "soon"}},
		}}}
		if rows := agQuotaStatusRows(bad); len(rows) != 0 {
			t.Fatalf("rows = %v", rows)
		}
	})
}

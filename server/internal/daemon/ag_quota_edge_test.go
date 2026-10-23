package daemon

// AG patch: Claude quota edge cases beyond the main hold/clear/restart flows;
// TestAGPatch* with "AG patch:" subtests, as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAGPatchQuotaHoldNeverShortens(t *testing.T) {
	newDaemon := func(t *testing.T) (*Daemon, *agQuotaClock) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		return newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock), clock
	}
	send := func(d *Daemon, raw string) {
		d.agQuotaObserver(Task{ID: "t"}, "claude", nil)(json.RawMessage(raw))
	}
	held := func(d *Daemon) *agQuotaHold {
		_, h := d.agQuotaDefaultHold()
		return h
	}
	const weekly = 1792821600 // later than the five-hour reset in agQuotaResets

	t.Run("AG patch: a later rejected reset extends the hold and a shorter one does not cut it", func(t *testing.T) {
		d, clock := newDaemon(t)
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		clock.set(clock.now().Add(time.Second)) // the state file merges holds by set_at
		send(d, `{"status":"rejected","rateLimitType":"seven_day","resetsAt":1792821600}`)
		h := held(d)
		want := time.Unix(weekly, 0).UTC().Add(agQuotaResetGrace)
		if h == nil || !h.Until.Equal(want) || h.Window != "seven_day" {
			t.Fatalf("hold = %+v, want until %s on seven_day", h, want)
		}
		clock.set(clock.now().Add(time.Second))
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		if h := held(d); h == nil || !h.Until.Equal(want) || h.Window != "seven_day" {
			t.Fatalf("a shorter rejection cut the hold: %+v", h)
		}
	})

	t.Run("AG patch: a rejection with a known reset replaces a reset_unknown hold", func(t *testing.T) {
		d, clock := newDaemon(t)
		send(d, `{"status":"rejected","rateLimitType":"five_hour"}`)
		if h := held(d); h == nil || h.Reason != agQuotaReasonResetUnknown {
			t.Fatalf("hold = %+v", h)
		}
		clock.set(clock.now().Add(time.Second))
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		h := held(d)
		if h == nil || h.Reason != agQuotaReasonRejected || !h.Until.Equal(agQuotaResets.Add(agQuotaResetGrace)) {
			t.Fatalf("hold = %+v", h)
		}
	})

	t.Run("AG patch: a resetsAt in milliseconds holds until the same instant as seconds", func(t *testing.T) {
		d, _ := newDaemon(t)
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600000}`)
		if h := held(d); h == nil || !h.Until.Equal(agQuotaResets.Add(agQuotaResetGrace)) {
			t.Fatalf("hold = %+v", h)
		}
	})

	t.Run("AG patch: statuses other than rejected never hold, even at full utilization", func(t *testing.T) {
		for _, status := range []string{"allowed", "allowed_warning", "", "queued", "something_new"} {
			d, _ := newDaemon(t)
			send(d, `{"status":"`+status+`","rateLimitType":"five_hour","utilization":1,"resetsAt":1792272600,"unifiedWindows":{"five_hour":{"utilization":1,"resetsAt":1792272600}}}`)
			if h := held(d); h != nil {
				t.Errorf("status %q held: %+v", status, h)
			}
		}
	})
}

func TestAGPatchQuotaAccountKey(t *testing.T) {
	t.Run("AG patch: the run's own CLAUDE_CONFIG_DIR wins over the daemon's, which wins over ~/.claude", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		if got, want := agQuotaAccountKey(nil), filepath.Join(home, ".claude"); got != want {
			t.Errorf("default = %q, want %q", got, want)
		}
		daemonDir := filepath.Join(t.TempDir(), "daemon-claude")
		t.Setenv("CLAUDE_CONFIG_DIR", daemonDir+"/")
		if got := agQuotaAccountKey(map[string]string{}); got != daemonDir {
			t.Errorf("daemon env = %q, want %q (trailing slash cleaned)", got, daemonDir)
		}
		own := filepath.Join(t.TempDir(), "own-claude")
		if got := agQuotaAccountKey(map[string]string{"CLAUDE_CONFIG_DIR": own}); got != own {
			t.Errorf("agent env = %q, want %q", got, own)
		}
		if got := agQuotaAccountKey(map[string]string{"CLAUDE_CONFIG_DIR": "  "}); got != daemonDir {
			t.Errorf("blank agent value = %q, want the daemon's %q", got, daemonDir)
		}
	})
}

func TestAGPatchQuotaConcurrentUse(t *testing.T) {
	t.Run("AG patch: observers, claims, health and clears can run together (go test -race)", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)

		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				obs := d.agQuotaObserver(Task{ID: "t"}, "claude", nil)
				for j := 0; j < 25; j++ {
					obs(json.RawMessage(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`))
					obs(json.RawMessage(`{"status":"rej`))
					obs(json.RawMessage(`{"status":"allowed","rateLimitType":"five_hour","utilization":0.1}`))
				}
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 25; j++ {
					_, _ = d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex"}, 1)
					_ = d.agQuotaHealth()
					d.agQuotaClear()
				}
			}()
		}
		wg.Wait()

		if h := d.agQuotaHealth().ClaudeQuota; h == nil || h.DroppedEvents != 100 {
			t.Fatalf("health = %+v, want 100 dropped events", h)
		}
	})
}

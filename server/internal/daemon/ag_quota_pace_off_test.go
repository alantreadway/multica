package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestAGPatchPaceOff covers the pace_off runtime setting: claims use Max while
// it is in force, the limiter keeps its own cap for when it ends, and a quota
// hold still gates Claude.
func TestAGPatchPaceOff(t *testing.T) {
	ids := []string{"rt-claude", "rt-codex"}
	paced := func(t *testing.T) (*agPaceFixture, *agPaceSpecServer) {
		t.Helper()
		srv := newAGPaceSpecServer(t)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		now := f.clock.now()
		// Paced zone with the cap pinned at 1: the burn is unknown, so it holds.
		f.event(t, 0.90, 0.10, now.Add(2*time.Hour), now.Add(100*time.Hour))
		f.d.agPace.mu.Lock()
		f.d.agPace.accountLocked(f.acct).limiter = agPaceLimiter{cap: 1, set: true, changedAt: now}
		f.d.agPace.mu.Unlock()
		srv.tasks = func(c agPaceClaimCall) int { return 0 }
		return f, srv
	}
	setting := func(off bool) *protocol.AGRuntimeSettings {
		return &protocol.AGRuntimeSettings{BackupMaxConcurrent: 2, PaceOff: off}
	}
	claim := func(t *testing.T, f *agPaceFixture, srv *agPaceSpecServer) []agPaceClaimCall {
		t.Helper()
		srv.reset()
		if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", ids, 5); err != nil {
			t.Fatal(err)
		}
		return srv.recorded()
	}

	t.Run("AG patch: with pace_off on a Claude runtime the claim uses Max and the report says so", func(t *testing.T) {
		f, srv := paced(t)
		if calls := claim(t, f, srv); len(calls) != 2 || calls[0].MaxTasks != 1 {
			t.Fatalf("paced calls = %+v, want a split claim with 1 for Claude", calls)
		}

		f.d.agApplySettings("rt-claude", setting(true))
		calls := claim(t, f, srv)
		if len(calls) != 1 || calls[0].MaxTasks != 5 || len(calls[0].RuntimeIDs) != 2 {
			t.Fatalf("calls = %+v, want one unsplit claim of 5", calls)
		}
		r := f.d.agQuotaReport("rt-claude")
		if !r.PaceOff || r.EffectiveCap != 5 || r.Max != 5 {
			t.Fatalf("report = pace_off %v, cap %d of %d; want true, 5 of 5", r.PaceOff, r.EffectiveCap, r.Max)
		}
	})

	t.Run("AG patch: switching pace_off off resumes at the limiter's own cap", func(t *testing.T) {
		f, srv := paced(t)
		f.d.agApplySettings("rt-claude", setting(true))
		claim(t, f, srv)
		f.d.agApplySettings("rt-claude", setting(false))
		if calls := claim(t, f, srv); len(calls) != 2 || calls[0].MaxTasks != 1 {
			t.Fatalf("calls = %+v, want the paced split claim again", calls)
		}
		if r := f.d.agQuotaReport("rt-claude"); r.PaceOff || r.EffectiveCap != 1 {
			t.Fatalf("report = pace_off %v, cap %d; want false, 1", r.PaceOff, r.EffectiveCap)
		}
	})

	t.Run("AG patch: pace_off lapses with the setting's TTL when no ack renews it", func(t *testing.T) {
		f, srv := paced(t)
		f.d.agApplySettings("rt-claude", setting(true))
		f.clock.set(f.clock.now().Add(agSettingsTTL))
		if f.d.agPaceOff() {
			t.Fatal("pace_off still in force after the TTL")
		}
		if calls := claim(t, f, srv); len(calls) != 2 || calls[0].MaxTasks != 1 {
			t.Fatalf("calls = %+v, want the paced split claim", calls)
		}
	})

	t.Run("AG patch: pace_off on a runtime that is not Claude does nothing", func(t *testing.T) {
		f, _ := paced(t)
		f.d.agApplySettings("rt-codex", setting(true))
		if f.d.agPaceOff() {
			t.Fatal("a Codex runtime's setting turned pacing off")
		}
	})

	t.Run("AG patch: a quota hold still keeps Claude out of the claim", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t1"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")
		d.agApplySettings("rt-claude", setting(true))

		res, err := d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-claude-b"}, 2)
		if err != nil || len(res.Tasks) != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if got := s.claimed(); len(got) != 0 {
			t.Fatalf("server was called with %v", got)
		}
		if r := d.agQuotaReport("rt-claude"); r.EffectiveCap != 0 || r.Hold == nil {
			t.Fatalf("report = cap %d hold %v; want 0 and a hold", r.EffectiveCap, r.Hold)
		}
	})
}

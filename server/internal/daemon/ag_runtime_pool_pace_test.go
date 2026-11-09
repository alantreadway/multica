package daemon

// AG patch: runtime pool, Claude pacing across the pool. TestAGPatch* with
// "AG patch:" subtests, as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestAGPatchRuntimePoolPaceWorkedExample is the 2026-11-09 case: one account
// at 21% of its weekly window, working-hours pacing (Mon-Fri 08:00-19:00),
// Monday 15:05 with the reset on Saturday 06:00, so 7.08 of the window's 55
// working hours are past. Bastion has 2 slots, the laptop 3, each running 1
// Claude run, and the account rises 1% an hour.
func TestAGPatchRuntimePoolPaceWorkedExample(t *testing.T) {
	const u, duDt = 0.21, 0.01
	work := &agPaceWork{Elapsed: 7.08 / 55, Left: time.Duration((55 - 7.08) * float64(time.Hour))}
	weekly := func(burn, share float64, lead bool, max int) int {
		w := &agPaceWin{Util: u, Remaining: 87 * time.Hour, Burn: burn, BurnKnown: true, Work: work, Share: share, Lead: lead}
		n, hold := agPaceWeeklyAllowed(w, max)
		if hold {
			t.Fatal("hold with a known burn")
		}
		return agPaceTarget(n, float64(max), max)
	}

	t.Run("AG patch: alone, each daemon divides du/dt by its 1 run and stays at 1", func(t *testing.T) {
		// n = (0.97 - 0.21) / (47.92h * 0.01) = 1.59 on both.
		if b, l := weekly(duDt/1, 0, false, 2), weekly(duDt/1, 0, false, 3); b != 1 || l != 1 {
			t.Fatalf("bastion/laptop = %d/%d, want 1/1", b, l)
		}
	})

	t.Run("AG patch: pool-aware, the burn halves and the caps add up to one pacer's 3", func(t *testing.T) {
		// b = 0.01 / 2 = 0.005; n = 0.76 / (47.92 * 0.005) = 3.17 pool runs.
		n, _ := agPaceWeeklyAllowed(&agPaceWin{Util: u, Remaining: 87 * time.Hour, Burn: duDt / 2, BurnKnown: true, Work: work}, 5)
		if !agPaceApprox(n, 3.17) {
			t.Fatalf("pool n = %.3f, want 3.17", n)
		}
		// Bastion: 3.17 * 2/5 = 1.27 -> 1. Laptop (lead): 3.17 - floor(3.17 * 2/5) = 2.17 -> 2.
		bastion := weekly(duDt/2, 2.0/5, false, 2)
		laptop := weekly(duDt/2, 3.0/5, true, 3)
		if bastion != 1 || laptop != 2 || bastion+laptop != int(math.Floor(n)) {
			t.Fatalf("bastion/laptop = %d/%d, want 1/2 (sum floor(n) = 3)", bastion, laptop)
		}
	})

	t.Run("AG patch: the split never adds up to more than floor(n)", func(t *testing.T) {
		for n := 0.05; n < 12; n += 0.05 {
			for _, slots := range [][2]int{{2, 3}, {1, 1}, {3, 3}, {1, 4}, {2, 0}} {
				a, b := slots[0], slots[1]
				wa := float64(a) / float64(a+b)
				// b leads when it has more slots; a on a tie.
				aLead := a >= b
				na := agPoolShareRuns(n, wa, aLead)
				nb := agPoolShareRuns(n, 1-wa, !aLead)
				sum := math.Floor(na) + math.Floor(nb)
				if sum > math.Floor(n)+1e-9 {
					t.Fatalf("n=%.2f slots=%v: %v + %v > floor(n)", n, slots, math.Floor(na), math.Floor(nb))
				}
				if sum < math.Floor(n)-1e-9 {
					t.Fatalf("n=%.2f slots=%v: %v + %v < floor(n), the lead lost a run", n, slots, math.Floor(na), math.Floor(nb))
				}
			}
		}
	})
}

// agPoolPaceFixture is a pacing fixture in a pool, with the weekly window paced
// and the five-hour one well under its knee.
func agPoolPaceFixture(t *testing.T, pool string) (*agPaceFixture, time.Time, time.Time) {
	t.Helper()
	f := newAGPaceFixture(t, 5)
	f.d.cfg.AGPool = pool
	now := f.clock.now()
	return f, now.Add(4 * time.Hour), now.Add(150 * time.Hour)
}

func (f *agPaceFixture) weeklyBurn(t *testing.T) (float64, bool) {
	t.Helper()
	f.d.agPace.mu.Lock()
	defer f.d.agPace.mu.Unlock()
	st := f.d.agPace.accounts[f.acct].wins[agQuotaWindowSevenDay]
	if st == nil {
		return 0, false
	}
	return st.burn, st.known
}

// targetNow evaluates with the limiter allowed to jump, so it returns the target.
func (f *agPaceFixture) targetNow(t *testing.T) (int, int) {
	t.Helper()
	f.d.agPace.mu.Lock()
	f.d.agPace.accountLocked(f.acct).jump = true
	f.d.agPace.mu.Unlock()
	return f.cap(t)
}

func TestAGPatchRuntimePoolPaceBurn(t *testing.T) {
	peer := &protocol.AGPoolPace{PeerActive: 1, PeerSlots: 3, Slots: 2}

	t.Run("AG patch: the burn divides by local and peer runs, and the claim room by local runs", func(t *testing.T) {
		f, reset5, reset7 := agPoolPaceFixture(t, "alan")
		now := f.clock.now()
		f.event(t, 0.10, 0.400, reset5, reset7)
		done := f.d.agPaceRunStart("r1", f.acct, now)
		defer done()
		f.d.agPoolApplyAck("rt-claude", peer)

		f.clock.set(now.Add(30 * time.Minute))
		f.d.agPoolApplyAck("rt-claude", peer) // the next heartbeats keep it fresh
		f.event(t, 0.10, 0.401, reset5, reset7)
		// du/dt = 0.002/h over 1 local + 1 peer run: 0.001 per run-hour.
		if b, known := f.weeklyBurn(t); !known || math.Abs(b-0.001) > 1e-9 {
			t.Fatalf("burn = %v (known %v), want 0.001", b, known)
		}

		// n = (0.97 - 0.401) / (149.5h * 0.001) = 3.81; Bastion's 2/5 is 1.52.
		c, active := f.targetNow(t)
		if c != 1 || active != 1 {
			t.Fatalf("cap/active = %d/%d, want 1/1 (active is local only)", c, active)
		}
		h := f.d.agPaceHealth()
		if h.ActiveClaude != 1 || h.PoolActiveClaude == nil || *h.PoolActiveClaude != 2 ||
			h.PoolShare == nil || *h.PoolShare != 0.4 || h.PoolLead == nil || *h.PoolLead {
			t.Fatalf("health = %+v", h)
		}

		// As the lead with 3 of 5 slots: 3.81 - floor(3.81 * 0.4) = 2.81 -> 2.
		f.d.agPoolApplyAck("rt-claude", &protocol.AGPoolPace{PeerActive: 1, PeerSlots: 2, Slots: 3, Lead: true})
		if c, _ := f.targetNow(t); c != 2 {
			t.Fatalf("lead cap = %d, want 2", c)
		}
	})

	t.Run("AG patch: without a pool the ack is ignored and the burn is local", func(t *testing.T) {
		f, reset5, reset7 := agPoolPaceFixture(t, "")
		now := f.clock.now()
		f.event(t, 0.10, 0.400, reset5, reset7)
		done := f.d.agPaceRunStart("r1", f.acct, now)
		defer done()
		f.d.agPoolApplyAck("rt-claude", peer)
		f.clock.set(now.Add(30 * time.Minute))
		f.d.agPoolApplyAck("rt-claude", peer)
		f.event(t, 0.10, 0.401, reset5, reset7)
		if b, _ := f.weeklyBurn(t); math.Abs(b-0.002) > 1e-9 {
			t.Fatalf("burn = %v, want 0.002", b)
		}
		if h := f.d.agPaceHealth(); h.PoolActiveClaude != nil || h.PoolShare != nil {
			t.Fatalf("health without a pool = %+v", h)
		}
	})

	t.Run("AG patch: an ack without ag_pool_pace drops it, and a stale one counts for nothing", func(t *testing.T) {
		f, _, _ := agPoolPaceFixture(t, "alan")
		f.d.agPoolApplyAck("rt-claude", peer)
		if h := f.d.agPaceHealth(); h.PoolActiveClaude == nil || *h.PoolActiveClaude != 1 {
			t.Fatalf("health = %+v, want pool_active_claude 1", h)
		}
		f.d.agPoolApplyAck("rt-claude", nil) // an old server, or the runtime left the pool
		if h := f.d.agPaceHealth(); h.PoolActiveClaude != nil {
			t.Fatalf("after an ack without it: %+v", h)
		}

		f.d.agPoolApplyAck("rt-claude", peer)
		f.clock.set(f.clock.now().Add(agPoolPaceTTL))
		if h := f.d.agPaceHealth(); h.PoolActiveClaude != nil {
			t.Fatalf("stale entry still counts: %+v", h)
		}
		f.d.agPace.mu.Lock()
		n := f.d.agPoolPeerActiveLocked(f.acct, f.clock.now())
		f.d.agPace.mu.Unlock()
		if n != 0 {
			t.Fatalf("stale peers = %d, want 0", n)
		}
	})

	t.Run("AG patch: another account (an agent's own CLAUDE_CONFIG_DIR) does not count peers", func(t *testing.T) {
		f, _, _ := agPoolPaceFixture(t, "alan")
		f.d.agPoolApplyAck("rt-claude", peer)
		f.d.agPace.mu.Lock()
		n := f.d.agPoolPeerActiveLocked("/elsewhere/.claude", f.clock.now())
		f.d.agPace.mu.Unlock()
		if n != 0 {
			t.Fatalf("peers on another account = %d", n)
		}
	})
}

// TestAGPatchRuntimePoolPaceHeartbeatAck decodes acks the way both transports
// do and feeds them to handleHeartbeatActions.
func TestAGPatchRuntimePoolPaceHeartbeatAck(t *testing.T) {
	f, _, _ := agPoolPaceFixture(t, "alan")
	beat := func(raw string) {
		t.Helper()
		var resp HeartbeatResponse
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatal(err)
		}
		f.d.handleHeartbeatActions(context.Background(), "rt-claude", &resp)
	}

	t.Run("AG patch: a new server's ack sets the pool", func(t *testing.T) {
		beat(`{"status":"ok","ag_pool_pace":{"peer_active":2,"peer_slots":3,"slots":2,"lead":false}}`)
		h := f.d.agPaceHealth()
		if h.PoolActiveClaude == nil || *h.PoolActiveClaude != 2 || *h.PoolShare != 0.4 || *h.PoolLead {
			t.Fatalf("health = %+v", h)
		}
		raw, _ := json.Marshal(h)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["pool_active_claude"] != float64(2) || m["pool_share"] != 0.4 || m["pool_lead"] != false {
			t.Fatalf("ag_quota JSON = %s", raw)
		}
	})

	t.Run("AG patch: an old server's ack (no field) falls back to local runs", func(t *testing.T) {
		beat(`{"status":"ok"}`)
		if h := f.d.agPaceHealth(); h.PoolActiveClaude != nil {
			t.Fatalf("health = %+v", h)
		}
		raw, _ := json.Marshal(f.d.agPaceHealth())
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if _, ok := m["pool_active_claude"]; ok {
			t.Fatalf("ag_quota JSON = %s", raw)
		}
	})
}

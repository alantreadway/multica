package daemon

// AG patch: runtime pool. MULTICA_AG_POOL names the pool this daemon's runtimes
// join. It is sent as ag_pool on every register, and the server lets runtimes
// with the same pool, workspace, owner and provider claim each other's agents'
// queued tasks. Unset, the register request is exactly upstream's.

import (
	"fmt"
	"math"
	"time"

	"github.com/multica-ai/multica/server/internal/agpool"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agPoolFromEnv reads MULTICA_AG_POOL. An invalid name fails daemon start, so
// a typo cannot silently leave the machine out of its pool.
func agPoolFromEnv(lookup func(string) (string, bool)) (string, error) {
	raw, _ := lookup(agpool.EnvVar)
	name, ok := agpool.Normalize(raw)
	if !ok {
		return "", fmt.Errorf("%s: invalid pool name %q (letters, digits, '.', '_' or '-', up to 64, starting with a letter or digit)", agpool.EnvVar, raw)
	}
	return name, nil
}

// agAddPoolToRegister adds ag_pool to a register request when a pool is set.
func agAddPoolToRegister(req map[string]any, pool string) {
	if pool != "" {
		req["ag_pool"] = pool
	}
}

// --- Claude pacing across the pool ------------------------------------------------
//
// Pool peers usually share one Claude account, and each daemon's pacer
// (ag_quota_pace.go) reads that account's utilisation. Divided by local runs
// alone, du/dt overstates the burn per run by about pool runs / local runs, so
// every daemon caps itself too low. The heartbeat ack of a pooled Claude
// runtime carries ag_pool_pace (the server's view of the online peers on the
// same account), and the pacer uses it twice:
//
//   - burn: the time-weighted active runs b divides by also count the peers'
//     dispatched and running tasks;
//   - share: the runs the account can afford, n, are pool-wide, so this daemon
//     takes w = slots / (slots + peer slots) of them: floor(n * w). The lead
//     (the runtime with the most slots, chosen by the server) takes what the
//     others' floors leave, floor(n) - floor(n * (1 - w)). With the same n on
//     every daemon the caps add up to floor(n), what one pacer would allow, and
//     never more; the floor of 1 per daemon is the only excess. Each daemon
//     estimates n itself from the shared utilisation, so the estimates agree
//     to within sampling noise.
//
// An ack without ag_pool_pace (a stock server, a daemon outside a pool, a
// failed lookup) drops that runtime's entry at once, and an entry older than
// agPoolPaceTTL (acks stopped) counts for nothing: pacing falls back to local
// runs, which is the conservative side (a higher burn, a lower cap).

// agPoolPaceTTL is how long the last ag_pool_pace stays in use without a fresh
// ack. Heartbeats come every 15 seconds.
const agPoolPaceTTL = 2 * time.Minute

type agPoolPaceEntry struct {
	pace protocol.AGPoolPace
	at   time.Time
}

// agPoolPaceState lives in agPaceState and is guarded by its mu.
type agPoolPaceState struct {
	byRT map[string]agPoolPaceEntry
}

// agPoolApplyAck keeps the ag_pool_pace a received heartbeat ack carried for
// runtimeID; nil drops it. A daemon outside a pool ignores it.
func (d *Daemon) agPoolApplyAck(runtimeID string, pace *protocol.AGPoolPace) {
	if runtimeID == "" || d.cfg.AGPool == "" {
		return
	}
	p := &d.agPace
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, had := p.pool.byRT[runtimeID]; pace == nil && !had {
		return
	}
	now := d.agQuotaNow()
	def := agQuotaAccountKey(nil)
	a := p.accountLocked(def)
	d.agPaceAccrueLocked(a, def, now) // the old peer count up to now
	if pace == nil {
		delete(p.pool.byRT, runtimeID)
	} else {
		if p.pool.byRT == nil {
			p.pool.byRT = map[string]agPoolPaceEntry{}
		}
		p.pool.byRT[runtimeID] = agPoolPaceEntry{pace: *pace, at: now}
	}
	d.agPaceAccrueLocked(a, def, now)
}

// agPoolShareRuns is this daemon's part of n pool-wide runs (n > 0): n * share,
// or for the lead n less the others' rounded-down part.
func agPoolShareRuns(n, share float64, lead bool) float64 {
	if lead {
		return n - math.Floor(n*(1-share))
	}
	return n * share
}

// agPoolView is the pool as the pacer sees it, from the fresh acks.
type agPoolView struct {
	peerActive int
	share      float64 // 1 outside a pool
	lead       bool
	ok         bool // false with no fresh ack
}

// agPoolLocked sums the fresh entries. With several pooled Claude runtimes (one
// per workspace) the peers are summed, this daemon's slots are the largest any
// runtime reported, and it leads only if every runtime does.
func (d *Daemon) agPoolLocked(now time.Time) agPoolView {
	v := agPoolView{share: 1, lead: true}
	slots, peerSlots := 0, 0
	for _, e := range d.agPace.pool.byRT {
		if now.Sub(e.at) >= agPoolPaceTTL {
			continue
		}
		v.ok = true
		v.peerActive += max(e.pace.PeerActive, 0)
		peerSlots += max(e.pace.PeerSlots, 0)
		slots = max(slots, e.pace.Slots)
		v.lead = v.lead && e.pace.Lead
	}
	if !v.ok {
		return agPoolView{share: 1}
	}
	if slots+peerSlots > 0 {
		v.share = float64(slots) / float64(slots+peerSlots)
	}
	return v
}

// agPoolPeerActiveLocked is the peers' runs counted against account (the
// daemon's own account only), added to the local count the burn divides by.
func (d *Daemon) agPoolPeerActiveLocked(account string, now time.Time) int {
	if account != agQuotaAccountKey(nil) {
		return 0
	}
	return d.agPoolLocked(now).peerActive
}

// agPoolShareLocked sets this daemon's share of the pool's runs on the windows
// the pacer evaluates for account (the daemon's own account only).
func (d *Daemon) agPoolShareLocked(account string, now time.Time, wins ...*agPaceWin) {
	if account != agQuotaAccountKey(nil) {
		return
	}
	v := d.agPoolLocked(now)
	if !v.ok {
		return
	}
	for _, w := range wins {
		if w != nil {
			w.Share, w.Lead = v.share, v.lead
		}
	}
}

// agPoolHealthLocked adds the pool to /health's ag_quota block: the pool-wide
// Claude runs the burn divides by (local plus peers), this daemon's share and
// whether it leads. Absent without fresh pool data.
func (d *Daemon) agPoolHealthLocked(info *agQuotaPaceInfo, now time.Time) {
	v := d.agPoolLocked(now)
	if !v.ok {
		return
	}
	def := agQuotaAccountKey(nil)
	total := d.agPaceActiveLocked(def, def) + v.peerActive
	share := math.Round(v.share*100) / 100
	lead := v.lead
	info.PoolActiveClaude, info.PoolShare, info.PoolLead = &total, &share, &lead
}

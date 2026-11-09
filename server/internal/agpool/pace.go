package agpool

// Claude pacing across a pool. Each daemon paces Claude claims against its
// account's utilisation (the daemon's ag_quota_pace.go). Pool peers usually
// share one account, so each daemon has to count its peers' runs when it turns
// the account's du/dt into a burn per run, and take only its share of the runs
// the account can afford. The server returns what the daemon cannot see: the
// peers' active runs and claim slots (ListAGPoolPaceRuntimes); Pace adds them
// up for the heartbeat ack.

import (
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SameAccountTolerance is how far two runtimes' weekly resets may differ and
// still be one account. Anthropic reports the reset per account, so two
// accounts almost never share it to the minute.
const SameAccountTolerance = 2 * time.Minute

// PaceRuntime is one row of ListAGPoolPaceRuntimes.
type PaceRuntime struct {
	ID     string
	Self   bool
	Active int
	// Slots is the runtime's claim slots; -1 when unknown.
	Slots int
	// WeeklyResetsAt is the weekly reset its stored quota report carries
	// (RFC 3339); empty when none.
	WeeklyResetsAt string
}

// Pace sums the peers of a pooled runtime for its heartbeat ack. rows are the
// runtime itself and its online peers; reportResetsAt and reportMax come from
// the quota report the heartbeat carried, which is newer than the stored one.
// ok is false without a self row (the runtime is not pooled).
//
// A peer whose weekly reset differs from this runtime's by more than
// SameAccountTolerance is on another account and is left out. When either side
// has not reported a reset yet, the peer is assumed to share the account.
// Unknown slots count as this runtime's own. Lead is set when no counted peer
// has more slots, or as many and a lower ID.
func Pace(rows []PaceRuntime, reportResetsAt string, reportMax int) (protocol.AGPoolPace, bool) {
	var self *PaceRuntime
	for i := range rows {
		if rows[i].Self {
			self = &rows[i]
			break
		}
	}
	if self == nil {
		return protocol.AGPoolPace{}, false
	}
	slots := self.Slots
	if slots < 0 {
		slots = reportMax
	}
	if slots < 0 {
		slots = 0
	}
	resets := reportResetsAt
	if resets == "" {
		resets = self.WeeklyResetsAt
	}
	out := protocol.AGPoolPace{Slots: slots, Lead: true}
	for _, r := range rows {
		if r.Self || !sameAccount(resets, r.WeeklyResetsAt) {
			continue
		}
		peerSlots := r.Slots
		if peerSlots < 0 {
			peerSlots = slots
		}
		out.PeerActive += max(r.Active, 0)
		out.PeerSlots += peerSlots
		if peerSlots > slots || (peerSlots == slots && r.ID < self.ID) {
			out.Lead = false
		}
	}
	return out, true
}

func sameAccount(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return true
	}
	return ta.Sub(tb).Abs() <= SameAccountTolerance
}

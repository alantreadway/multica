// Package agtokenrate holds the live token rates patched daemons attach to
// heartbeats (AG patch). Rates are not stored: the server keeps the latest per
// runtime in memory, publishes it to the workspace, and serves it to pages that
// load between heartbeats. A restart or a second server instance only means a
// page shows no rate until the runtime's next heartbeat.
package agtokenrate

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TTL is how long a rate is served after its heartbeat: three missed 15s
// heartbeats.
const TTL = 45 * time.Second

// Rate is one runtime's rate as published and listed.
type Rate struct {
	RuntimeID       string  `json:"runtime_id"`
	WorkspaceID     string  `json:"workspace_id"`
	InputPerSec     float64 `json:"input_per_sec"`
	OutputPerSec    float64 `json:"output_per_sec"`
	CacheReadPerSec float64 `json:"cache_read_per_sec"`
	WindowSeconds   int     `json:"window_seconds"`
	ActiveRuns      int     `json:"active_runs"`
	ObservedAt      string  `json:"observed_at"`
}

func (r Rate) idle() bool {
	return r.ActiveRuns == 0 && r.InputPerSec == 0 && r.OutputPerSec == 0 && r.CacheReadPerSec == 0
}

type entry struct {
	rate Rate
	at   time.Time
}

// Board is the in-memory latest rate per runtime.
type Board struct {
	mu        sync.Mutex
	byRuntime map[string]entry
}

// Default is the server's board.
var Default = &Board{}

// Valid rejects a report no daemon would send.
func Valid(r *protocol.DaemonTokenRateReport) bool {
	for _, v := range []float64{r.InputPerSec, r.OutputPerSec, r.CacheReadPerSec} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return false
		}
	}
	return r.WindowSeconds > 0 && r.ActiveRuns >= 0
}

// Apply records a report and says whether to publish it: every report while
// the runtime is busy, and the first idle one after, so viewers see it drop to
// zero. Idle-to-idle heartbeats are recorded but not published.
func (b *Board) Apply(runtimeID, workspaceID string, r *protocol.DaemonTokenRateReport, now time.Time) (Rate, bool) {
	rate := Rate{
		RuntimeID:       runtimeID,
		WorkspaceID:     workspaceID,
		InputPerSec:     r.InputPerSec,
		OutputPerSec:    r.OutputPerSec,
		CacheReadPerSec: r.CacheReadPerSec,
		WindowSeconds:   r.WindowSeconds,
		ActiveRuns:      r.ActiveRuns,
		ObservedAt:      now.UTC().Format(time.RFC3339),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byRuntime == nil {
		b.byRuntime = make(map[string]entry)
	}
	prev, had := b.byRuntime[runtimeID]
	b.byRuntime[runtimeID] = entry{rate: rate, at: now}
	publish := !rate.idle() || (had && now.Sub(prev.at) <= TTL && !prev.rate.idle())
	return rate, publish
}

// List returns the workspace's rates heard within TTL.
func (b *Board) List(workspaceID string, now time.Time) []Rate {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Rate{}
	for id, e := range b.byRuntime {
		if now.Sub(e.at) > TTL {
			delete(b.byRuntime, id)
			continue
		}
		if e.rate.WorkspaceID == workspaceID {
			out = append(out, e.rate)
		}
	}
	return out
}

type reportKey struct{}

// WithReport carries a WebSocket heartbeat's report to the heartbeat handler
// without widening daemonws.HeartbeatHandler.
func WithReport(ctx context.Context, r *protocol.DaemonTokenRateReport) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, reportKey{}, r)
}

// ReportFrom returns the report WithReport stored, or nil.
func ReportFrom(ctx context.Context) *protocol.DaemonTokenRateReport {
	r, _ := ctx.Value(reportKey{}).(*protocol.DaemonTokenRateReport)
	return r
}

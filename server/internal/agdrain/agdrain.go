// Package agdrain is the server half of the AG patch's drain badge: it turns
// the drain report on a daemon heartbeat into agent_runtime.metadata["ag_drain"].
//
// It is its own package, with no database of its own, so the write-only-on-
// change rules can be tested without Postgres (the handler package's tests
// skip when no database is reachable).
package agdrain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// MetadataKey is where the drain state lives in agent_runtime.metadata.
const MetadataKey = "ag_drain"

// RefreshInterval is how often a runtime that is still draining re-checks its
// stored state. Registration overwrites metadata wholesale, so a connection
// that outlives a re-register would otherwise believe a key that is gone.
// The check is a guarded UPDATE, a no-op while the key is intact.
const RefreshInterval = 60 * time.Second

// State is the stored value. A runtime that is not draining has no key at all.
type State struct {
	Draining    bool  `json:"draining"`
	Idle        bool  `json:"idle"`
	ActiveTasks int64 `json:"active_tasks"`
}

// Memo is what a caller remembers of the stored state between heartbeats:
// the HTTP path rebuilds it from the runtime row it already loaded, the
// WebSocket path keeps it on the connection's runtime lease.
type Memo struct {
	// Known is false until the stored state has been observed or written.
	Known bool
	// State is nil when no drain key is stored.
	State     *State
	WrittenAt time.Time
}

// Store is the two guarded queries. Both return rows affected: 0 means the
// stored state already matched.
type Store interface {
	SetAGDrain(ctx context.Context, runtimeID pgtype.UUID, state []byte) (int64, error)
	ClearAGDrain(ctx context.Context, runtimeID pgtype.UUID) (int64, error)
}

// MemoFromMetadata reads the stored state out of a runtime row's metadata.
func MemoFromMetadata(metadata []byte, now time.Time) Memo {
	m := Memo{Known: true, WrittenAt: now}
	if len(metadata) == 0 {
		return m
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return m
	}
	raw, ok := doc[MetadataKey]
	if !ok {
		return m
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil || !s.Draining {
		// Not a state this patch wrote. A guarded write will replace it.
		return Memo{Known: false, WrittenAt: now}
	}
	m.State = &s
	return m
}

// target is the state a report asks for: nil when the daemon is not draining.
func target(r *protocol.DaemonDrainReport) *State {
	if !r.Draining {
		return nil
	}
	active := r.ActiveTasks
	if active < 0 {
		active = 0
	}
	return &State{Draining: true, Idle: r.Idle && active == 0, ActiveTasks: active}
}

func same(a, b *State) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Apply reconciles one heartbeat's report with the stored state and returns
// the memo to keep and whether the stored state changed.
//
// A nil report (a stock daemon, or an older patched one) touches nothing.
// Otherwise it writes only when the report differs from the memo, so the
// steady state, drain or no drain, costs no query per heartbeat.
func Apply(ctx context.Context, store Store, runtimeID pgtype.UUID, memo Memo, report *protocol.DaemonDrainReport, now time.Time) (Memo, bool, error) {
	if report == nil {
		return memo, false, nil
	}
	want := target(report)
	if memo.Known && same(memo.State, want) &&
		(want == nil || now.Sub(memo.WrittenAt) < RefreshInterval) {
		return memo, false, nil
	}

	var (
		rows int64
		err  error
	)
	if want == nil {
		rows, err = store.ClearAGDrain(ctx, runtimeID)
	} else {
		var payload []byte
		payload, err = json.Marshal(want)
		if err != nil {
			return memo, false, err
		}
		rows, err = store.SetAGDrain(ctx, runtimeID, payload)
	}
	if err != nil {
		// Keep the old memo so the next heartbeat retries.
		return memo, false, err
	}
	return Memo{Known: true, State: want, WrittenAt: now}, rows > 0, nil
}

type reportKey struct{}

// WithReport carries a WebSocket heartbeat's drain report to the heartbeat
// handler without widening daemonws.HeartbeatHandler.
func WithReport(ctx context.Context, r *protocol.DaemonDrainReport) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, reportKey{}, r)
}

// ReportFrom returns the report WithReport stored, or nil.
func ReportFrom(ctx context.Context) *protocol.DaemonDrainReport {
	r, _ := ctx.Value(reportKey{}).(*protocol.DaemonDrainReport)
	return r
}

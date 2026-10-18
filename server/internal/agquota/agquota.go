// Package agquota writes a daemon heartbeat's Claude quota report into
// agent_runtime.metadata["ag_quota"] and reads the runtime's backup setting
// ("ag_settings") back out. It is separate from the handler package so its
// write-on-change rules are testable without Postgres. It follows package agdrain.
package agquota

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	// MetadataKey is where the report lives in agent_runtime.metadata.
	MetadataKey = "ag_quota"
	// SettingsKey is where the UI-stored setting lives. Registration preserves it
	// (see the two upserts in runtime.sql); every other metadata key is replaced.
	SettingsKey = "ag_settings"

	// DefaultBackupMaxConcurrent applies when a PUT leaves backup_max_concurrent unset.
	DefaultBackupMaxConcurrent = 2
	// MaxBackupMaxConcurrent is a sanity bound; the daemon clamps to its own max.
	MaxBackupMaxConcurrent = 100
)

// RefreshInterval is how often a runtime re-runs the guarded write even when the
// report is unchanged. It bounds how stale a WebSocket lease's copy of the stored
// setting can be, and repairs a row that registration or another writer reset.
const RefreshInterval = 60 * time.Second

// Memo is what a caller remembers between heartbeats: the HTTP path rebuilds it
// from the runtime row, the WS path keeps it on the lease.
type Memo struct {
	// Known is false until the stored report has been observed or written.
	Known bool
	// Report is the normalised JSON of the stored report; nil when none is stored.
	Report    []byte
	WrittenAt time.Time
	// Settings is the stored setting; nil when none is stored.
	Settings *protocol.AGRuntimeSettings
}

// Store is the one guarded query. It returns whether the stored report changed
// and the runtime's stored ag_settings (nil when there is none).
type Store interface {
	SetAGQuota(ctx context.Context, runtimeID pgtype.UUID, report []byte) (changed bool, settings []byte, err error)
}

// Normalize returns the canonical JSON for a report, so a report and the same
// report read back from jsonb compare equal.
func Normalize(r *protocol.DaemonQuotaReport) ([]byte, error) {
	return json.Marshal(r)
}

// Valid reports whether a report is fit to store. A malformed one is logged by
// the caller and not written.
func Valid(r *protocol.DaemonQuotaReport) bool {
	if r == nil {
		return false
	}
	if r.Account != "primary" && r.Account != "backup" {
		return false
	}
	return r.EffectiveCap >= 0 && r.Max >= 0
}

// MemoFromMetadata reads the stored report and setting out of a runtime row's
// metadata.
func MemoFromMetadata(metadata []byte, now time.Time) Memo {
	m := Memo{Known: true, WrittenAt: now}
	if len(metadata) == 0 {
		return m
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return m
	}
	m.Settings = ParseSettings(doc[SettingsKey])
	raw, ok := doc[MetadataKey]
	if !ok {
		return m
	}
	var r protocol.DaemonQuotaReport
	if err := json.Unmarshal(raw, &r); err != nil {
		// Not a report this patch wrote. A guarded write will replace it.
		return Memo{Known: false, WrittenAt: now, Settings: m.Settings}
	}
	norm, err := Normalize(&r)
	if err != nil {
		return Memo{Known: false, WrittenAt: now, Settings: m.Settings}
	}
	m.Report = norm
	return m
}

// SettingsFromMetadata returns the stored setting, or nil.
func SettingsFromMetadata(metadata []byte) *protocol.AGRuntimeSettings {
	if len(metadata) == 0 {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return nil
	}
	return ParseSettings(doc[SettingsKey])
}

// ParseSettings decodes a stored ag_settings value. It returns nil for a missing,
// malformed or out-of-range value, which the daemon reads as "backup disabled".
func ParseSettings(raw []byte) *protocol.AGRuntimeSettings {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var s protocol.AGRuntimeSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	if s.BackupMaxConcurrent < 1 || s.BackupMaxConcurrent > MaxBackupMaxConcurrent {
		return nil
	}
	return &s
}

// Apply reconciles one heartbeat's report with the stored state and returns the
// memo to keep and whether the stored report changed. A nil report (a stock
// daemon, or a non-Claude runtime) touches nothing; otherwise it writes only when
// the report differs from the memo, or when RefreshInterval has passed.
func Apply(ctx context.Context, store Store, runtimeID pgtype.UUID, memo Memo, report *protocol.DaemonQuotaReport, now time.Time) (Memo, bool, error) {
	if report == nil {
		return memo, false, nil
	}
	want, err := Normalize(report)
	if err != nil {
		return memo, false, err
	}
	if memo.Known && bytes.Equal(memo.Report, want) && now.Sub(memo.WrittenAt) < RefreshInterval {
		return memo, false, nil
	}
	changed, settings, err := store.SetAGQuota(ctx, runtimeID, want)
	if err != nil {
		// Keep the old memo so the next heartbeat retries.
		return memo, false, err
	}
	return Memo{Known: true, Report: want, WrittenAt: now, Settings: ParseSettings(settings)}, changed, nil
}

type reportKey struct{}

// WithReport carries a WebSocket heartbeat's quota report to the heartbeat
// handler without widening daemonws.HeartbeatHandler.
func WithReport(ctx context.Context, r *protocol.DaemonQuotaReport) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, reportKey{}, r)
}

// ReportFrom returns the report WithReport stored, or nil.
func ReportFrom(ctx context.Context) *protocol.DaemonQuotaReport {
	r, _ := ctx.Value(reportKey{}).(*protocol.DaemonQuotaReport)
	return r
}

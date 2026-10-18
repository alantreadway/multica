package daemon

// AG patch: Claude quota report and runtime backup setting.
//
// Each heartbeat for a Claude runtime carries an ag_quota report built from the
// quota tracker (ag_quota.go) and the pacing limiter (ag_quota_pace.go); the
// server stores it in the runtime's metadata for the runtimes page. The ack
// carries ag_settings back: the backup-account setting the UI stored for that
// runtime. This unit only reports and keeps the setting; detecting and using a
// backup account is separate.
//
// Fail policy: a runtime that is not Claude reports nothing; no setting on the
// ack means backup is disabled. The last setting received is kept in memory for
// agSettingsTTL to ride out a missed ack, and dropped on restart.

import (
	"math"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	// agSettingsTTL is how long the last-received setting stays in force without
	// a fresh ack.
	agSettingsTTL = 10 * time.Minute
	// agBackupDefaultMaxConcurrent mirrors the server's default for an unset value.
	agBackupDefaultMaxConcurrent = 2

	agQuotaAccountPrimary = "primary"
)

// agSettingsState is the daemon's copy of the stored per-runtime settings.
type agSettingsState struct {
	mu      sync.Mutex
	byRT    map[string]protocol.AGRuntimeSettings
	savedAt map[string]time.Time
}

// agApplySettings keeps the setting an ack carried for runtimeID. A nil setting
// (a stock server, or none stored) leaves the last one standing until it expires.
func (d *Daemon) agApplySettings(runtimeID string, s *protocol.AGRuntimeSettings) {
	if s == nil || runtimeID == "" || s.BackupMaxConcurrent < 1 {
		return
	}
	st := &d.agSettings
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.byRT == nil {
		st.byRT = map[string]protocol.AGRuntimeSettings{}
		st.savedAt = map[string]time.Time{}
	}
	st.byRT[runtimeID] = *s
	st.savedAt[runtimeID] = d.agQuotaNow()
}

// agBackupSetting returns the backup setting in force for runtimeID: the last one
// received within agSettingsTTL, else backup disabled with the default limit.
func (d *Daemon) agBackupSetting(runtimeID string) protocol.AGRuntimeSettings {
	st := &d.agSettings
	st.mu.Lock()
	defer st.mu.Unlock()
	if s, ok := st.byRT[runtimeID]; ok && d.agQuotaNow().Sub(st.savedAt[runtimeID]) < agSettingsTTL {
		return s
	}
	return protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: agBackupDefaultMaxConcurrent}
}

// agQuotaReport builds the report for one heartbeat, or nil for a runtime that is
// not Claude. It describes the daemon's own account.
func (d *Daemon) agQuotaReport(runtimeID string) *protocol.DaemonQuotaReport {
	if !d.agQuotaIsClaudeRuntime(runtimeID) {
		return nil
	}
	_, hold := d.agQuotaDefaultHold()
	windows := d.agQuotaDefaultWindows()
	setting := d.agBackupSetting(runtimeID)

	report := &protocol.DaemonQuotaReport{
		Account:       agQuotaAccountPrimary,
		EffectiveCap:  d.agPaceHealth().EffectiveCap,
		Max:           d.cfg.MaxConcurrentTasks,
		Windows:       map[string]protocol.DaemonQuotaWindow{},
		BackupWindows: map[string]protocol.DaemonQuotaWindow{},
		Backup: protocol.DaemonQuotaBackup{
			Enabled:       setting.BackupEnabled,
			MaxConcurrent: setting.BackupMaxConcurrent,
		},
	}
	if hold != nil {
		report.EffectiveCap = 0
		report.Hold = &protocol.DaemonQuotaHold{Until: hold.Until.UTC().Format(time.RFC3339), Window: hold.Window}
	}
	for _, name := range []string{agQuotaWindowFiveHour, agQuotaWindowSevenDay} {
		if w, ok := windows[name]; ok {
			report.Windows[name] = agQuotaReportWindow(w)
		}
	}
	return report
}

// agQuotaReportWindow rounds a tracked window so the server's write-on-change
// guard fires only on a real change: utilisation to 0.01, observed_at to the minute.
func agQuotaReportWindow(w agQuotaWindow) protocol.DaemonQuotaWindow {
	out := protocol.DaemonQuotaWindow{}
	if w.Utilization != nil {
		u := math.Round(*w.Utilization*100) / 100
		out.Utilization = &u
	}
	if !w.ResetsAt.IsZero() {
		out.ResetsAt = w.ResetsAt.UTC().Format(time.RFC3339)
	}
	if !w.ObservedAt.IsZero() {
		out.ObservedAt = w.ObservedAt.UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	return out
}

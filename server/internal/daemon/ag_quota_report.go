package daemon

// AG patch: Claude quota report and runtime backup setting.
//
// Each heartbeat for a Claude runtime carries an ag_quota report built from the
// quota tracker (ag_quota.go) and the pacing limiter (ag_quota_pace.go); the
// server stores it in the runtime's metadata for the runtimes page. The ack
// carries ag_settings back: the backup-account setting the UI stored for that
// runtime; ag_quota_backup.go detects and uses the backup account.
//
// Fail policy: a runtime that is not Claude reports nothing; a received ack with
// no setting disables backup for that runtime at once (a rollback to a stock
// server stops failover on the next heartbeat). The last setting received is
// kept in memory for agSettingsTTL only to ride out an ack that never arrives
// (a missed or failed heartbeat), and dropped on restart.

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

// agApplySettings keeps the setting a received ack carried for runtimeID. A nil
// setting (a stock server, or none stored) disables backup for the runtime now.
func (d *Daemon) agApplySettings(runtimeID string, s *protocol.AGRuntimeSettings) {
	if runtimeID == "" {
		return
	}
	if s == nil {
		s = &protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: agBackupDefaultMaxConcurrent}
	} else if s.BackupMaxConcurrent < 1 {
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
// not Claude. It describes the daemon's own account, or the backup while urgent
// Claude work is failing over to it (see agBackupFillReport).
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
	d.agBackupFillReport(report, runtimeID, hold != nil) // AG patch: backup account
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

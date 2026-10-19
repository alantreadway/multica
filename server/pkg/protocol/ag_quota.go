package protocol

// AG patch: Claude quota report and runtime backup setting.

// DaemonQuotaReport is the Claude quota state a patched daemon attaches to each
// Claude runtime's heartbeat (HTTP and WebSocket). The server merges it into
// agent_runtime.metadata under "ag_quota". Windows and Hold describe the daemon's
// own account.
//
// Utilisation is rounded to 0.01 and ObservedAt to the minute so the server's
// write-on-change guard fires only on a real change.
type DaemonQuotaReport struct {
	// Account is "primary" or "backup": the account new Claude runs use. While it
	// is "backup", Max and EffectiveCap are the backup's; Windows and Hold stay the
	// primary's (the reason for the failover) and BackupWindows are the backup's.
	Account string `json:"account"`
	// Hold is set while a quota rejection gates Claude claims.
	Hold *DaemonQuotaHold `json:"hold,omitempty"`
	// EffectiveCap is the most Claude runs the next claim may leave in flight; 0
	// while a hold gates claims. Max is MULTICA_DAEMON_MAX_CONCURRENT_TASKS.
	EffectiveCap int `json:"effective_cap"`
	Max          int `json:"max"`
	// Windows is keyed by window name (five_hour, seven_day).
	Windows map[string]DaemonQuotaWindow `json:"windows"`
	Backup  DaemonQuotaBackup            `json:"backup"`
	// BackupWindows is the backup account's windows, once it is detected.
	BackupWindows map[string]DaemonQuotaWindow `json:"backup_windows"`
}

// DaemonQuotaHold is a quota hold on Claude claims. Until is RFC 3339.
type DaemonQuotaHold struct {
	Until  string `json:"until"`
	Window string `json:"window,omitempty"`
}

// DaemonQuotaWindow is one rate-limit window. Utilization is nil when the daemon
// has not seen one; ResetsAt and ObservedAt are RFC 3339.
type DaemonQuotaWindow struct {
	Utilization *float64 `json:"utilization,omitempty"`
	ResetsAt    string   `json:"resets_at,omitempty"`
	ObservedAt  string   `json:"observed_at,omitempty"`
}

// DaemonQuotaBackup is the daemon's view of the backup Claude account plus the
// stored setting it is running with.
type DaemonQuotaBackup struct {
	Detected bool   `json:"detected"`
	Valid    bool   `json:"valid"`
	Reason   string `json:"reason"`
	// Enabled and MaxConcurrent echo the setting the daemon last received.
	Enabled       bool `json:"enabled"`
	MaxConcurrent int  `json:"max_concurrent"`
}

// AGRuntimeSettings is the per-runtime setting the UI stores in
// agent_runtime.metadata under "ag_settings" and the heartbeat ack returns to
// the daemon. A daemon that gets none treats the backup account as disabled.
type AGRuntimeSettings struct {
	BackupEnabled       bool `json:"backup_enabled"`
	BackupMaxConcurrent int  `json:"backup_max_concurrent"`
}

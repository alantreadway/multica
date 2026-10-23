package daemon

// AG patch: Claude quota state file.
//
// <~/.multica>/ag_quota.json sits beside the slot dir, so every daemon on the
// machine (a handoff pair included) sees the same quota state and a hold
// survives a restart. Writes are atomic (temp file + rename). Reads happen when
// the file's mtime or size changed, once per claim cycle. Merging is per
// account: each window by newest observed_at, the account status by newest
// observed_at, the hold by newest set_at, and a `quota clear` override by
// newest cleared_at; a hold only counts when it was set after the last clear.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	agQuotaFileName    = "ag_quota.json"
	agQuotaFileVersion = 1
)

type agQuotaWindow struct {
	// Utilization is nil when the event did not carry one: the window does not
	// constrain anything.
	Utilization *float64  `json:"utilization,omitempty"`
	ResetsAt    time.Time `json:"resets_at,omitzero"`
	ObservedAt  time.Time `json:"observed_at"`
}

type agQuotaHold struct {
	Window string    `json:"window,omitempty"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"` // agQuotaReasonRejected | agQuotaReasonResetUnknown
	SetAt  time.Time `json:"set_at"`
}

const (
	agQuotaReasonRejected     = "rejected"
	agQuotaReasonResetUnknown = "reset_unknown"
)

type agQuotaAccount struct {
	Windows      map[string]agQuotaWindow `json:"windows,omitempty"`
	Status       string                   `json:"status,omitempty"`
	StatusWindow string                   `json:"status_window,omitempty"`
	// ObservedAt is the last time any run reported on this account.
	ObservedAt time.Time    `json:"observed_at"`
	Hold       *agQuotaHold `json:"hold,omitempty"`
	ClearedAt  time.Time    `json:"cleared_at,omitzero"`
}

type agQuotaFile struct {
	Version  int                        `json:"version"`
	Accounts map[string]*agQuotaAccount `json:"accounts"`
}

// activeHold returns the hold if it is still in force at now.
func (a *agQuotaAccount) activeHold(now time.Time) *agQuotaHold {
	if a == nil || a.Hold == nil {
		return nil
	}
	if !now.Before(a.Hold.Until) || !a.Hold.SetAt.After(a.ClearedAt) {
		return nil
	}
	return a.Hold
}

// endHold records a clear that is newer than the hold it ends.
func (a *agQuotaAccount) endHold(now time.Time) {
	a.ClearedAt = now
	if a.Hold != nil && !a.ClearedAt.After(a.Hold.SetAt) {
		a.ClearedAt = a.Hold.SetAt.Add(time.Nanosecond)
	}
}

// merge folds src into a, newest observation winning field by field.
func (a *agQuotaAccount) merge(src *agQuotaAccount) {
	if src == nil {
		return
	}
	for name, w := range src.Windows {
		if cur, ok := a.Windows[name]; !ok || w.ObservedAt.After(cur.ObservedAt) {
			if a.Windows == nil {
				a.Windows = map[string]agQuotaWindow{}
			}
			a.Windows[name] = w
		}
	}
	if src.ObservedAt.After(a.ObservedAt) {
		a.ObservedAt = src.ObservedAt
		a.Status = src.Status
		a.StatusWindow = src.StatusWindow
	}
	if src.Hold != nil && (a.Hold == nil || src.Hold.SetAt.After(a.Hold.SetAt)) {
		hold := *src.Hold
		a.Hold = &hold
	}
	if src.ClearedAt.After(a.ClearedAt) {
		a.ClearedAt = src.ClearedAt
	}
}

func agMergeQuotaAccounts(dst map[string]*agQuotaAccount, src map[string]*agQuotaAccount) {
	for key, acct := range src {
		cur := dst[key]
		if cur == nil {
			cur = &agQuotaAccount{}
			dst[key] = cur
		}
		cur.merge(acct)
	}
}

// agQuotaStatePath is the state file location; the state's path field is a
// test override.
func (d *Daemon) agQuotaStatePath() (string, error) {
	if d.agQuota.path != "" {
		return d.agQuota.path, nil
	}
	root, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, agQuotaFileName), nil
}

// agQuotaLoadFile reads and decodes the state file. A missing file is not an
// error (found=false).
func agQuotaLoadFile(path string) (f agQuotaFile, info os.FileInfo, found bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return agQuotaFile{}, nil, false, nil
	}
	if err != nil {
		return agQuotaFile{}, nil, true, err
	}
	// Stat after the read: a write that lands in between is then re-read next time.
	info, _ = os.Stat(path)
	if err := json.Unmarshal(raw, &f); err != nil {
		return agQuotaFile{}, info, true, fmt.Errorf("decode %s: %w", path, err)
	}
	return f, info, true, nil
}

// agQuotaRefreshLocked merges the state file into memory when it changed.
// s.mu must be held. Any failure leaves the in-memory state standing (open).
func (d *Daemon) agQuotaRefreshLocked() {
	s := &d.agQuota
	path, err := d.agQuotaStatePath()
	if err != nil {
		d.agQuotaWarnLocked("path", "quota state: resolve state file failed; running on in-memory state", "error", err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			d.agQuotaWarnLocked("read", "quota state: stat failed; running on in-memory state", "error", err)
		}
		return
	}
	if info.ModTime().Equal(s.lastMtime) && info.Size() == s.lastSize {
		return
	}
	f, info, _, err := agQuotaLoadFile(path)
	if err != nil {
		d.agQuotaWarnLocked("read", "quota state: state file unreadable; running on in-memory state", "path", path, "error", err)
		return
	}
	if s.accounts == nil {
		s.accounts = map[string]*agQuotaAccount{}
	}
	agMergeQuotaAccounts(s.accounts, f.Accounts)
	if info != nil {
		s.lastMtime, s.lastSize = info.ModTime(), info.Size()
	}
}

// agQuotaFlushLocked writes the in-memory state if it changed. Unforced writes
// are spaced agQuotaWriteInterval apart; the next claim cycle flushes whatever
// was held back. s.mu must be held.
func (d *Daemon) agQuotaFlushLocked(force bool) {
	s := &d.agQuota
	if !s.dirty {
		return
	}
	now := d.agQuotaNow()
	if !force && now.Sub(s.lastWrite) < agQuotaWriteInterval {
		return
	}
	path, err := d.agQuotaStatePath()
	if err != nil {
		d.agQuotaWarnLocked("path", "quota state: resolve state file failed; running on in-memory state", "error", err)
		return
	}
	// Merge what is on disk first, so a write never drops another daemon's
	// newer observations. A corrupt file is replaced.
	merged := map[string]*agQuotaAccount{}
	if f, _, _, err := agQuotaLoadFile(path); err == nil {
		agMergeQuotaAccounts(merged, f.Accounts)
	}
	agMergeQuotaAccounts(merged, s.accounts)
	s.accounts = merged

	if err := agQuotaWriteFile(path, agQuotaFile{Version: agQuotaFileVersion, Accounts: merged}); err != nil {
		d.agQuotaWarnLocked("write", "quota state: write failed; running on in-memory state", "path", path, "error", err)
		return
	}
	s.dirty = false
	s.lastWrite = now
	if info, err := os.Stat(path); err == nil {
		s.lastMtime, s.lastSize = info.ModTime(), info.Size()
	}
}

func agQuotaWriteFile(path string, f agQuotaFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, agQuotaFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

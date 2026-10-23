package daemonws

// AG patch: Claude quota report and runtime backup setting. The runtime lease
// caches the stored quota report and setting so a WebSocket heartbeat writes
// metadata only when the report changes and can ack the setting without a read.
// See ag-patches/PATCHES.md.

import "github.com/multica-ai/multica/server/internal/agquota"

// AGQuota and SetAGQuota hold the AG patch's quota memo (see package agquota).
func (l *RuntimeLease) AGQuota() agquota.Memo {
	if l == nil {
		return agquota.Memo{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agQuota
}

func (l *RuntimeLease) SetAGQuota(m agquota.Memo) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.agQuota = m
	l.mu.Unlock()
}

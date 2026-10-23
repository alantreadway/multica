package daemonws

// AG patch: drain badge. The runtime lease caches the stored drain state so a
// WebSocket heartbeat writes metadata only when the report changes. See
// ag-patches/PATCHES.md.

import "github.com/multica-ai/multica/server/internal/agdrain"

// AGDrain and SetAGDrain hold the AG patch's drain memo (see package agdrain).
func (l *RuntimeLease) AGDrain() agdrain.Memo {
	if l == nil {
		return agdrain.Memo{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agDrain
}

func (l *RuntimeLease) SetAGDrain(m agdrain.Memo) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.agDrain = m
	l.mu.Unlock()
}

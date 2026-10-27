package daemon

// AG patch: live token rate. Runs whose backend streams usage (Claude, Codex)
// feed each increase into a per-runtime 60s window; every heartbeat of such a
// runtime carries the window's average as tokens per second. See
// pkg/agent/ag_usage.go for where the increases come from.

import (
	"math"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const agTokenRateWindow = 60 * time.Second

// agTokenRateNow is swapped in tests.
var agTokenRateNow = time.Now

type agTokenSample struct {
	at time.Time
	u  agent.TokenUsage
}

type agTokenRateRuntime struct {
	active  int
	samples []agTokenSample
}

type agTokenRateState struct {
	mu   sync.Mutex
	byRT map[string]*agTokenRateRuntime
}

func agTokenRateProviderSupported(provider string) bool {
	family, _ := agent.RuntimeProtocolFamily(provider)
	return family == "claude" || family == "codex"
}

// runtimeLocked returns the runtime's entry, creating it. Caller holds mu.
func (s *agTokenRateState) runtimeLocked(runtimeID string) *agTokenRateRuntime {
	if s.byRT == nil {
		s.byRT = make(map[string]*agTokenRateRuntime)
	}
	rt := s.byRT[runtimeID]
	if rt == nil {
		rt = &agTokenRateRuntime{}
		s.byRT[runtimeID] = rt
	}
	return rt
}

// pruneLocked drops samples older than the window. Caller holds mu.
func (rt *agTokenRateRuntime) pruneLocked(now time.Time) {
	cut := 0
	for cut < len(rt.samples) && now.Sub(rt.samples[cut].at) > agTokenRateWindow {
		cut++
	}
	rt.samples = rt.samples[cut:]
}

// agTokenRateWire sets the run's usage observer on opts and returns the
// function that ends the run's count; runTask defers it. A run on a provider
// that does not stream usage, or without a runtime, is left alone.
func (d *Daemon) agTokenRateWire(opts *agent.ExecOptions, task Task, provider string) func() {
	if task.RuntimeID == "" || !agTokenRateProviderSupported(provider) {
		return func() {}
	}
	s := &d.agTokens
	s.mu.Lock()
	s.runtimeLocked(task.RuntimeID).active++
	s.mu.Unlock()
	opts.AGOnUsage = func(u agent.TokenUsage) {
		now := agTokenRateNow()
		s.mu.Lock()
		defer s.mu.Unlock()
		rt := s.runtimeLocked(task.RuntimeID)
		rt.pruneLocked(now)
		rt.samples = append(rt.samples, agTokenSample{at: now, u: u})
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if rt := s.byRT[task.RuntimeID]; rt != nil && rt.active > 0 {
				rt.active--
			}
		})
	}
}

// agTokenRateReport builds the report for one heartbeat, or nil for a runtime
// whose provider does not stream usage.
func (d *Daemon) agTokenRateReport(runtimeID string) *protocol.DaemonTokenRateReport {
	rt := d.findRuntime(runtimeID)
	if rt == nil || !agTokenRateProviderSupported(rt.Provider) {
		return nil
	}
	return d.agTokens.report(runtimeID, agTokenRateNow())
}

func (s *agTokenRateState) report(runtimeID string, now time.Time) *protocol.DaemonTokenRateReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	report := &protocol.DaemonTokenRateReport{WindowSeconds: int(agTokenRateWindow / time.Second)}
	rt := s.byRT[runtimeID]
	if rt == nil {
		return report
	}
	rt.pruneLocked(now)
	var sum agent.TokenUsage
	for _, sample := range rt.samples {
		sum.InputTokens += sample.u.InputTokens
		sum.CacheWriteTokens += sample.u.CacheWriteTokens
		sum.CacheReadTokens += sample.u.CacheReadTokens
		sum.OutputTokens += sample.u.OutputTokens
	}
	window := agTokenRateWindow.Seconds()
	report.InputPerSec = agTokenRateRound(float64(sum.InputTokens+sum.CacheWriteTokens) / window)
	report.OutputPerSec = agTokenRateRound(float64(sum.OutputTokens) / window)
	report.CacheReadPerSec = agTokenRateRound(float64(sum.CacheReadTokens) / window)
	report.ActiveRuns = rt.active
	if rt.active == 0 && len(rt.samples) == 0 {
		delete(s.byRT, runtimeID)
	}
	return report
}

func agTokenRateRound(v float64) float64 {
	return math.Round(v*10) / 10
}

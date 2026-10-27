package agtokenrate

// AG patch: live token rate; TestAGPatch* as counted by ag-patches/test.sh.

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchTokenRateBoard(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	busy := &protocol.DaemonTokenRateReport{InputPerSec: 12.5, OutputPerSec: 80, CacheReadPerSec: 900, WindowSeconds: 60, ActiveRuns: 1}
	idle := &protocol.DaemonTokenRateReport{WindowSeconds: 60}
	waiting := &protocol.DaemonTokenRateReport{WindowSeconds: 60, ActiveRuns: 1}

	t.Run("AG patch: busy reports publish, the first idle one after publishes, idle-to-idle does not", func(t *testing.T) {
		b := &Board{}
		steps := []struct {
			report *protocol.DaemonTokenRateReport
			at     time.Duration
			want   bool
		}{
			{idle, 0, false},
			{busy, 15 * time.Second, true},
			{busy, 30 * time.Second, true},
			{idle, 45 * time.Second, true},
			{idle, 60 * time.Second, false},
			// A run waiting on a tool reports zero tokens but is still busy.
			{waiting, 75 * time.Second, true},
		}
		for i, s := range steps {
			if _, got := b.Apply("rt-1", "ws-1", s.report, t0.Add(s.at)); got != s.want {
				t.Fatalf("step %d: publish = %v, want %v", i, got, s.want)
			}
		}
	})

	t.Run("AG patch: list returns the workspace's rates within TTL", func(t *testing.T) {
		b := &Board{}
		b.Apply("rt-1", "ws-1", busy, t0)
		b.Apply("rt-2", "ws-1", idle, t0.Add(-TTL-time.Second))
		b.Apply("rt-3", "ws-2", busy, t0)
		got := b.List("ws-1", t0.Add(time.Second))
		if len(got) != 1 || got[0].RuntimeID != "rt-1" || got[0].WorkspaceID != "ws-1" || got[0].OutputPerSec != 80 || got[0].ObservedAt != "2026-10-27T09:00:00Z" {
			t.Fatalf("list = %+v", got)
		}
		if got := b.List("ws-1", t0.Add(TTL+time.Second)); len(got) != 0 {
			t.Fatalf("stale list = %+v", got)
		}
	})

	t.Run("AG patch: malformed reports are rejected", func(t *testing.T) {
		for _, r := range []protocol.DaemonTokenRateReport{
			{InputPerSec: -1, WindowSeconds: 60},
			{OutputPerSec: math.NaN(), WindowSeconds: 60},
			{CacheReadPerSec: math.Inf(1), WindowSeconds: 60},
			{WindowSeconds: 0},
			{WindowSeconds: 60, ActiveRuns: -1},
		} {
			if Valid(&r) {
				t.Errorf("%+v accepted", r)
			}
		}
		if !Valid(busy) {
			t.Error("busy report rejected")
		}
	})

	t.Run("AG patch: the report rides in the context", func(t *testing.T) {
		if ReportFrom(context.Background()) != nil || ReportFrom(WithReport(context.Background(), nil)) != nil {
			t.Fatal("empty context carries a report")
		}
		if got := ReportFrom(WithReport(context.Background(), busy)); got != busy {
			t.Fatalf("report = %+v", got)
		}
	})
}

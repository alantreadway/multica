package daemon

// AG patch: live token rate; TestAGPatch* as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func agTokenRateClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	prev := agTokenRateNow
	agTokenRateNow = func() time.Time { return now }
	t.Cleanup(func() { agTokenRateNow = prev })
	return &now
}

func TestAGPatchTokenRateWindow(t *testing.T) {
	t.Run("AG patch: a run's increases average over 60s, cache writes count as input", func(t *testing.T) {
		now := agTokenRateClock(t)
		f := newAGPaceFixture(t, 5)
		var opts agent.ExecOptions
		done := f.d.agTokenRateWire(&opts, Task{ID: "t1", RuntimeID: "rt-claude"}, "claude")
		defer done()
		if opts.AGOnUsage == nil {
			t.Fatal("no observer on a Claude run")
		}
		opts.AGOnUsage(agent.TokenUsage{InputTokens: 60, CacheWriteTokens: 540, CacheReadTokens: 6000})
		*now = now.Add(10 * time.Second)
		opts.AGOnUsage(agent.TokenUsage{OutputTokens: 3000})

		got := f.d.agTokenRateReport("rt-claude")
		want := &protocol.DaemonTokenRateReport{InputPerSec: 10, OutputPerSec: 50, CacheReadPerSec: 100, WindowSeconds: 60, ActiveRuns: 1}
		if got == nil || *got != *want {
			t.Fatalf("report = %+v, want %+v", got, want)
		}
	})

	t.Run("AG patch: samples older than the window drop out", func(t *testing.T) {
		now := agTokenRateClock(t)
		f := newAGPaceFixture(t, 5)
		var opts agent.ExecOptions
		defer f.d.agTokenRateWire(&opts, Task{ID: "t1", RuntimeID: "rt-codex"}, "codex")()
		opts.AGOnUsage(agent.TokenUsage{OutputTokens: 600})
		*now = now.Add(61 * time.Second)
		opts.AGOnUsage(agent.TokenUsage{OutputTokens: 60})
		if got := f.d.agTokenRateReport("rt-codex"); got == nil || got.OutputPerSec != 1 || got.ActiveRuns != 1 {
			t.Fatalf("report = %+v, want 1 tok/s from the recent sample only", got)
		}
	})

	t.Run("AG patch: two runs on one runtime add up; a finished run stops counting as active", func(t *testing.T) {
		agTokenRateClock(t)
		f := newAGPaceFixture(t, 5)
		var a, b agent.ExecOptions
		doneA := f.d.agTokenRateWire(&a, Task{ID: "a", RuntimeID: "rt-claude"}, "claude")
		doneB := f.d.agTokenRateWire(&b, Task{ID: "b", RuntimeID: "rt-claude"}, "claude")
		a.AGOnUsage(agent.TokenUsage{OutputTokens: 120})
		b.AGOnUsage(agent.TokenUsage{OutputTokens: 60})
		doneA()
		doneA() // safe to call twice
		got := f.d.agTokenRateReport("rt-claude")
		if got.OutputPerSec != 3 || got.ActiveRuns != 1 {
			t.Fatalf("report = %+v, want 3 tok/s and one active run", got)
		}
		doneB()
	})

	t.Run("AG patch: an idle supported runtime reports zero; others report nothing", func(t *testing.T) {
		agTokenRateClock(t)
		f := newAGPaceFixture(t, 5)
		if got := f.d.agTokenRateReport("rt-claude"); got == nil || *got != (protocol.DaemonTokenRateReport{WindowSeconds: 60}) {
			t.Fatalf("idle Claude report = %+v", got)
		}
		if got := f.d.agTokenRateReport("rt-unknown"); got != nil {
			t.Fatalf("unknown runtime report = %+v, want nil", got)
		}
	})

	t.Run("AG patch: a provider without live usage, or a task without a runtime, gets no observer", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		for _, tc := range []struct {
			task     Task
			provider string
		}{{Task{ID: "g", RuntimeID: "rt-claude"}, "gemini"}, {Task{ID: "n"}, "claude"}} {
			var opts agent.ExecOptions
			f.d.agTokenRateWire(&opts, tc.task, tc.provider)()
			if opts.AGOnUsage != nil {
				t.Errorf("%s/%q: observer set", tc.provider, tc.task.RuntimeID)
			}
		}
	})
}

func TestAGPatchTokenRateHeartbeat(t *testing.T) {
	t.Run("AG patch: the HTTP heartbeat carries ag_token_rate for supported runtimes only", func(t *testing.T) {
		agTokenRateClock(t)
		srv := newAGReportServer(t, `{"status":"ok"}`)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		var opts agent.ExecOptions
		defer f.d.agTokenRateWire(&opts, Task{ID: "t1", RuntimeID: "rt-codex"}, "codex")()
		opts.AGOnUsage(agent.TokenUsage{OutputTokens: 600})

		f.d.runHeartbeatTick(context.Background(), "rt-codex")
		rate, ok := srv.lastBody()["ag_token_rate"].(map[string]any)
		if !ok || rate["output_per_sec"] != float64(10) || rate["active_runs"] != float64(1) {
			t.Fatalf("Codex heartbeat body = %v", srv.lastBody())
		}
		if _, err := f.d.client.SendHeartbeat(context.Background(), "rt-codex"); err != nil {
			t.Fatal(err)
		}
		if _, present := srv.lastBody()["ag_token_rate"]; present {
			t.Fatalf("plain heartbeat carries ag_token_rate: %v", srv.lastBody())
		}
	})

	t.Run("AG patch: the WebSocket heartbeat frames carry ag_token_rate", func(t *testing.T) {
		agTokenRateClock(t)
		f := newAGPaceFixture(t, 5)
		writes := make(chan *wsOutbound, 4)
		f.d.sendWSHeartbeats(context.Background(), []string{"rt-claude", "rt-unknown"}, writes)
		for _, want := range []struct {
			id   string
			rate bool
		}{{"rt-claude", true}, {"rt-unknown", false}} {
			out := <-writes
			var msg protocol.Message
			var payload protocol.DaemonHeartbeatRequestPayload
			if err := json.Unmarshal(out.data, &msg); err != nil || json.Unmarshal(msg.Payload, &payload) != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if payload.RuntimeID != want.id || (payload.AGTokenRate != nil) != want.rate {
				t.Fatalf("%s frame: ag_token_rate present = %v, want %v", payload.RuntimeID, payload.AGTokenRate != nil, want.rate)
			}
		}
	})
}

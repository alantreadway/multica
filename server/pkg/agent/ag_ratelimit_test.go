package agent

// AG patch: rate_limit_event parsing and the claude stream hook; TestAGPatch*
// as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAGPatchRateLimitParse(t *testing.T) {
	const resets = 1792272600

	t.Run("AG patch: spec sample with both windows", func(t *testing.T) {
		ev, err := AGParseRateLimitEvent([]byte(`{"status":"allowed_warning","rateLimitType":"seven_day","utilization":0.8,"surpassedThreshold":0.75,"unifiedWindows":{"five_hour":{"utilization":0.21,"resetsAt":1792272600},"seven_day":{"utilization":0.8,"resetsAt":1792821600}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Status != "allowed_warning" || ev.RateLimitType != "seven_day" {
			t.Fatalf("status/type = %q/%q", ev.Status, ev.RateLimitType)
		}
		if ev.Utilization == nil || *ev.Utilization != 0.8 || ev.SurpassedThreshold == nil || *ev.SurpassedThreshold != 0.75 {
			t.Fatalf("utilization/threshold = %v/%v", ev.Utilization, ev.SurpassedThreshold)
		}
		five := ev.Windows["five_hour"]
		if five.Utilization == nil || *five.Utilization != 0.21 || five.ResetsAt.Unix() != resets {
			t.Fatalf("five_hour = %+v", five)
		}
		if seven := ev.Windows["seven_day"]; seven.ResetsAt.Unix() != 1792821600 {
			t.Fatalf("seven_day = %+v", seven)
		}
	})

	t.Run("AG patch: missing utilization leaves it nil, status and resetsAt still read", func(t *testing.T) {
		ev, err := AGParseRateLimitEvent([]byte(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600,"unifiedWindows":{"five_hour":{"resetsAt":1792272600}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Utilization != nil || ev.Windows["five_hour"].Utilization != nil {
			t.Fatalf("utilization should be nil: %+v", ev)
		}
		if ev.Status != "rejected" || ev.ResetsAt.Unix() != resets || ev.Windows["five_hour"].ResetsAt.Unix() != resets {
			t.Fatalf("status/resetsAt lost: %+v", ev)
		}
	})

	t.Run("AG patch: resetsAt in seconds, milliseconds, fractional seconds and RFC 3339 agree", func(t *testing.T) {
		for name, raw := range map[string]string{
			"seconds":      `1792272600`,
			"milliseconds": `1792272600000`,
			"fractional":   `1792272600.25`,
			"rfc3339":      `"2026-10-17T21:30:00Z"`,
		} {
			ev, err := AGParseRateLimitEvent([]byte(`{"status":"rejected","resetsAt":` + raw + `}`))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if ev.ResetsAt.Unix() != resets {
				t.Errorf("%s: resetsAt = %s, want unix %d", name, ev.ResetsAt, resets)
			}
		}
		for _, bad := range []string{`"soon"`, `-5`, `0`, `null`, `true`} {
			ev, err := AGParseRateLimitEvent([]byte(`{"status":"rejected","resetsAt":` + bad + `}`))
			if err != nil {
				t.Fatalf("resetsAt %s: %v", bad, err)
			}
			if !ev.ResetsAt.IsZero() {
				t.Errorf("resetsAt %s = %s, want the zero time", bad, ev.ResetsAt)
			}
		}
	})

	t.Run("AG patch: top-level fields and the nested rate_limit_info form both parse", func(t *testing.T) {
		top, err := AGParseRateLimitEvent([]byte(`{"type":"rate_limit_event","status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`))
		if err != nil {
			t.Fatal(err)
		}
		nested, err := AGParseRateLimitEvent([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}}`))
		if err != nil {
			t.Fatal(err)
		}
		if top.Status != "rejected" || top.RateLimitType != "five_hour" || top.ResetsAt.Unix() != resets {
			t.Fatalf("top-level form = %+v", top)
		}
		if nested.Status != top.Status || nested.RateLimitType != top.RateLimitType || !nested.ResetsAt.Equal(top.ResetsAt) {
			t.Fatalf("nested %+v != top-level %+v", nested, top)
		}
	})

	t.Run("AG patch: garbled, truncated and empty payloads are errors", func(t *testing.T) {
		for name, raw := range map[string]string{
			"not json":       `rate limit`,
			"cut mid-line":   `{"type":"rate_limit_event","rate_limit_info":{"status":"rej`,
			"wrong type":     `{"status":7}`,
			"nested garbage": `{"rate_limit_info":"nope"}`,
			"empty object":   `{}`,
			"not an object":  `[1,2]`,
			"empty":          ``,
		} {
			if _, err := AGParseRateLimitEvent([]byte(raw)); err == nil {
				t.Errorf("%s: parsed without error", name)
			}
		}
	})
}

// agRunClaudeStream runs the claude backend against a fake executable that
// replays frames, collecting what the rate-limit hook received.
func agRunClaudeStream(t *testing.T, frames string, hook bool) (Result, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fixturePath := filepath.Join(t.TempDir(), "frames.jsonl")
	if err := os.WriteFile(fixturePath, []byte(frames), 0o600); err != nil {
		t.Fatal(err)
	}
	fakePath := filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, fakePath, []byte("#!/bin/sh\nIFS= read -r _\ncat "+fixturePath+"\n"))
	backend, err := New("claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	opts := ExecOptions{Timeout: 5 * time.Second}
	if hook {
		opts.AGOnRateLimit = func(raw json.RawMessage) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, string(raw))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "prompt", opts)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		mu.Lock()
		defer mu.Unlock()
		return result, append([]string(nil), got...)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
		return Result{}, nil
	}
}

func TestAGPatchRateLimitClaudeStreamHook(t *testing.T) {
	t.Run("AG patch: a captured allowed event reaches the hook with per-window utilization", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-2.1.299-rate-limit-allowed.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		result, got := agRunClaudeStream(t, string(frames), true)
		if result.Status != "completed" {
			t.Fatalf("status = %q (%s)", result.Status, result.Error)
		}
		if len(got) != 1 {
			t.Fatalf("hook calls = %d, want 1", len(got))
		}
		ev, err := AGParseRateLimitEvent([]byte(got[0]))
		if err != nil {
			t.Fatal(err)
		}
		// The risk the spec called out: a plain "allowed" event carries utilization.
		if ev.Status != "allowed" || ev.RateLimitType != "five_hour" {
			t.Fatalf("event = %+v", ev)
		}
		if u := ev.Windows["five_hour"].Utilization; u == nil || *u != 0.07 {
			t.Fatalf("five_hour utilization = %v, want 0.07", u)
		}
		if u := ev.Windows["seven_day"].Utilization; u == nil || *u != 0.02 {
			t.Fatalf("seven_day utilization = %v, want 0.02", u)
		}
	})

	t.Run("AG patch: a rejected event reaches the hook", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-rate-limit-rejected.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		_, got := agRunClaudeStream(t, string(frames), true)
		if len(got) != 1 {
			t.Fatalf("hook calls = %d, want 1", len(got))
		}
		ev, err := AGParseRateLimitEvent([]byte(got[0]))
		if err != nil || ev.Status != "rejected" || ev.Windows["five_hour"].ResetsAt.Unix() != 1792272600 {
			t.Fatalf("event = %+v, err = %v", ev, err)
		}
	})

	t.Run("AG patch: an event cut mid-line reaches the hook as unparseable, and the run carries on", func(t *testing.T) {
		frames := `{"type":"system","subtype":"init","session_id":"s1"}` + "\n" +
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rej` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}` + "\n"
		result, got := agRunClaudeStream(t, frames, true)
		if result.Status != "completed" {
			t.Fatalf("status = %q (%s)", result.Status, result.Error)
		}
		if len(got) != 1 {
			t.Fatalf("hook calls = %d, want 1", len(got))
		}
		if _, err := AGParseRateLimitEvent([]byte(got[0])); err == nil {
			t.Fatalf("truncated payload %q parsed", got[0])
		}
	})

	t.Run("AG patch: other garbled lines do not reach the hook", func(t *testing.T) {
		frames := `{"type":"system","subtype":"init","session_id":"s1"}` + "\n" +
			`this is not json` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}` + "\n"
		_, got := agRunClaudeStream(t, frames, true)
		if len(got) != 0 {
			t.Fatalf("hook calls = %v, want none", got)
		}
	})

	t.Run("AG patch: no hook set, events are ignored as before", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-rate-limit-rejected.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		result, got := agRunClaudeStream(t, string(frames), false)
		if len(got) != 0 || !strings.Contains(result.Error, "5-hour") {
			t.Fatalf("got=%v result=%+v", got, result)
		}
	})
}

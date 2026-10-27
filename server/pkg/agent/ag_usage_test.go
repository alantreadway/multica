package agent

// AG patch: live token rate hooks; TestAGPatch* as counted by ag-patches/test.sh.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// agRunClaudeUsageStream runs the fake claude over frames and returns the
// result, every AGOnUsage increase, and the args claude was started with.
func agRunClaudeUsageStream(t *testing.T, frames string, hook bool) (Result, []TokenUsage, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "frames.jsonl")
	argsPath := filepath.Join(dir, "args")
	if err := os.WriteFile(fixturePath, []byte(frames), 0o600); err != nil {
		t.Fatal(err)
	}
	fakePath := filepath.Join(dir, "claude")
	writeTestExecutable(t, fakePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsPath+"\nIFS= read -r _\ncat "+fixturePath+"\n"))
	backend, err := New("claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []TokenUsage
	opts := ExecOptions{Timeout: 5 * time.Second}
	if hook {
		opts.AGOnUsage = func(u TokenUsage) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, u)
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
		rawArgs, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return result, append([]TokenUsage(nil), got...), strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
		return Result{}, nil, nil
	}
}

func agSumUsage(us []TokenUsage) TokenUsage {
	var sum TokenUsage
	for _, u := range us {
		sum.InputTokens += u.InputTokens
		sum.OutputTokens += u.OutputTokens
		sum.CacheReadTokens += u.CacheReadTokens
		sum.CacheWriteTokens += u.CacheWriteTokens
	}
	return sum
}

func TestAGPatchUsageClaudeStream(t *testing.T) {
	t.Run("AG patch: a captured partial-messages run reports increases that sum to the result usage", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-2.1.311-partial-usage.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		result, got, args := agRunClaudeUsageStream(t, string(frames), true)
		if result.Status != "completed" {
			t.Fatalf("status = %q (%s)", result.Status, result.Error)
		}
		if !slices.Contains(args, agClaudeIncludePartialMessages) {
			t.Fatalf("args %v lack %s", args, agClaudeIncludePartialMessages)
		}
		// Two API calls: start then delta for each, the placeholder output on
		// message_start (17, 8) never counted.
		want := []TokenUsage{
			{InputTokens: 2, CacheReadTokens: 22890},
			{OutputTokens: 77},
			{InputTokens: 2, CacheReadTokens: 24992},
			{OutputTokens: 2390},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("increases = %+v, want %+v", got, want)
		}
		// The result line's own usage: 4 in, 2467 out, 47882 cache read.
		if sum := agSumUsage(got); sum != (TokenUsage{InputTokens: 4, OutputTokens: 2467, CacheReadTokens: 47882}) {
			t.Fatalf("sum = %+v", sum)
		}
	})

	t.Run("AG patch: no hook, no partial-messages flag and no calls", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-2.1.311-partial-usage.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		result, got, args := agRunClaudeUsageStream(t, string(frames), false)
		if result.Status != "completed" || len(got) != 0 {
			t.Fatalf("status = %q, calls = %v", result.Status, got)
		}
		if slices.Contains(args, agClaudeIncludePartialMessages) {
			t.Fatalf("args %v carry %s without an observer", args, agClaudeIncludePartialMessages)
		}
	})

	t.Run("AG patch: the stream events do not change billed usage", func(t *testing.T) {
		frames, err := os.ReadFile("testdata/claude-code-2.1.311-partial-usage.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		with, _, _ := agRunClaudeUsageStream(t, string(frames), true)
		without, _, _ := agRunClaudeUsageStream(t, string(frames), false)
		if len(with.Usage) == 0 || !mapsEqualUsage(with.Usage, without.Usage) {
			t.Fatalf("usage with hook %+v, without %+v", with.Usage, without.Usage)
		}
	})
}

func mapsEqualUsage(a, b map[string]TokenUsage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestAGPatchUsageClaudeTracker(t *testing.T) {
	observe := func(t *testing.T) (ExecOptions, *[]TokenUsage) {
		var got []TokenUsage
		return ExecOptions{AGOnUsage: func(u TokenUsage) { got = append(got, u) }}, &got
	}
	event := func(s string) json.RawMessage { return json.RawMessage(s) }

	t.Run("AG patch: repeated cumulative deltas report only the growth", func(t *testing.T) {
		opts, got := observe(t)
		var tr agClaudeUsageTracker
		tr.observe(opts, event(`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":5,"cache_creation_input_tokens":100,"output_tokens":3}}}`), "m1", "")
		tr.observe(opts, event(`{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":40}}`), "m1", "")
		tr.observe(opts, event(`{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":90}}`), "m1", "")
		tr.observe(opts, event(`{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":90}}`), "m1", "")
		want := []TokenUsage{{InputTokens: 5, CacheWriteTokens: 100}, {OutputTokens: 40}, {OutputTokens: 50}}
		if !slices.Equal(*got, want) {
			t.Fatalf("got %+v, want %+v", *got, want)
		}
	})

	t.Run("AG patch: subagent messages without ids do not collide with the main loop", func(t *testing.T) {
		opts, got := observe(t)
		var tr agClaudeUsageTracker
		tr.observe(opts, event(`{"type":"message_delta","usage":{"output_tokens":30}}`), "", "")
		tr.observe(opts, event(`{"type":"message_delta","usage":{"output_tokens":20}}`), "", "toolu_1")
		if sum := agSumUsage(*got); sum.OutputTokens != 50 {
			t.Fatalf("output = %d, want 50 (%+v)", sum.OutputTokens, *got)
		}
	})

	t.Run("AG patch: other and unreadable events are ignored", func(t *testing.T) {
		opts, got := observe(t)
		var tr agClaudeUsageTracker
		tr.observe(opts, event(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`), "m1", "")
		tr.observe(opts, event(`{"type":"message_start"}`), "m1", "")
		tr.observe(opts, event(`{"type":"message_de`), "m1", "")
		if len(*got) != 0 {
			t.Fatalf("got %+v", *got)
		}
	})
}

func TestAGPatchUsageCodex(t *testing.T) {
	t.Run("AG patch: thread token usage increases reach the observer once each", func(t *testing.T) {
		var got []TokenUsage
		c := &codexClient{agOnUsage: func(u TokenUsage) { got = append(got, u) }}
		c.setActiveTurnID("turn-current")
		first := codexThreadTokenUsageParams(
			"thread-1", "turn-current",
			map[string]any{"inputTokens": float64(100), "cachedInputTokens": float64(30), "outputTokens": float64(10)},
			map[string]any{"inputTokens": float64(100), "cachedInputTokens": float64(30), "outputTokens": float64(10)},
		)
		second := codexThreadTokenUsageParams(
			"thread-1", "turn-current",
			map[string]any{"inputTokens": float64(300), "cachedInputTokens": float64(200), "outputTokens": float64(60)},
			map[string]any{"inputTokens": float64(200), "cachedInputTokens": float64(170), "outputTokens": float64(50)},
		)
		c.updateThreadTokenUsage(first)
		c.updateThreadTokenUsage(first) // duplicate snapshot: no growth, no call
		c.updateThreadTokenUsage(second)
		want := []TokenUsage{
			{InputTokens: 70, OutputTokens: 10, CacheReadTokens: 30},
			{InputTokens: 30, OutputTokens: 50, CacheReadTokens: 170},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("AG patch: no observer, usage still accumulates", func(t *testing.T) {
		c := &codexClient{}
		c.setActiveTurnID("turn-current")
		c.updateThreadTokenUsage(codexThreadTokenUsageParams(
			"thread-1", "turn-current",
			map[string]any{"inputTokens": float64(100), "outputTokens": float64(10)},
			map[string]any{"inputTokens": float64(100), "outputTokens": float64(10)},
		))
		if c.usage != (TokenUsage{InputTokens: 100, OutputTokens: 10}) {
			t.Fatalf("usage = %+v", c.usage)
		}
	})
}

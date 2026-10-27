package agent

// AG patch: live token rate. Backends hand each increase in token usage to
// ExecOptions.AGOnUsage as it streams, so the daemon can report a per-runtime
// tokens-per-second rate on its heartbeat (internal/daemon/ag_token_rate.go).
// Billing still comes from Result.Usage; nothing here changes it.
//
// Codex: thread/tokenUsage/updated already yields a per-notification increase
// (codexClient.updateThreadTokenUsage), which is passed on as is.
//
// Claude: plain stream-json carries placeholder output_tokens on assistant
// messages and real totals only on the final result (verified against Claude
// Code 2.1.311). With --include-partial-messages, each API call's message_start
// carries its input/cache usage and message_delta its real output_tokens, and
// the deltas sum to the result total. Subagent calls are counted too.

import "encoding/json"

// agClaudeIncludePartialMessages is the flag that turns on Claude's
// stream_event lines; added only when a usage observer is set.
const agClaudeIncludePartialMessages = "--include-partial-messages"

type agClaudeStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string       `json:"id"`
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	Usage *claudeUsage `json:"usage"`
}

// agClaudeUsageTracker turns per-message cumulative usage into increases.
// message_start and message_delta both carry the message's usage so far, so
// each counter is reported only by how much it grew.
type agClaudeUsageTracker struct {
	seen map[string]TokenUsage
}

// observe handles one stream_event line's fields. apiMessageID is the outer
// api_message_id, used when the event itself has no message id (message_delta).
func (t *agClaudeUsageTracker) observe(opts ExecOptions, raw json.RawMessage, apiMessageID, parentToolUseID string) {
	if opts.AGOnUsage == nil || len(raw) == 0 {
		return
	}
	var ev agClaudeStreamEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	var u *claudeUsage
	id := apiMessageID
	countOutput := false
	switch ev.Type {
	case "message_start":
		if ev.Message == nil {
			return
		}
		u = ev.Message.Usage
		if ev.Message.ID != "" {
			id = ev.Message.ID
		}
	case "message_delta":
		u = ev.Usage
		countOutput = true
	default:
		return
	}
	if u == nil {
		return
	}
	if id == "" {
		// Without an id, key by the conversation the event belongs to so a
		// subagent's messages do not collide with the main loop's.
		id = "parent:" + parentToolUseID
	}
	if t.seen == nil {
		t.seen = make(map[string]TokenUsage)
	}
	prev := t.seen[id]
	next := TokenUsage{
		InputTokens:      max(prev.InputTokens, u.InputTokens),
		CacheReadTokens:  max(prev.CacheReadTokens, u.CacheReadInputTokens),
		CacheWriteTokens: max(prev.CacheWriteTokens, u.CacheCreationInputTokens),
		OutputTokens:     prev.OutputTokens,
	}
	// message_start's output_tokens is a placeholder; only the delta's is real.
	if countOutput {
		next.OutputTokens = max(prev.OutputTokens, u.OutputTokens)
	}
	t.seen[id] = next
	agEmitUsage(opts.AGOnUsage, TokenUsage{
		InputTokens:      next.InputTokens - prev.InputTokens,
		OutputTokens:     next.OutputTokens - prev.OutputTokens,
		CacheReadTokens:  next.CacheReadTokens - prev.CacheReadTokens,
		CacheWriteTokens: next.CacheWriteTokens - prev.CacheWriteTokens,
	})
}

// agEmitUsage calls the observer with a non-empty increase.
func agEmitUsage(observe func(TokenUsage), d TokenUsage) {
	if observe == nil {
		return
	}
	if d.InputTokens <= 0 && d.OutputTokens <= 0 && d.CacheReadTokens <= 0 && d.CacheWriteTokens <= 0 {
		return
	}
	observe(TokenUsage{
		InputTokens:      max(d.InputTokens, 0),
		OutputTokens:     max(d.OutputTokens, 0),
		CacheReadTokens:  max(d.CacheReadTokens, 0),
		CacheWriteTokens: max(d.CacheWriteTokens, 0),
	})
}

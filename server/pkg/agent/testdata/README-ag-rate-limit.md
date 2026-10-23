AG patch fixtures for rate_limit_event.

- `claude-code-2.1.299-rate-limit-allowed.jsonl`: captured from one headless run
  (`claude -p ... --output-format stream-json --verbose`, Claude Code 2.1.299,
  2026-10-17). Only the `rate_limit_event` frame is verbatim, with its `uuid` and
  `session_id` replaced; the init and result frames are minimal stand-ins.
  It shows that a plain `status: allowed` event does carry
  `unifiedWindows.<window>.utilization`.
- `claude-code-rate-limit-rejected.jsonl`: synthetic. Same shape with
  `status: rejected`; no rejected frame has been captured yet.

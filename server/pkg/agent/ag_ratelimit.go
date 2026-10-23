package agent

// AG patch: Claude quota tracking. Claude Code emits a rate_limit_event on
// stream-json for every headless run; upstream drops it. This file parses it
// into a typed value and hands the raw payload to ExecOptions.AGOnRateLimit.
// The daemon keeps the per-account state (internal/daemon/ag_quota.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AGRateLimitWindow is one entry of unifiedWindows.
type AGRateLimitWindow struct {
	// Utilization is nil when the event did not carry one, so the window does
	// not constrain anything.
	Utilization *float64
	// ResetsAt is the zero time when absent or unreadable.
	ResetsAt time.Time
}

// AGRateLimitEvent is a parsed rate_limit_info payload.
type AGRateLimitEvent struct {
	Status             string // allowed | allowed_warning | rejected | ...
	RateLimitType      string // the window the status refers to, e.g. five_hour
	Utilization        *float64
	SurpassedThreshold *float64
	ResetsAt           time.Time
	Windows            map[string]AGRateLimitWindow
}

type agRateLimitWire struct {
	Status             string                           `json:"status"`
	RateLimitType      string                           `json:"rateLimitType"`
	Utilization        *float64                         `json:"utilization"`
	SurpassedThreshold *float64                         `json:"surpassedThreshold"`
	ResetsAt           json.RawMessage                  `json:"resetsAt"`
	UnifiedWindows     map[string]agRateLimitWireWindow `json:"unifiedWindows"`
	RateLimitInfo      json.RawMessage                  `json:"rate_limit_info"`
}

type agRateLimitWireWindow struct {
	Utilization *float64        `json:"utilization"`
	ResetsAt    json.RawMessage `json:"resetsAt"`
}

// AGParseRateLimitEvent parses the payload handed to AGOnRateLimit: either the
// rate_limit_info object or the whole stream line (fields at the top level, or
// nested under rate_limit_info). Any error means the event is unusable.
func AGParseRateLimitEvent(raw []byte) (AGRateLimitEvent, error) {
	var wire agRateLimitWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return AGRateLimitEvent{}, fmt.Errorf("rate limit event: %w", err)
	}
	if len(bytes.TrimSpace(wire.RateLimitInfo)) > 0 && string(bytes.TrimSpace(wire.RateLimitInfo)) != "null" {
		var inner agRateLimitWire
		if err := json.Unmarshal(wire.RateLimitInfo, &inner); err != nil {
			return AGRateLimitEvent{}, fmt.Errorf("rate limit event: rate_limit_info: %w", err)
		}
		wire = inner
	}

	ev := AGRateLimitEvent{
		Status:             strings.TrimSpace(wire.Status),
		RateLimitType:      strings.TrimSpace(wire.RateLimitType),
		Utilization:        finiteOrNil(wire.Utilization),
		SurpassedThreshold: finiteOrNil(wire.SurpassedThreshold),
		ResetsAt:           agParseResetsAt(wire.ResetsAt),
	}
	if ev.Status == "" && ev.RateLimitType == "" && len(wire.UnifiedWindows) == 0 {
		return AGRateLimitEvent{}, fmt.Errorf("rate limit event: no status, rateLimitType or windows")
	}
	if len(wire.UnifiedWindows) > 0 {
		ev.Windows = make(map[string]AGRateLimitWindow, len(wire.UnifiedWindows))
		for name, w := range wire.UnifiedWindows {
			ev.Windows[name] = AGRateLimitWindow{
				Utilization: finiteOrNil(w.Utilization),
				ResetsAt:    agParseResetsAt(w.ResetsAt),
			}
		}
	}
	return ev, nil
}

func finiteOrNil(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	return v
}

// agParseResetsAt reads resetsAt as epoch seconds or milliseconds (a value
// above 1e12 is milliseconds) or an RFC 3339 string. Anything else is the zero
// time, which callers treat as unknown.
func agParseResetsAt(raw json.RawMessage) time.Time {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return time.Time{}
		}
		if t, err := time.Parse(time.RFC3339, str); err == nil {
			return t.UTC()
		}
		s = str
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return time.Time{}
	}
	if n > 1e12 {
		return time.UnixMilli(int64(n)).UTC()
	}
	return time.Unix(int64(n), int64((n-math.Floor(n))*1e9)).UTC()
}

// agRateLimit hands a rate_limit_event to the run's observer. info is the
// rate_limit_info field; when it is absent the whole line is passed so the
// parser can read top-level fields.
func agRateLimit(opts ExecOptions, info json.RawMessage, line string) {
	if opts.AGOnRateLimit == nil {
		return
	}
	if len(bytes.TrimSpace(info)) > 0 {
		opts.AGOnRateLimit(info)
		return
	}
	opts.AGOnRateLimit(json.RawMessage(line))
}

// agRateLimitGarbled passes a stream line that failed to parse to the observer
// when it looks like a rate_limit_event, so it is counted rather than lost. The
// observer's parse of it fails.
func agRateLimitGarbled(opts ExecOptions, line string) {
	if opts.AGOnRateLimit == nil || !strings.Contains(line, "rate_limit_event") {
		return
	}
	opts.AGOnRateLimit(json.RawMessage(line))
}

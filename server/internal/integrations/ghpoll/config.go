// Package ghpoll polls the GitHub REST API for pull requests in the
// repositories a workspace lists, for deployments with no GitHub App (and so no
// webhooks). It is the AG fork's token-mode replacement for the webhook
// doorbell: every changed PR is mirrored the way a pull_request webhook would
// be, then handed to the ghsnapshot pipeline for its CI and mergeability state.
//
// The package knows nothing about the handler or the database schema beyond
// the Source and Sink seams, so it is tested against an httptest GitHub.
//
// The poll token is an opaque secret: it is never logged and never put in an
// error.
package ghpoll

import (
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInterval is the poll period when GITHUB_POLL_INTERVAL is unset or
	// invalid.
	DefaultInterval = 60 * time.Second
	// MinInterval floors GITHUB_POLL_INTERVAL.
	MinInterval = 15 * time.Second
	defaultAPI  = "https://api.github.com"
)

// Mode says why the poller is, or is not, running.
type Mode string

const (
	// ModeOff: nothing runs.
	ModeOff Mode = "off"
	// ModeToken: no App configured and GITHUB_POLL_TOKEN set.
	ModeToken Mode = "token"
	// ModeAppOptIn: an App is configured and GITHUB_POLL_ENABLED=true plus
	// GITHUB_POLL_TOKEN opt in to polling next to its webhooks.
	ModeAppOptIn Mode = "app+poll"
)

// Config is what the poller needs from the environment.
type Config struct {
	Token    string
	Interval time.Duration
	// APIBase overrides https://api.github.com (tests).
	APIBase string
}

// FromEnv decides whether the poller runs. getenv is os.Getenv in production.
//
//   - App vars absent, GITHUB_POLL_TOKEN set: ModeToken.
//   - App vars set: ModeOff, unless GITHUB_POLL_ENABLED=true and
//     GITHUB_POLL_TOKEN are both set (ModeAppOptIn).
//   - Otherwise ModeOff. An empty or whitespace-only token counts as unset.
func FromEnv(getenv func(string) string) (Config, Mode) {
	token := strings.TrimSpace(getenv("GITHUB_POLL_TOKEN"))
	app := strings.TrimSpace(getenv("GITHUB_APP_ID")) != "" &&
		strings.TrimSpace(getenv("GITHUB_APP_PRIVATE_KEY")) != ""
	enabled, _ := strconv.ParseBool(strings.TrimSpace(getenv("GITHUB_POLL_ENABLED")))

	mode := ModeOff
	switch {
	case token == "":
		if enabled {
			slog.Warn("github: GITHUB_POLL_ENABLED is set but GITHUB_POLL_TOKEN is empty, polling stays off")
		}
	case !app:
		mode = ModeToken
	case enabled:
		mode = ModeAppOptIn
	}
	if mode == ModeOff {
		return Config{}, ModeOff
	}
	return Config{
		Token:    token,
		Interval: parseInterval(getenv("GITHUB_POLL_INTERVAL")),
		APIBase:  defaultAPI,
	}, mode
}

// parseInterval reads a Go duration, defaulting when empty or invalid and
// flooring at MinInterval.
func parseInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("github: invalid GITHUB_POLL_INTERVAL, using the default", "default", DefaultInterval)
		return DefaultInterval
	}
	if d < MinInterval {
		return MinInterval
	}
	return d
}

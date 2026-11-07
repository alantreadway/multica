package daemon

// AG patch: provider reachability probe before claim.
//
// The daemon talks to the server on localhost, so a DNS or network outage does
// not stop it claiming runs; each run then fails on its first provider call and
// the server's provider_network retry chain spends its attempts. The batch
// poller asks agProviderGate before it claims: while the AI provider is
// unreachable it claims nothing, so queued runs keep their attempts. Already
// running tasks are not touched.
//
// Reachable = DNS resolved and the HTTPS request completed with any HTTP status
// (401, 403, 404 and 5xx all prove the network path works). Unreachable = DNS
// failure, connect or TLS error, or timeout. Probe disabled = fail open.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultAGProviderProbeURL is the probe target when
	// MULTICA_AG_PROVIDER_PROBE_URL is unset.
	DefaultAGProviderProbeURL = "https://api.anthropic.com"
	// DefaultAGProviderProbeTTL is how long a probe result is reused.
	DefaultAGProviderProbeTTL = 30 * time.Second
	// DefaultAGProviderProbeTimeout bounds one probe (DNS and HTTP together).
	DefaultAGProviderProbeTimeout = 5 * time.Second
)

// agProviderProbeURLFromEnv reads MULTICA_AG_PROVIDER_PROBE_URL. Unset gives the
// default; empty or "off" disables the probe (returned as ""). Anything else must
// be an absolute http(s) URL, so a typo fails daemon start instead of silently
// turning the gate off.
func agProviderProbeURLFromEnv(lookup func(string) (string, bool)) (string, error) {
	const key = "MULTICA_AG_PROVIDER_PROBE_URL"
	raw, ok := lookup(key)
	if !ok {
		return DefaultAGProviderProbeURL, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "off") {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: invalid URL %q: %w", key, raw, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", fmt.Errorf("%s: %q must be an absolute http(s) URL, or \"off\"", key, raw)
	}
	return raw, nil
}

// agProviderProbeFn checks one target and returns nil when it is reachable.
type agProviderProbeFn func(ctx context.Context, target string) error

// agProbeCall is one in-flight probe that concurrent callers wait on.
type agProbeCall struct {
	done      chan struct{}
	reachable bool
}

// agProviderProbeState is the cached probe result. The zero value is usable.
// mu is never held while a probe runs, and is independent of claimMu.
type agProviderProbeState struct {
	mu          sync.Mutex
	checked     bool
	checkedAt   time.Time
	reachable   bool
	downSince   time.Time
	inflight    *agProbeCall
	probeFn     agProviderProbeFn // test seam; nil uses agHTTPProbe
	now         func() time.Time  // test seam; nil uses time.Now
	probeCalled int               // probes started, for tests
}

// agProviderHealth is embedded in HealthResponse and flattened into /health.
// Both fields are omitted while the probe is disabled or has not run yet.
type agProviderHealth struct {
	ProviderReachable        *bool  `json:"provider_reachable,omitempty"`
	ProviderUnreachableSince string `json:"provider_unreachable_since,omitempty"`
}

func (s *agProviderProbeState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// agProviderGate reports whether the poller may claim. It returns true when the
// probe is disabled, and false when the provider is unreachable or ctx ends
// before a result is known. It must not be called with claimMu held.
func (d *Daemon) agProviderGate(ctx context.Context) bool {
	target := d.cfg.AGProviderProbeURL
	if target == "" {
		return true
	}
	s := &d.agProbe

	s.mu.Lock()
	if s.checked && s.clock().Sub(s.checkedAt) < d.cfg.AGProviderProbeTTL {
		ok := s.reachable
		s.mu.Unlock()
		return ok
	}
	if call := s.inflight; call != nil {
		s.mu.Unlock()
		select {
		case <-call.done:
			return call.reachable
		case <-ctx.Done():
			return false
		}
	}
	call := &agProbeCall{done: make(chan struct{})}
	s.inflight = call
	s.probeCalled++
	probe := s.probeFn
	s.mu.Unlock()

	if probe == nil {
		probe = agHTTPProbe
	}
	// The timeout bounds the probe whatever the probe function does; shutdown
	// cancels ctx and ends it sooner.
	probeCtx, cancel := context.WithTimeout(ctx, d.cfg.AGProviderProbeTimeout)
	err := probe(probeCtx, target)
	if err == nil && probeCtx.Err() != nil && ctx.Err() == nil {
		err = probeCtx.Err() // a probe that overran its timeout is a failure
	}
	cancel()

	s.mu.Lock()
	s.inflight = nil
	if ctx.Err() != nil {
		// Shutdown mid-probe: record nothing, so a half-run probe cannot flip
		// the state or log a transition.
		call.reachable = false
		close(call.done)
		s.mu.Unlock()
		return false
	}
	now := s.clock()
	wasDown := s.checked && !s.reachable
	reachable := err == nil
	s.checked, s.checkedAt, s.reachable = true, now, reachable
	var downFor time.Duration
	switch {
	case !reachable && !wasDown:
		s.downSince = now
	case reachable && wasDown:
		downFor = now.Sub(s.downSince)
		s.downSince = time.Time{}
	}
	call.reachable = reachable
	close(call.done)
	s.mu.Unlock()

	switch {
	case !reachable && !wasDown:
		d.logger.Warn("provider unreachable: not claiming tasks until it is back", "target", target, "error", err)
	case reachable && wasDown:
		d.logger.Info("provider reachable again: claiming resumes", "target", target, "down_for", downFor.Round(time.Second).String())
	}
	return reachable
}

func (d *Daemon) agProviderHealth() agProviderHealth {
	if d.cfg.AGProviderProbeURL == "" {
		return agProviderHealth{}
	}
	s := &d.agProbe
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checked {
		return agProviderHealth{}
	}
	reachable := s.reachable
	h := agProviderHealth{ProviderReachable: &reachable}
	if !reachable {
		h.ProviderUnreachableSince = s.downSince.UTC().Format(time.RFC3339)
	}
	return h
}

// agHTTPProbe does a DNS lookup of the target host, then an HTTPS HEAD. Any HTTP
// status counts as reachable. When a proxy applies to the target the local DNS
// step is skipped, because the proxy resolves the name, not this host.
func agHTTPProbe(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return err
	}
	if proxy, perr := http.ProxyFromEnvironment(req); perr != nil || proxy == nil {
		host := u.Hostname()
		if net.ParseIP(host) == nil {
			if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
				return fmt.Errorf("dns lookup %s: %w", host, err)
			}
		}
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
		// A redirect is already an HTTP answer; do not chase it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

type agProviderProbeConfig struct {
	url     string
	ttl     time.Duration
	timeout time.Duration
}

// agProviderProbeConfigFromEnv reads the three MULTICA_AG_PROVIDER_PROBE_*
// settings. Invalid values fail daemon start like the other duration settings.
func agProviderProbeConfigFromEnv() (agProviderProbeConfig, error) {
	target, err := agProviderProbeURLFromEnv(os.LookupEnv)
	if err != nil {
		return agProviderProbeConfig{}, err
	}
	ttl, err := durationFromEnv("MULTICA_AG_PROVIDER_PROBE_TTL", DefaultAGProviderProbeTTL)
	if err != nil {
		return agProviderProbeConfig{}, err
	}
	if ttl < 0 {
		return agProviderProbeConfig{}, fmt.Errorf("MULTICA_AG_PROVIDER_PROBE_TTL: must not be negative, got %s", ttl)
	}
	timeout, err := durationFromEnv("MULTICA_AG_PROVIDER_PROBE_TIMEOUT", DefaultAGProviderProbeTimeout)
	if err != nil {
		return agProviderProbeConfig{}, err
	}
	if timeout <= 0 {
		return agProviderProbeConfig{}, fmt.Errorf("MULTICA_AG_PROVIDER_PROBE_TIMEOUT: must be positive, got %s", timeout)
	}
	return agProviderProbeConfig{url: target, ttl: ttl, timeout: timeout}, nil
}

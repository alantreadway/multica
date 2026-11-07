package daemon

// AG patch: provider reachability probe tests beyond the smoke test: the poller
// path, TTL, transition logging, single-flight, real HTTP/DNS/timeout outcomes,
// /health fields and env parsing. TestAGPatch* with "AG patch:" subtests, as
// counted by ag-patches/test.sh. No real network: the HTTP cases use httptest
// and the reserved ".invalid" TLD.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// agLockedBuffer is a bytes.Buffer safe for slog writes from several goroutines.
type agLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *agLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *agLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *agLockedBuffer) count(substr string) int {
	return strings.Count(b.String(), substr)
}

func agProbeCalls(d *Daemon) int {
	d.agProbe.mu.Lock()
	defer d.agProbe.mu.Unlock()
	return d.agProbe.probeCalled
}

// newAGProbePollerDaemon builds a daemon whose only reachable server endpoint is
// a claim counter, with the probe function controlled by the returned flag
// (true = provider reachable). The probe TTL is 0, so every poll cycle probes.
func newAGProbePollerDaemon(t *testing.T, maxTasks int) (d *Daemon, claims *atomic.Int64, reachable *atomic.Bool) {
	t.Helper()
	claims = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			claims.Add(1)
			_, _ = w.Write([]byte(`{"tasks":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	d = New(Config{
		ServerBaseURL:          srv.URL,
		HeartbeatInterval:      time.Hour,
		PollInterval:           10 * time.Millisecond,
		MaxConcurrentTasks:     maxTasks,
		AGProviderProbeURL:     "https://provider.test",
		AGProviderProbeTTL:     0,
		AGProviderProbeTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	d.workspaces["ws-1"] = &workspaceState{workspaceID: "ws-1", runtimeIDs: []string{"rt-1"}}
	d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "codex"}
	d.cancelPollInterval = time.Hour

	reachable = &atomic.Bool{}
	d.agProbe.probeFn = func(context.Context, string) error {
		if reachable.Load() {
			return nil
		}
		return errors.New("lookup provider.test: no such host")
	}
	return d, claims, reachable
}

func waitForAGProbe(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAGPatchProviderProbePoller(t *testing.T) {
	t.Run("AG patch: provider probe poller skips claims while down, releases slots and resumes", func(t *testing.T) {
		const maxTasks = 3
		d, claims, reachable := newAGProbePollerDaemon(t, maxTasks)
		sem := newTaskSlotSemaphore(t.TempDir(), maxTasks, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var taskWG sync.WaitGroup
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &taskWG)
		}()

		// Down: several poll cycles run and probe, but nothing is claimed.
		waitForAGProbe(t, "several probes while down", func() bool { return agProbeCalls(d) >= 5 })
		if n := claims.Load(); n != 0 {
			t.Fatalf("claim endpoint called %d times while the provider is unreachable", n)
		}

		// Up: the next cycle claims.
		reachable.Store(true)
		waitForAGProbe(t, "a claim after recovery", func() bool { return claims.Load() >= 1 })

		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("poller did not stop after cancel")
		}
		taskWG.Wait()

		// Every slot the poller took (while down and after the empty claims) is
		// free again.
		got, err := sem.tryAcquireN(maxTasks)
		if err != nil || len(got) != maxTasks {
			t.Fatalf("slots after poller exit: got %v err=%v, want all %d free", got, err, maxTasks)
		}
	})

	t.Run("AG patch: provider probe poller slots are free between down cycles", func(t *testing.T) {
		const maxTasks = 2
		d, claims, _ := newAGProbePollerDaemon(t, maxTasks)
		sem := newTaskSlotSemaphore(t.TempDir(), maxTasks, nil)
		d.cfg.PollInterval = time.Hour // park in the down-branch sleep after one cycle

		ctx, cancel := context.WithCancel(context.Background())
		var taskWG sync.WaitGroup
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &taskWG)
		}()
		waitForAGProbe(t, "the first probe", func() bool { return agProbeCalls(d) >= 1 })

		// The poller is now sleeping in the down branch; it must not hold slots.
		waitForAGProbe(t, "slots released while sleeping", func() bool {
			got, _ := sem.tryAcquireN(maxTasks)
			for _, s := range got {
				sem.release(s)
			}
			return len(got) == maxTasks
		})
		if claims.Load() != 0 {
			t.Fatal("claimed while down")
		}
		// Shutdown ends the down-branch sleep promptly.
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("poller did not stop while the provider was down")
		}
	})

	t.Run("AG patch: provider probe poller with the probe disabled claims without probing", func(t *testing.T) {
		d, claims, _ := newAGProbePollerDaemon(t, 2)
		d.cfg.AGProviderProbeURL = ""
		sem := newTaskSlotSemaphore(t.TempDir(), 2, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var taskWG sync.WaitGroup
		go d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &taskWG)
		waitForAGProbe(t, "a claim with the probe disabled", func() bool { return claims.Load() >= 1 })
		if n := agProbeCalls(d); n != 0 {
			t.Fatalf("disabled probe ran %d times", n)
		}
		cancel()
		taskWG.Wait()
	})
}

func TestAGPatchProviderProbeCache(t *testing.T) {
	t.Run("AG patch: provider probe result is reused inside the TTL and re-probed at it", func(t *testing.T) {
		calls := 0
		d, now := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			calls++
			return nil
		})
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if !d.agProviderGate(ctx) {
				t.Fatal("gate closed while reachable")
			}
		}
		if calls != 1 {
			t.Fatalf("calls = %d within the TTL, want 1", calls)
		}
		*now = now.Add(29 * time.Second)
		d.agProviderGate(ctx)
		if calls != 1 {
			t.Fatalf("calls = %d at 29s of a 30s TTL, want 1", calls)
		}
		*now = now.Add(time.Second) // exactly the TTL
		d.agProviderGate(ctx)
		if calls != 2 {
			t.Fatalf("calls = %d at the TTL, want 2", calls)
		}
	})

	t.Run("AG patch: provider probe passes the configured target to the probe", func(t *testing.T) {
		var got string
		d, _ := newAGProbeTestDaemon("https://example.test/x", func(_ context.Context, target string) error {
			got = target
			return nil
		})
		d.agProviderGate(context.Background())
		if got != "https://example.test/x" {
			t.Fatalf("probe target = %q", got)
		}
	})

	t.Run("AG patch: provider probe concurrent callers share one in-flight probe", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{}, 8)
		d, _ := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			started <- struct{}{}
			<-release
			return nil
		})
		const callers = 6
		results := make(chan bool, callers)
		for i := 0; i < callers; i++ {
			go func() { results <- d.agProviderGate(context.Background()) }()
		}
		<-started
		// Give the other callers time to find the probe in flight.
		waitForAGProbe(t, "callers to queue", func() bool { return agProbeCalls(d) == 1 })
		time.Sleep(50 * time.Millisecond)
		close(release)
		for i := 0; i < callers; i++ {
			select {
			case ok := <-results:
				if !ok {
					t.Fatal("a caller saw unreachable although the shared probe passed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller never returned")
			}
		}
		if n := agProbeCalls(d); n != 1 {
			t.Fatalf("probes started = %d, want 1", n)
		}
	})

	t.Run("AG patch: provider probe does not run while claimMu is held", func(t *testing.T) {
		d, _ := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error { return nil })
		d.claimMu.Lock()
		defer d.claimMu.Unlock()
		done := make(chan bool, 1)
		go func() { done <- d.agProviderGate(context.Background()) }()
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("gate closed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("gate blocked on claimMu")
		}
	})
}

func TestAGPatchProviderProbeTransitionLogs(t *testing.T) {
	t.Run("AG patch: provider probe logs each transition once, not each probe", func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		d, now := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			if fail.Load() {
				return errors.New("no such host")
			}
			return nil
		})
		logs := &agLockedBuffer{}
		d.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		ctx := context.Background()

		// Five failed probes (TTL expires between each) log one Warn.
		for i := 0; i < 5; i++ {
			d.agProviderGate(ctx)
			*now = now.Add(31 * time.Second)
		}
		if n := logs.count("level=WARN"); n != 1 {
			t.Fatalf("WARN lines while down = %d, want 1\n%s", n, logs.String())
		}
		warn := logs.String()
		if !strings.Contains(warn, "https://provider.test") || !strings.Contains(warn, "no such host") {
			t.Fatalf("down log lacks target or error: %s", warn)
		}
		if logs.count("level=INFO") != 0 {
			t.Fatalf("INFO logged before recovery: %s", logs.String())
		}

		// Recovery logs one Info with the down duration; later passes log nothing.
		fail.Store(false)
		for i := 0; i < 4; i++ {
			d.agProviderGate(ctx)
			*now = now.Add(31 * time.Second)
		}
		if n := logs.count("level=INFO"); n != 1 {
			t.Fatalf("INFO lines after recovery = %d, want 1\n%s", n, logs.String())
		}
		if n := logs.count("level=WARN"); n != 1 {
			t.Fatalf("WARN lines = %d after recovery, want still 1", n)
		}
		if !strings.Contains(logs.String(), "down_for=") {
			t.Fatalf("recovery log lacks down_for: %s", logs.String())
		}

		// A second outage logs a second Warn.
		fail.Store(true)
		d.agProviderGate(ctx)
		if n := logs.count("level=WARN"); n != 2 {
			t.Fatalf("WARN lines after a second outage = %d, want 2", n)
		}
	})

	t.Run("AG patch: provider probe recovery log reports how long it was down", func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		d, now := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			if fail.Load() {
				return errors.New("down")
			}
			return nil
		})
		logs := &agLockedBuffer{}
		d.logger = slog.New(slog.NewTextHandler(logs, nil))
		d.agProviderGate(context.Background())
		*now = now.Add(5 * time.Minute)
		fail.Store(false)
		d.agProviderGate(context.Background())
		if !strings.Contains(logs.String(), "down_for=5m0s") {
			t.Fatalf("recovery log = %s, want down_for=5m0s", logs.String())
		}
	})

	t.Run("AG patch: provider probe stays quiet when the first probe passes", func(t *testing.T) {
		d, _ := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error { return nil })
		logs := &agLockedBuffer{}
		d.logger = slog.New(slog.NewTextHandler(logs, nil))
		d.agProviderGate(context.Background())
		if logs.String() != "" {
			t.Fatalf("unexpected log on a clean first probe: %s", logs.String())
		}
	})
}

func TestAGPatchProviderProbeTimeoutAndShutdown(t *testing.T) {
	t.Run("AG patch: provider probe counts a probe that outruns its timeout as unreachable", func(t *testing.T) {
		d, _ := newAGProbeTestDaemon("https://provider.test", func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return nil // a probe that ignores the deadline and reports success late
		})
		d.cfg.AGProviderProbeTimeout = 30 * time.Millisecond
		start := time.Now()
		if d.agProviderGate(context.Background()) {
			t.Fatal("gate open after a timed-out probe")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("probe took %s with a 30ms timeout", time.Since(start))
		}
		if h := d.agProviderHealth(); h.ProviderReachable == nil || *h.ProviderReachable {
			t.Fatalf("health after timeout = %+v", h)
		}
	})

	t.Run("AG patch: provider probe shutdown mid-probe records nothing", func(t *testing.T) {
		started := make(chan struct{})
		d, _ := newAGProbeTestDaemon("https://provider.test", func(ctx context.Context, _ string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		d.cfg.AGProviderProbeTimeout = time.Minute
		logs := &agLockedBuffer{}
		d.logger = slog.New(slog.NewTextHandler(logs, nil))

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan bool, 1)
		go func() { result <- d.agProviderGate(ctx) }()
		<-started
		cancel()
		select {
		case ok := <-result:
			if ok {
				t.Fatal("gate open on shutdown")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("gate did not return after the poller context ended")
		}
		if logs.String() != "" {
			t.Fatalf("shutdown logged a transition: %s", logs.String())
		}
		if h := d.agProviderHealth(); h.ProviderReachable != nil {
			t.Fatalf("shutdown recorded a result: %+v", h)
		}
		// The next caller probes afresh rather than reusing a half-run result.
		d.agProbe.probeFn = func(context.Context, string) error { return nil }
		if !d.agProviderGate(context.Background()) {
			t.Fatal("gate stayed closed after a cancelled probe")
		}
	})

	t.Run("AG patch: provider probe waiter returns when its context ends", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		d, _ := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
		go d.agProviderGate(context.Background())
		<-started

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan bool, 1)
		go func() { result <- d.agProviderGate(ctx) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		select {
		case ok := <-result:
			if ok {
				t.Fatal("waiter reported reachable after cancel")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiter stuck behind the in-flight probe")
		}
		close(release)
	})
}

func TestAGPatchProviderProbeHTTP(t *testing.T) {
	probe := func(t *testing.T, target string, timeout time.Duration) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return agHTTPProbe(ctx, target)
	}

	for _, status := range []int{200, 301, 401, 403, 404, 429, 500, 503} {
		status := status
		t.Run("AG patch: provider probe HTTP "+http.StatusText(status)+" is reachable", func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodHead {
					t.Errorf("method = %s, want HEAD", r.Method)
				}
				if status == 301 {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			if err := probe(t, srv.URL, 5*time.Second); err != nil {
				t.Fatalf("status %d should be reachable: %v", status, err)
			}
			if hits.Load() != 1 {
				t.Fatalf("server hits = %d, want 1 (redirects are not followed)", hits.Load())
			}
		})
	}

	t.Run("AG patch: provider probe connection refused is unreachable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close() // nothing listens here now
		if err := probe(t, "http://"+addr, 5*time.Second); err == nil {
			t.Fatal("refused connection counted as reachable")
		}
	})

	t.Run("AG patch: provider probe unresolvable host is unreachable", func(t *testing.T) {
		if hasProxyEnv() {
			t.Skip("a proxy is configured; the local DNS step is skipped by design")
		}
		// ".invalid" is reserved (RFC 6761) and never resolves.
		err := probe(t, "https://ag-probe-test.invalid", 5*time.Second)
		if err == nil {
			t.Fatal("unresolvable host counted as reachable")
		}
		if !strings.Contains(err.Error(), "dns lookup") {
			t.Fatalf("error = %v, want a DNS error", err)
		}
	})

	t.Run("AG patch: provider probe stalled server times out as unreachable", func(t *testing.T) {
		unblock := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-unblock:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(unblock)
		start := time.Now()
		if err := probe(t, srv.URL, 100*time.Millisecond); err == nil {
			t.Fatal("stalled server counted as reachable")
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("probe took %s with a 100ms deadline", time.Since(start))
		}
	})

	t.Run("AG patch: provider probe through the gate with a real server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		d, now := newAGProbeTestDaemon(srv.URL, nil) // nil probeFn uses the real probe
		d.agProbe.probeFn = nil
		if !d.agProviderGate(context.Background()) {
			t.Fatal("5xx should leave the gate open")
		}
		srv.Close()
		*now = now.Add(31 * time.Second)
		if d.agProviderGate(context.Background()) {
			t.Fatal("gate open after the server went away")
		}
	})
}

func hasProxyEnv() bool {
	req, err := http.NewRequest(http.MethodHead, "https://ag-probe-test.invalid", nil)
	if err != nil {
		return true
	}
	proxy, perr := http.ProxyFromEnvironment(req)
	return perr != nil || proxy != nil
}

// agProbeHealthJSON reads /health through the real handler, so a dropped embed
// or a dropped field in the response literal fails the test.
func agProbeHealthJSON(t *testing.T, d *Daemon) map[string]any {
	t.Helper()
	if d.workspaces == nil {
		d.workspaces = map[string]*workspaceState{}
	}
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d", rec.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return raw
}

func TestAGPatchProviderProbeHealth(t *testing.T) {
	t.Run("AG patch: provider probe /health fields track down, still down and recovery", func(t *testing.T) {
		d, now := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error { return errors.New("down") })

		// Before the first probe: fields omitted.
		m := agProbeHealthJSON(t, d)
		for _, k := range []string{"provider_reachable", "provider_unreachable_since"} {
			if _, ok := m[k]; ok {
				t.Fatalf("%s present before any probe: %v", k, m)
			}
		}

		down := *now
		d.agProviderGate(context.Background())
		m = agProbeHealthJSON(t, d)
		if m["provider_reachable"] != false {
			t.Fatalf("provider_reachable = %v, want false", m["provider_reachable"])
		}
		if m["provider_unreachable_since"] != down.UTC().Format(time.RFC3339) {
			t.Fatalf("provider_unreachable_since = %v, want %s", m["provider_unreachable_since"], down.UTC().Format(time.RFC3339))
		}

		// Still down on a later probe: "since" keeps the first failure time.
		*now = now.Add(31 * time.Second)
		d.agProviderGate(context.Background())
		if got := agProbeHealthJSON(t, d)["provider_unreachable_since"]; got != down.UTC().Format(time.RFC3339) {
			t.Fatalf("provider_unreachable_since moved to %v on a repeat failure", got)
		}

		d.agProbe.probeFn = func(context.Context, string) error { return nil }
		*now = now.Add(31 * time.Second)
		d.agProviderGate(context.Background())
		m = agProbeHealthJSON(t, d)
		if m["provider_reachable"] != true {
			t.Fatalf("provider_reachable = %v after recovery, want true", m["provider_reachable"])
		}
		if _, ok := m["provider_unreachable_since"]; ok {
			t.Fatalf("provider_unreachable_since still present after recovery: %v", m)
		}
	})

	t.Run("AG patch: provider probe /health omits the fields while disabled", func(t *testing.T) {
		for _, target := range []string{"", "off"} {
			d, _ := newAGProbeTestDaemon(target, func(context.Context, string) error { return errors.New("down") })
			if target == "off" {
				d.cfg.AGProviderProbeURL = "" // config.go normalises "off" to empty
			}
			d.agProviderGate(context.Background())
			m := agProbeHealthJSON(t, d)
			for k := range m {
				if strings.HasPrefix(k, "provider_") {
					t.Fatalf("disabled probe (%q) leaked %s: %v", target, k, m)
				}
			}
		}
	})
}

// The proxy branch of agHTTPProbe depends on http.ProxyFromEnvironment, which
// reads the environment once per process. These cases therefore re-exec the
// test binary with a controlled environment and run agHTTPProbe in the child.
const agProbeChildEnv = "AG_PROBE_CHILD_TARGET"

func TestAGPatchProviderProbeChild(t *testing.T) {
	target := os.Getenv(agProbeChildEnv)
	if target == "" {
		t.Skip("helper for the re-exec cases below")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := agHTTPProbe(ctx, target); err != nil {
		fmt.Fprintln(os.Stderr, "PROBE_ERROR:", err)
		os.Exit(3)
	}
	fmt.Fprintln(os.Stderr, "PROBE_OK")
	os.Exit(0)
}

// agProbeRunChild runs agHTTPProbe(target) in a fresh process whose proxy
// environment is exactly env (inherited proxy settings are dropped).
func agProbeRunChild(t *testing.T, target string, env ...string) (output string, err error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAGPatchProviderProbeChild$", "-test.v")
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if k == "HTTP_PROXY" || k == "HTTPS_PROXY" || k == "NO_PROXY" || k == "ALL_PROXY" || k == "REQUEST_METHOD" {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, agProbeChildEnv+"="+target)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAGPatchProviderProbeProxy(t *testing.T) {
	const unresolvable = "http://ag-probe-test.invalid/"

	t.Run("AG patch: provider probe skips local DNS when a proxy applies and counts the proxy's answer as reachable", func(t *testing.T) {
		var hits atomic.Int64
		var gotMethod, gotHost atomic.Value
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			gotMethod.Store(r.Method)
			gotHost.Store(r.Host)
			w.WriteHeader(http.StatusForbidden) // any status counts as reachable
		}))
		defer proxy.Close()

		out, err := agProbeRunChild(t, unresolvable, "HTTP_PROXY="+proxy.URL)
		if err != nil || !strings.Contains(out, "PROBE_OK") {
			t.Fatalf("probe through the proxy failed: %v\n%s", err, out)
		}
		if hits.Load() != 1 {
			t.Fatalf("proxy hits = %d, want 1", hits.Load())
		}
		if gotMethod.Load() != http.MethodHead || gotHost.Load() != "ag-probe-test.invalid" {
			t.Fatalf("proxy saw %v %v, want HEAD ag-probe-test.invalid", gotMethod.Load(), gotHost.Load())
		}
	})

	t.Run("AG patch: provider probe through a dead proxy is unreachable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		out, err := agProbeRunChild(t, unresolvable, "HTTP_PROXY=http://"+addr)
		if err == nil || !strings.Contains(out, "PROBE_ERROR") {
			t.Fatalf("dead proxy counted as reachable: %v\n%s", err, out)
		}
		if strings.Contains(out, "dns lookup") {
			t.Fatalf("local DNS ran although a proxy applies:\n%s", out)
		}
	})

	t.Run("AG patch: provider probe does local DNS when NO_PROXY exempts the target", func(t *testing.T) {
		var hits atomic.Int64
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		defer proxy.Close()
		out, err := agProbeRunChild(t, unresolvable, "HTTP_PROXY="+proxy.URL, "NO_PROXY=.invalid")
		if err == nil || !strings.Contains(out, "dns lookup") {
			t.Fatalf("want a DNS failure for an exempt target, got %v\n%s", err, out)
		}
		if hits.Load() != 0 {
			t.Fatalf("proxy hit %d times for an exempt target", hits.Load())
		}
	})

	t.Run("AG patch: provider probe without a proxy fails an unresolvable host at DNS", func(t *testing.T) {
		out, err := agProbeRunChild(t, unresolvable)
		if err == nil || !strings.Contains(out, "dns lookup") {
			t.Fatalf("want a DNS failure with no proxy, got %v\n%s", err, out)
		}
	})
}

func TestAGPatchProviderProbeConfigEnv(t *testing.T) {
	const (
		urlKey     = "MULTICA_AG_PROVIDER_PROBE_URL"
		ttlKey     = "MULTICA_AG_PROVIDER_PROBE_TTL"
		timeoutKey = "MULTICA_AG_PROVIDER_PROBE_TIMEOUT"
	)

	t.Run("AG patch: provider probe env defaults", func(t *testing.T) {
		// t.Setenv registers restoration; Unsetenv then gives a truly unset var.
		for _, k := range []string{urlKey, ttlKey, timeoutKey} {
			t.Setenv(k, "")
			unsetenvForTest(t, k)
		}
		cfg, err := agProviderProbeConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.url != "https://api.anthropic.com" || cfg.ttl != 30*time.Second || cfg.timeout != 5*time.Second {
			t.Fatalf("defaults = %+v", cfg)
		}
	})

	t.Run("AG patch: provider probe env custom values", func(t *testing.T) {
		t.Setenv(urlKey, "https://example.test/health")
		t.Setenv(ttlKey, "2m")
		t.Setenv(timeoutKey, "750ms")
		cfg, err := agProviderProbeConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.url != "https://example.test/health" || cfg.ttl != 2*time.Minute || cfg.timeout != 750*time.Millisecond {
			t.Fatalf("custom = %+v", cfg)
		}
	})

	t.Run("AG patch: provider probe env off and empty disable the probe", func(t *testing.T) {
		for _, v := range []string{"off", "OFF", "Off", " off ", "", "   "} {
			t.Setenv(urlKey, v)
			cfg, err := agProviderProbeConfigFromEnv()
			if err != nil || cfg.url != "" {
				t.Fatalf("%q: cfg=%+v err=%v, want disabled", v, cfg, err)
			}
		}
	})

	t.Run("AG patch: provider probe env rejects invalid values", func(t *testing.T) {
		cases := []struct{ key, val, want string }{
			{urlKey, "not a url", urlKey},
			{urlKey, "api.anthropic.com", urlKey},  // no scheme
			{urlKey, "ftp://example.test", urlKey}, // wrong scheme
			{urlKey, "https://", urlKey},           // no host
			{ttlKey, "soon", ttlKey},               // not a duration
			{ttlKey, "-5s", ttlKey},                // negative
			{timeoutKey, "abc", timeoutKey},        // not a duration
			{timeoutKey, "0s", timeoutKey},         // not positive
			{timeoutKey, "-1s", timeoutKey},        // not positive
		}
		for _, c := range cases {
			t.Setenv(urlKey, "https://example.test")
			t.Setenv(ttlKey, "30s")
			t.Setenv(timeoutKey, "5s")
			t.Setenv(c.key, c.val)
			_, err := agProviderProbeConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s=%q: err = %v, want an error naming %s", c.key, c.val, err, c.want)
			}
		}
	})

	t.Run("AG patch: provider probe env TTL zero is allowed", func(t *testing.T) {
		t.Setenv(urlKey, "https://example.test")
		t.Setenv(ttlKey, "0s")
		cfg, err := agProviderProbeConfigFromEnv()
		if err != nil || cfg.ttl != 0 {
			t.Fatalf("cfg=%+v err=%v", cfg, err)
		}
	})

	t.Run("AG patch: provider probe LoadConfig wires the settings and fails start on bad values", func(t *testing.T) {
		stageFakeAgent(t)
		redirectTestHome(t, t.TempDir())
		t.Setenv("SHELL", t.TempDir()+"/missing-shell")
		overrides := Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()}

		t.Setenv(urlKey, "https://example.test")
		t.Setenv(ttlKey, "45s")
		t.Setenv(timeoutKey, "2s")
		cfg, err := LoadConfig(overrides)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AGProviderProbeURL != "https://example.test" || cfg.AGProviderProbeTTL != 45*time.Second || cfg.AGProviderProbeTimeout != 2*time.Second {
			t.Fatalf("Config probe fields = %q %s %s", cfg.AGProviderProbeURL, cfg.AGProviderProbeTTL, cfg.AGProviderProbeTimeout)
		}

		t.Setenv(urlKey, "off")
		cfg, err = LoadConfig(overrides)
		if err != nil || cfg.AGProviderProbeURL != "" {
			t.Fatalf("off: url=%q err=%v", cfg.AGProviderProbeURL, err)
		}

		t.Setenv(ttlKey, "not-a-duration")
		if _, err := LoadConfig(overrides); err == nil || !strings.Contains(err.Error(), ttlKey) {
			t.Fatalf("LoadConfig with a bad TTL: err = %v", err)
		}
	})
}

// unsetenvForTest removes k for the rest of the test. Call t.Setenv(k, ...)
// first so the original value is restored on cleanup.
func unsetenvForTest(t *testing.T, k string) {
	t.Helper()
	if err := os.Unsetenv(k); err != nil {
		t.Fatal(err)
	}
}

package daemon

// AG patch: provider reachability probe smoke tests; TestAGPatch* with
// "AG patch:" subtests, as counted by ag-patches/test.sh.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAGProbeTestDaemon(target string, probe agProviderProbeFn) (*Daemon, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	d := &Daemon{
		cfg: Config{
			AGProviderProbeURL:     target,
			AGProviderProbeTTL:     30 * time.Second,
			AGProviderProbeTimeout: time.Second,
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	d.agProbe.probeFn = probe
	d.agProbe.now = func() time.Time { return now }
	return d, &now
}

func TestAGPatchProviderProbeGate(t *testing.T) {
	t.Run("AG patch: provider probe gates claims, caches and recovers", func(t *testing.T) {
		var fail = true
		calls := 0
		d, now := newAGProbeTestDaemon("https://provider.test", func(context.Context, string) error {
			calls++
			if fail {
				return errors.New("no such host")
			}
			return nil
		})
		ctx := context.Background()
		if d.agProviderGate(ctx) {
			t.Fatal("gate allowed a claim while the probe fails")
		}
		if d.agProviderGate(ctx) || calls != 1 {
			t.Fatalf("result not cached within the TTL: calls=%d", calls)
		}
		if h := d.agProviderHealth(); h.ProviderReachable == nil || *h.ProviderReachable || h.ProviderUnreachableSince == "" {
			t.Fatalf("health while down = %+v", h)
		}
		fail = false
		*now = now.Add(31 * time.Second)
		if !d.agProviderGate(ctx) || calls != 2 {
			t.Fatalf("gate did not reopen after the TTL: calls=%d", calls)
		}
		if h := d.agProviderHealth(); h.ProviderReachable == nil || !*h.ProviderReachable || h.ProviderUnreachableSince != "" {
			t.Fatalf("health after recovery = %+v", h)
		}
	})

	t.Run("AG patch: provider probe disabled never blocks", func(t *testing.T) {
		d, _ := newAGProbeTestDaemon("", func(context.Context, string) error { return errors.New("down") })
		if !d.agProviderGate(context.Background()) || d.agProbe.probeCalled != 0 {
			t.Fatal("disabled probe blocked or ran")
		}
	})

	t.Run("AG patch: provider probe counts any HTTP status as reachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead {
				t.Errorf("method = %s, want HEAD", r.Method)
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := agHTTPProbe(ctx, srv.URL); err != nil {
			t.Fatalf("403 should be reachable: %v", err)
		}
		srv.Close()
		if err := agHTTPProbe(ctx, srv.URL); err == nil {
			t.Fatal("closed server should be unreachable")
		}
	})
}

func TestAGPatchProviderProbeConfig(t *testing.T) {
	t.Run("AG patch: provider probe env parsing", func(t *testing.T) {
		t.Setenv("MULTICA_AG_PROVIDER_PROBE_URL", "off")
		cfg, err := agProviderProbeConfigFromEnv()
		if err != nil || cfg.url != "" || cfg.ttl != 30*time.Second || cfg.timeout != 5*time.Second {
			t.Fatalf("off: %+v %v", cfg, err)
		}
		t.Setenv("MULTICA_AG_PROVIDER_PROBE_URL", "not a url")
		if _, err := agProviderProbeConfigFromEnv(); err == nil {
			t.Fatal("invalid URL accepted")
		}
	})
}

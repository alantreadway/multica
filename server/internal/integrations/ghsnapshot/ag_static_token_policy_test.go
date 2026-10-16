package ghsnapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// authLog records the Authorization header of each request by path.
type authLog struct {
	mu   sync.Mutex
	seen []string // "<path> <Authorization>"
}

func (a *authLog) add(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, r.URL.Path+" "+r.Header.Get("Authorization"))
}

func (a *authLog) joined() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.seen, "\n")
}

func TestAGPatchAppClientKeepsAppAuthForRealInstallations(t *testing.T) {
	log := &authLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_minted","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	t.Setenv("GITHUB_APP_ID", "123")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", agTestPEM(t))
	t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")
	c, err := NewClientFromEnv()
	if err != nil || !c.Enabled() {
		t.Fatalf("client = %v, %v", c, err)
	}
	c.apiBase = srv.URL

	t.Run("AG patch: a webhook installation still mints and uses the App token", func(t *testing.T) {
		if _, err := c.graphQL(context.Background(), 42, "query{x}", nil); err != nil {
			t.Fatal(err)
		}
		out := log.joined()
		if !strings.Contains(out, "/app/installations/42/access_tokens") || !strings.Contains(out, "/graphql Bearer ghs_minted") {
			t.Fatalf("requests:\n%s", out)
		}
		if strings.Contains(out, "ghp_secret") {
			t.Fatalf("the poll token was sent for a real installation:\n%s", out)
		}
	})

	t.Run("AG patch: installation 0 uses the poll token and mints nothing", func(t *testing.T) {
		log.mu.Lock()
		log.seen = nil
		log.mu.Unlock()
		if _, err := c.graphQL(context.Background(), 0, "query{x}", nil); err != nil {
			t.Fatal(err)
		}
		out := log.joined()
		if out != "/graphql Bearer ghp_secret" {
			t.Fatalf("requests:\n%s", out)
		}
	})
}

func TestAGPatchStaticTokenClientErrorsAndLimits(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")

	t.Run("AG patch: a rate-limited token client reports a RateLimitError with the Retry-After", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		c := agStaticTokenClient()
		c.apiBase = srv.URL
		_, err := c.graphQL(context.Background(), 0, "query{x}", nil)
		rl, ok := err.(*RateLimitError)
		if !ok || rl.RetryAfter != 42*time.Second {
			t.Fatalf("err = %T %v, want a 42s RateLimitError", err, err)
		}
		if strings.Contains(err.Error(), "ghp_secret") {
			t.Fatal("the rate-limit error carries the token")
		}
	})

	t.Run("AG patch: non-200 and malformed answers never echo the token", func(t *testing.T) {
		for name, h := range map[string]http.HandlerFunc{
			"500": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
			"echo auth": func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(r.Header.Get("Authorization")))
			},
			"bad json": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{")) },
		} {
			srv := httptest.NewServer(h)
			c := agStaticTokenClient()
			c.apiBase = srv.URL
			_, err := c.graphQL(context.Background(), 0, "query{x}", nil)
			srv.Close()
			if err == nil {
				t.Fatalf("%s: want an error", name)
			}
			if strings.Contains(err.Error(), "ghp_") {
				t.Fatalf("%s: error leaks the token: %v", name, err)
			}
		}
	})

	t.Run("AG patch: the token client's request path never touches the App endpoints", func(t *testing.T) {
		log := &authLog{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.add(r)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))
		defer srv.Close()
		c := agStaticTokenClient()
		c.apiBase = srv.URL
		for _, inst := range []int64{0, 1, 99} {
			if _, err := c.graphQL(context.Background(), inst, "query{x}", nil); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(log.joined(), "/app/") {
			t.Fatalf("token client called an App endpoint:\n%s", log.joined())
		}
	})
}

// A token-only client drives the refresh pipeline as installation 0: enqueue
// accepts the address, the worker pool picks it up and the sweep keys on 0.
func TestAGPatchTokenClientDrivesRefreshManager(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")
	c := agStaticTokenClient()

	t.Run("AG patch: the manager is enabled by a token-only client and queues installation 0", func(t *testing.T) {
		m := NewManager(c, nil, nil, nil)
		if !m.Enabled() {
			t.Fatal("manager should be enabled by a token client")
		}
		m.Enqueue(0, "o", "r", 7)
		m.Enqueue(0, "o", "r", 7)
		if len(m.queue) != 1 {
			t.Fatalf("queue length = %d, want 1", len(m.queue))
		}
	})

	t.Run("AG patch: a disabled client leaves the manager off and Enqueue a no-op", func(t *testing.T) {
		t.Setenv("GITHUB_POLL_TOKEN", "  ")
		off, err := NewClientFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		m := NewManager(off, nil, nil, nil)
		m.Enqueue(0, "o", "r", 7)
		if m.Enabled() || len(m.queue) != 0 {
			t.Fatalf("enabled = %v, queued = %d", m.Enabled(), len(m.queue))
		}
	})

	t.Run("AG patch: a rate limit on installation 0 pauses 0 and no other installation", func(t *testing.T) {
		m := NewManager(c, nil, nil, nil)
		now := time.Unix(30000, 0)
		m.now = func() time.Time { return now }
		m.jitter = func() time.Duration { return 0 }
		m.extendRateLimit(0, time.Hour)
		if m.rateLimitPause(0) <= 0 {
			t.Fatal("installation 0 should be paused")
		}
		if m.rateLimitPause(7) > 0 {
			t.Fatal("installation 7 must not be paused by installation 0")
		}
	})
}

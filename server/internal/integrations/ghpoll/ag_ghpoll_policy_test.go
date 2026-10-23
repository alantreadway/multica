package ghpoll

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// logCapture records every slog record, with its level, while a test runs.
type logCapture struct {
	mu   sync.Mutex
	recs []capturedRecord
}

type capturedRecord struct {
	level slog.Level
	text  string // message plus every attribute, flattened
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, capturedRecord{level: r.Level, text: b.String()})
	return nil
}

func (c *logCapture) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var parts []string
	for _, r := range c.recs {
		parts = append(parts, r.text)
	}
	return strings.Join(parts, "\n")
}

func (c *logCapture) count(level slog.Level) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if r.level == level {
			n++
		}
	}
	return n
}

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = nil
}

// captureLogs swaps the default logger for a recorder until the test ends.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

var testWS = []pgtype.UUID{{Bytes: [16]byte{1}, Valid: true}}

func TestAGPatchGHPollConfigEdges(t *testing.T) {
	app := map[string]string{"GITHUB_APP_ID": "1", "GITHUB_APP_PRIVATE_KEY": "key", "GITHUB_POLL_TOKEN": "t"}
	for _, v := range []string{"true", "TRUE", "1"} {
		t.Run("AG patch: ENABLED="+v+" opts an App deployment in", func(t *testing.T) {
			env := map[string]string{"GITHUB_POLL_ENABLED": v}
			for k, x := range app {
				env[k] = x
			}
			if _, mode := FromEnv(envOf(env)); mode != ModeAppOptIn {
				t.Fatalf("mode = %q", mode)
			}
		})
	}
	for _, v := range []string{"", "false", "no", "garbage", "0"} {
		t.Run("AG patch: ENABLED="+v+" leaves an App deployment unpolled", func(t *testing.T) {
			env := map[string]string{"GITHUB_POLL_ENABLED": v}
			for k, x := range app {
				env[k] = x
			}
			if cfg, mode := FromEnv(envOf(env)); mode != ModeOff || cfg.Token != "" {
				t.Fatalf("mode = %q, token carried = %v", mode, cfg.Token != "")
			}
		})
	}
	t.Run("AG patch: token mode does not need ENABLED", func(t *testing.T) {
		cfg, mode := FromEnv(envOf(map[string]string{"GITHUB_POLL_TOKEN": "t"}))
		if mode != ModeToken || cfg.APIBase != "https://api.github.com" || cfg.Interval != DefaultInterval {
			t.Fatalf("cfg = %+v, mode = %q", cfg, mode)
		}
	})
	for in, want := range map[string]time.Duration{"14s": MinInterval, "0s": DefaultInterval, "1h": time.Hour, "250ms": MinInterval} {
		t.Run("AG patch: interval edge "+in, func(t *testing.T) {
			cfg, _ := FromEnv(envOf(map[string]string{"GITHUB_POLL_TOKEN": "t", "GITHUB_POLL_INTERVAL": in}))
			if cfg.Interval != want {
				t.Fatalf("interval = %s, want %s", cfg.Interval, want)
			}
		})
	}
}

func TestAGPatchGHPollNeverLogsTheToken(t *testing.T) {
	const secret = "ghp_SuperSecretValue123"
	failures := map[string]func(w http.ResponseWriter, r *http.Request){
		"401":       func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"403":       func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) },
		"502":       func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) },
		"malformed": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not a list")) },
		"rate limit": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(403)
		},
		"dropped connection": func(w http.ResponseWriter, r *http.Request) {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		},
		"echoes the auth header": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(r.Header.Get("Authorization")))
		},
	}
	for name, h := range failures {
		t.Run("AG patch: "+name+" never puts the token or its prefix in the log", func(t *testing.T) {
			logs := captureLogs(t)
			srv := httptest.NewServer(http.HandlerFunc(h))
			defer srv.Close()
			p := New(Config{Token: secret, APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"o", "r"}, Workspaces: testWS}}, &fakeSink{})
			p.Tick(context.Background())
			out := logs.all()
			if out == "" {
				t.Fatal("expected the failure to be logged")
			}
			for _, leak := range []string{secret, "ghp_", "SuperSecret", "Bearer"} {
				if strings.Contains(out, leak) {
					t.Fatalf("log leaked %q:\n%s", leak, out)
				}
			}
		})
	}
}

func TestAGPatchGHPollAuthFailureLogsOncePerTick(t *testing.T) {
	logs := captureLogs(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if strings.Contains(r.URL.Path, "/good") {
			_, _ = w.Write([]byte("[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	sink := &fakeSink{}
	p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{
		{Repo: RepoRef{"o", "bad1"}, Workspaces: testWS},
		{Repo: RepoRef{"o", "bad2"}, Workspaces: testWS},
		{Repo: RepoRef{"o", "bad3"}, Workspaces: testWS},
		{Repo: RepoRef{"o", "good"}, Workspaces: testWS},
	}, sink)

	for tick := 1; tick <= 2; tick++ {
		logs.reset()
		before := atomic.LoadInt32(&calls)
		p.Tick(context.Background())
		if got := atomic.LoadInt32(&calls) - before; got != 4 {
			t.Fatalf("tick %d: %d requests, want one per repo (4)", tick, got)
		}
		if n := logs.count(slog.LevelError); n != 1 {
			t.Fatalf("tick %d: %d error logs, want exactly 1:\n%s", tick, n, logs.all())
		}
	}
	if len(sink.take()) == 0 {
		t.Fatal("the healthy repo was never mirrored")
	}
}

func TestAGPatchGHPollRateLimitVariants(t *testing.T) {
	t.Run("AG patch: a secondary limit with Retry-After pauses for that long", func(t *testing.T) {
		g := &fakeGitHub{status: 403, headers: map[string]string{"Retry-After": "120"}}
		p, clock := newPoller(t, g, &fakeSink{})
		p.Tick(context.Background())
		*clock = clock.Add(time.Minute)
		p.Tick(context.Background())
		if g.count() != 1 {
			t.Fatalf("polled inside the Retry-After window: %d requests", g.count())
		}
		*clock = clock.Add(90 * time.Second)
		p.Tick(context.Background())
		if g.count() != 2 {
			t.Fatalf("did not resume once Retry-After passed: %d requests", g.count())
		}
	})

	t.Run("AG patch: a secondary limit named only in the body pauses", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
		}))
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"o", "r"}, Workspaces: testWS}}, &fakeSink{})
		p.Tick(context.Background())
		p.Tick(context.Background())
		if hits != 1 {
			t.Fatalf("requests = %d, want 1 (second tick paused)", hits)
		}
	})

	t.Run("AG patch: a rate limit stops the rest of the tick", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(403)
		}))
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{
			{Repo: RepoRef{"o", "a"}, Workspaces: testWS}, {Repo: RepoRef{"o", "b"}, Workspaces: testWS},
		}, &fakeSink{})
		p.Tick(context.Background())
		if hits != 1 {
			t.Fatalf("requests = %d, want 1: the second repo must wait for the reset", hits)
		}
	})

	t.Run("AG patch: a distant reset is capped at an hour", func(t *testing.T) {
		g := &fakeGitHub{status: 403, headers: map[string]string{"Retry-After": "999999"}}
		p, clock := newPoller(t, g, &fakeSink{})
		p.Tick(context.Background())
		*clock = clock.Add(61 * time.Minute)
		p.Tick(context.Background())
		if g.count() != 2 {
			t.Fatalf("still paused after the one-hour cap: %d requests", g.count())
		}
	})

	t.Run("AG patch: a pause keeps each repo's cursor and ETag", func(t *testing.T) {
		g := &fakeGitHub{etag: `"e"`, pages: map[string]string{"1": "[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{}
		p, clock := newPoller(t, g, sink)
		p.Tick(context.Background())
		sink.take()
		before := *p.state["org/repo"]
		g.mu.Lock()
		g.status, g.headers = 403, map[string]string{"X-RateLimit-Remaining": "0"}
		g.mu.Unlock()
		p.Tick(context.Background())
		if *p.state["org/repo"] != before {
			t.Fatalf("state changed by a rate-limit response")
		}
		g.mu.Lock()
		g.status, g.headers = 0, nil
		g.mu.Unlock()
		*clock = clock.Add(2 * time.Minute)
		p.Tick(context.Background())
		if got := g.requests[len(g.requests)-1].Header.Get("If-None-Match"); got != `"e"` {
			t.Fatalf("after the pause If-None-Match = %q, want the kept ETag", got)
		}
	})
}

func TestAGPatchGHPollTransientFailuresKeepState(t *testing.T) {
	var mode atomic.Value
	mode.Store("ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load().(string) {
		case "drop":
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		case "garbage":
			w.Header().Set("ETag", `"garbage"`)
			_, _ = w.Write([]byte("<html>"))
		case "5xx":
			w.WriteHeader(503)
		default:
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte("[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"))
		}
	}))
	defer srv.Close()
	sink := &fakeSink{}
	p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"org", "repo"}, Workspaces: testWS}}, sink)
	p.Tick(context.Background())
	if len(sink.take()) != 1 {
		t.Fatal("first pass should mirror the PR")
	}
	want := *p.state["org/repo"]
	if want.etag != `"v1"` || want.cursor.IsZero() {
		t.Fatalf("state after the first pass = %+v", want)
	}

	for _, m := range []string{"drop", "garbage", "5xx"} {
		t.Run("AG patch: "+m+" keeps the cursor and ETag and the next tick recovers", func(t *testing.T) {
			mode.Store(m)
			p.Tick(context.Background())
			if *p.state["org/repo"] != want {
				t.Fatalf("state = %+v, want unchanged %+v", *p.state["org/repo"], want)
			}
			if n := len(sink.take()); n != 0 {
				t.Fatalf("mirrored %d PRs from a failed list", n)
			}
			mode.Store("ok")
			p.Tick(context.Background()) // 304 with the kept ETag
			if *p.state["org/repo"] != want {
				t.Fatalf("recovery changed the state: %+v", *p.state["org/repo"])
			}
		})
	}
}

func TestAGPatchGHPollMirrorFailureIsolation(t *testing.T) {
	t.Run("AG patch: a failed older PR holds the cursor behind it even when a newer one mirrored", func(t *testing.T) {
		g := &fakeGitHub{etag: `"e"`, pages: map[string]string{"1": "[" + prJSON(2, "2026-10-16T11:00:00Z") + "," + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{failOn: map[int32]bool{1: true}}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#2" {
			t.Fatalf("mirrored %q", got)
		}
		st := p.state["org/repo"]
		if st.etag != "" {
			t.Fatalf("etag kept after a failed mirror: %q", st.etag)
		}
		if !st.cursor.Before(time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("cursor %s moved to or past the failed PR (10:00)", st.cursor)
		}

		sink.failOn = nil
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); !strings.Contains(got, "org/repo#1") {
			t.Fatalf("the failed PR was not retried: %q", got)
		}
		if !st.cursor.Equal(time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)) {
			t.Fatalf("cursor = %s after the retry, want 11:00", st.cursor)
		}
	})

	t.Run("AG patch: a failing repo does not stop mirroring in the next repo", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := 7
			if strings.Contains(r.URL.Path, "/b/") {
				n = 8
			}
			_, _ = w.Write([]byte("[" + prJSON(n, "2026-10-16T10:00:00Z") + "]"))
		}))
		defer srv.Close()
		sink := &fakeSink{failOn: map[int32]bool{7: true}}
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{
			{Repo: RepoRef{"o", "a"}, Workspaces: testWS}, {Repo: RepoRef{"o", "b"}, Workspaces: testWS},
		}, sink)
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#8" {
			t.Fatalf("mirrored %q, want only repo b's PR", got)
		}
	})

	t.Run("AG patch: the sink gets every workspace that lists the repo", func(t *testing.T) {
		a, b := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
		g := &fakeGitHub{pages: map[string]string{"1": "[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		var seen [][]pgtype.UUID
		sink := &workspaceSink{onMirror: func(ws []pgtype.UUID) { seen = append(seen, ws) }}
		srv := httptest.NewServer(g)
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"org", "repo"}, Workspaces: []pgtype.UUID{a, b}}}, sink)
		p.Tick(context.Background())
		if len(seen) != 1 || len(seen[0]) != 2 || seen[0][0] != a || seen[0][1] != b {
			t.Fatalf("workspaces handed to Mirror = %v", seen)
		}
	})
}

type workspaceSink struct{ onMirror func([]pgtype.UUID) }

func (s *workspaceSink) Mirror(_ context.Context, ws []pgtype.UUID, _ PR) error {
	s.onMirror(ws)
	return nil
}
func (s *workspaceSink) Refresh(RepoRef, int32) {}

func TestAGPatchGHPollETagAcrossPagesAndRepos(t *testing.T) {
	full := func(from int, updated string) string {
		var items []string
		for i := 0; i < perPage; i++ {
			items = append(items, prJSON(from-i, updated))
		}
		return "[" + strings.Join(items, ",") + "]"
	}

	t.Run("AG patch: only the first page carries If-None-Match and a fresh ETag replaces the old one", func(t *testing.T) {
		g := &fakeGitHub{
			etag:    `"v1"`,
			headers: map[string]string{"Link": `<http://x/?page=2>; rel="next"`},
			pages:   map[string]string{"1": full(500, "2026-10-16T11:00:00Z"), "2": "[" + prJSON(1, "2026-10-16T09:00:00Z") + "]"},
		}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background()) // first pass: one page, stores v1
		sink.take()

		g.mu.Lock()
		g.etag = `"v2"`
		g.mu.Unlock()
		before := g.count()
		p.Tick(context.Background()) // v1 no longer matches: 200 with v2, then page 2
		reqs := g.requests[before:]
		if len(reqs) != 2 {
			t.Fatalf("requests = %d, want page 1 and the page that holds the older PRs", len(reqs))
		}
		if reqs[0].Header.Get("If-None-Match") != `"v1"` {
			t.Fatalf("page 1 If-None-Match = %q", reqs[0].Header.Get("If-None-Match"))
		}
		if reqs[1].URL.Query().Get("page") != "2" || reqs[1].Header.Get("If-None-Match") != "" {
			t.Fatalf("page 2 request = %s with If-None-Match %q", reqs[1].URL, reqs[1].Header.Get("If-None-Match"))
		}
		if p.state["org/repo"].etag != `"v2"` {
			t.Fatalf("stored etag = %q, want the page-1 ETag", p.state["org/repo"].etag)
		}

		before = g.count()
		p.Tick(context.Background())
		if g.count()-before != 1 || g.requests[before].Header.Get("If-None-Match") != `"v2"` {
			t.Fatalf("the next tick should send v2 and stop on the 304")
		}
	})

	t.Run("AG patch: each repo keeps its own ETag and cursor", func(t *testing.T) {
		var mu sync.Mutex
		seen := map[string][]string{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[r.URL.Path] = append(seen[r.URL.Path], r.Header.Get("If-None-Match"))
			mu.Unlock()
			tag := `"` + strings.ReplaceAll(r.URL.Path, "/", "-") + `"`
			if r.Header.Get("If-None-Match") == tag {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", tag)
			_, _ = w.Write([]byte("[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"))
		}))
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{
			{Repo: RepoRef{"o", "a"}, Workspaces: testWS}, {Repo: RepoRef{"o", "b"}, Workspaces: testWS},
		}, &fakeSink{})
		p.Tick(context.Background())
		p.Tick(context.Background())
		mu.Lock()
		defer mu.Unlock()
		for _, path := range []string{"/repos/o/a/pulls", "/repos/o/b/pulls"} {
			want := `"` + strings.ReplaceAll(path, "/", "-") + `"`
			if got := seen[path]; len(got) != 2 || got[0] != "" || got[1] != want {
				t.Fatalf("%s If-None-Match sequence = %q, want [\"\" %s]", path, got, want)
			}
		}
	})
}

func TestAGPatchGHPollPagingCaps(t *testing.T) {
	t.Run("AG patch: a repo with endless newer pages is cut at the page cap and the cursor still advances", func(t *testing.T) {
		pages := map[string]string{}
		for i := 1; i <= maxPages+3; i++ {
			var items []string
			for j := 0; j < perPage; j++ {
				items = append(items, prJSON(i*1000+j, "2026-10-16T11:00:00Z"))
			}
			pages[fmt.Sprint(i)] = "[" + strings.Join(items, ",") + "]"
		}
		g := &fakeGitHub{headers: map[string]string{"Link": `<http://x/?page=2>; rel="next"`}, pages: pages}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background()) // first pass: one page
		sink.take()
		p.state["org/repo"].cursor = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
		before := g.count()
		p.Tick(context.Background())
		if got := g.count() - before; got != maxPages {
			t.Fatalf("fetched %d pages, want the cap %d", got, maxPages)
		}
		if !p.state["org/repo"].cursor.Equal(time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)) {
			t.Fatalf("cursor = %s", p.state["org/repo"].cursor)
		}
	})

	t.Run("AG patch: a short page ends paging even when Link claims a next page", func(t *testing.T) {
		g := &fakeGitHub{
			headers: map[string]string{"Link": `<http://x/?page=2>; rel="next"`},
			pages:   map[string]string{"1": "[" + prJSON(1, "2026-10-16T11:00:00Z") + "]", "2": "[" + prJSON(2, "2026-10-16T11:00:00Z") + "]"},
		}
		p, _ := newPoller(t, g, &fakeSink{})
		p.Tick(context.Background())
		p.Tick(context.Background())
		if g.count() != 2 {
			t.Fatalf("requests = %d, want one per tick", g.count())
		}
	})
}

func TestAGPatchGHPollListHandling(t *testing.T) {
	t.Run("AG patch: the PR is mirrored under the repo GitHub reports, which may differ from the watched name", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": `[{"number":4,"updated_at":"2026-10-16T10:00:00Z","base":{"repo":{"name":"Renamed","owner":{"login":"NewOwner"}}}}]`}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		refreshed := append([]string(nil), sink.refreshed...)
		if got := strings.Join(sink.take(), ","); got != "newowner/renamed#4" {
			t.Fatalf("mirrored %q", got)
		}
		if len(refreshed) != 1 || refreshed[0] != "newowner/renamed#4" {
			t.Fatalf("refreshed %v", refreshed)
		}
	})

	t.Run("AG patch: items without a number or a parseable updated_at are skipped, not fatal", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": `[` +
			`{"number":0,"updated_at":"2026-10-16T10:00:00Z"},` +
			`{"number":5,"updated_at":"yesterday"},` +
			`"not an object",` +
			prJSON(6, "2026-10-16T10:00:00Z") + `]`}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#6" {
			t.Fatalf("mirrored %q", got)
		}
	})

	t.Run("AG patch: an empty repo polls cleanly and picks up its first PR later", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": "[]"}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		if len(sink.take()) != 0 {
			t.Fatal("nothing to mirror yet")
		}
		g.mu.Lock()
		g.pages["1"] = "[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"
		g.mu.Unlock()
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#1" {
			t.Fatalf("mirrored %q", got)
		}
	})

	t.Run("AG patch: a PR that was merged and an open one both flow through, oldest first", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": "[" +
			prJSON(3, "2026-10-16T12:00:00Z") + "," + prJSON(2, "2026-10-16T11:00:00Z") + "," + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#1,org/repo#2,org/repo#3" {
			t.Fatalf("order = %q", got)
		}
	})

	t.Run("AG patch: owner and name are escaped into the request path", func(t *testing.T) {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.EscapedPath()
			_, _ = w.Write([]byte("[]"))
		}))
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL + "/"}, fakeSource{{Repo: RepoRef{"my org", "re/po"}, Workspaces: testWS}}, &fakeSink{})
		p.Tick(context.Background())
		if path != "/repos/my%20org/re%2Fpo/pulls" {
			t.Fatalf("path = %q", path)
		}
	})
}

type countingSource struct {
	calls int32
	err   error
	watch []Watch
}

func (s *countingSource) Watched(context.Context) ([]Watch, error) {
	atomic.AddInt32(&s.calls, 1)
	return s.watch, s.err
}

func TestAGPatchGHPollRunLoop(t *testing.T) {
	t.Run("AG patch: Run polls at once, repeats every interval and returns when the context ends", func(t *testing.T) {
		src := &countingSource{}
		p := New(Config{Token: "t", Interval: 10 * time.Millisecond, APIBase: "http://127.0.0.1:1"}, src, &fakeSink{})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { p.Run(ctx); close(done) }()
		deadline := time.After(5 * time.Second)
		for atomic.LoadInt32(&src.calls) < 3 {
			select {
			case <-deadline:
				t.Fatalf("only %d polls", atomic.LoadInt32(&src.calls))
			case <-time.After(5 * time.Millisecond):
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})

	t.Run("AG patch: a failing workspace query is logged and makes no GitHub calls", func(t *testing.T) {
		logs := captureLogs(t)
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
		defer srv.Close()
		p := New(Config{Token: "t", APIBase: srv.URL}, &countingSource{err: fmt.Errorf("db down")}, &fakeSink{})
		p.Tick(context.Background())
		if hits != 0 || logs.count(slog.LevelWarn) != 1 {
			t.Fatalf("hits = %d, warn logs = %d", hits, logs.count(slog.LevelWarn))
		}
	})

	t.Run("AG patch: a cancelled context stops the tick between repos", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"o", "a"}, Workspaces: testWS}}, &fakeSink{})
		p.Tick(ctx)
		if hits != 0 {
			t.Fatalf("polled with a dead context: %d", hits)
		}
	})

	t.Run("AG patch: a panic in the sink is contained and the next tick still runs", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": "[" + prJSON(9, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{panicOn: 9}
		p, _ := newPoller(t, g, sink)
		p.safeTick(context.Background())
		p.safeTick(context.Background())
		if g.count() != 2 {
			t.Fatalf("requests = %d, want the loop to keep polling after a panic", g.count())
		}
	})
}

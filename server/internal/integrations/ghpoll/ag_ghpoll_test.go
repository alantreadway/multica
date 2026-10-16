package ghpoll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAGPatchGHPollModeMatrix(t *testing.T) {
	app := map[string]string{"GITHUB_APP_ID": "1", "GITHUB_APP_PRIVATE_KEY": "key"}
	with := func(base map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	cases := []struct {
		name string
		env  map[string]string
		want Mode
	}{
		{"AG patch: neither App nor token runs nothing", map[string]string{}, ModeOff},
		{"AG patch: token only polls", with(nil, "GITHUB_POLL_TOKEN", "t"), ModeToken},
		{"AG patch: whitespace token counts as unset", with(nil, "GITHUB_POLL_TOKEN", "  "), ModeOff},
		{"AG patch: ENABLED without a token stays off", with(nil, "GITHUB_POLL_ENABLED", "true"), ModeOff},
		{"AG patch: App alone leaves the poller off", app, ModeOff},
		{"AG patch: App plus token without ENABLED stays off", with(app, "GITHUB_POLL_TOKEN", "t"), ModeOff},
		{"AG patch: App plus token and ENABLED=false stays off", with(app, "GITHUB_POLL_TOKEN", "t", "GITHUB_POLL_ENABLED", "false"), ModeOff},
		{"AG patch: App plus token and ENABLED=true opts in", with(app, "GITHUB_POLL_TOKEN", "t", "GITHUB_POLL_ENABLED", "true"), ModeAppOptIn},
		{"AG patch: a half-set App is not an App", with(nil, "GITHUB_APP_ID", "1", "GITHUB_POLL_TOKEN", "t"), ModeToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, mode := FromEnv(envOf(c.env))
			if mode != c.want {
				t.Fatalf("mode = %q, want %q", mode, c.want)
			}
			if mode != ModeOff && cfg.Token != strings.TrimSpace(c.env["GITHUB_POLL_TOKEN"]) {
				t.Fatalf("token not carried into config")
			}
		})
	}
}

func TestAGPatchGHPollInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      DefaultInterval,
		"2m":    2 * time.Minute,
		"1s":    MinInterval,
		"nope":  DefaultInterval,
		"-5m":   DefaultInterval,
		"15s":   15 * time.Second,
		" 90s ": 90 * time.Second,
	} {
		t.Run("AG patch: interval "+in, func(t *testing.T) {
			cfg, _ := FromEnv(envOf(map[string]string{"GITHUB_POLL_TOKEN": "t", "GITHUB_POLL_INTERVAL": in}))
			if cfg.Interval != want {
				t.Fatalf("interval = %s, want %s", cfg.Interval, want)
			}
		})
	}
}

func TestAGPatchGHPollRepoParsing(t *testing.T) {
	good := map[string]RepoRef{
		"https://github.com/ag-grid/ag-charts":       {"ag-grid", "ag-charts"},
		"https://github.com/ag-grid/ag-charts.git":   {"ag-grid", "ag-charts"},
		"https://www.github.com/ag-grid/ag-charts/":  {"ag-grid", "ag-charts"},
		"git@github.com:ag-grid/ag-charts.git":       {"ag-grid", "ag-charts"},
		"ssh://git@github.com/ag-grid/ag-charts.git": {"ag-grid", "ag-charts"},
		" https://github.com/Foo/Bar.js.git ":        {"Foo", "Bar.js"},
	}
	for in, want := range good {
		t.Run("AG patch: parses "+in, func(t *testing.T) {
			got, ok := ParseGitHubRepo(in)
			if !ok || got != want {
				t.Fatalf("got %v %v, want %v", got, ok, want)
			}
		})
	}
	for _, in := range []string{"", "not a url", "https://gitlab.com/a/b", "https://github.com/onlyowner", "git@gitlab.com:a/b.git", "https://github.com/a/b/c/d"} {
		t.Run("AG patch: skips "+in, func(t *testing.T) {
			if got, ok := ParseGitHubRepo(in); ok {
				t.Fatalf("parsed %v", got)
			}
		})
	}
}

func TestAGPatchGHPollGroupWatches(t *testing.T) {
	a, b := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	got := GroupWatches([]WorkspaceRepos{
		{a, []byte(`[{"url":"https://github.com/Org/Repo"},{"url":"https://gitlab.com/x/y"},{"url":"https://github.com/org/repo.git"}]`)},
		{b, []byte(`[{"url":"https://github.com/org/repo"},{"url":"https://github.com/org/other"}]`)},
		{b, []byte(`not json`)},
	})
	if len(got) != 2 || got[0].Repo.key() != "org/other" || got[1].Repo.key() != "org/repo" {
		t.Fatalf("watches = %+v", got)
	}
	if len(got[1].Workspaces) != 2 || len(got[0].Workspaces) != 1 {
		t.Fatalf("workspaces were not de-duplicated per repo: %+v", got)
	}
}

// ── poller against an httptest GitHub ───────────────────────────────────────

type fakeSource []Watch

func (f fakeSource) Watched(context.Context) ([]Watch, error) { return f, nil }

type fakeSink struct {
	mu        sync.Mutex
	mirrored  []string
	refreshed []string
	failOn    map[int32]bool
	panicOn   int32
}

func (s *fakeSink) Mirror(_ context.Context, ws []pgtype.UUID, pr PR) error {
	if pr.Number == s.panicOn {
		panic("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn[pr.Number] {
		return errors.New("mirror failed")
	}
	s.mirrored = append(s.mirrored, fmt.Sprintf("%s#%d", pr.Repo.key(), pr.Number))
	return nil
}

func (s *fakeSink) Refresh(repo RepoRef, n int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshed = append(s.refreshed, fmt.Sprintf("%s#%d", repo.key(), n))
}

func (s *fakeSink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.mirrored
	s.mirrored, s.refreshed = nil, nil
	return out
}

func prJSON(number int, updated string) string {
	return fmt.Sprintf(`{"number":%d,"updated_at":%q,"base":{"repo":{"name":"Repo","owner":{"login":"Org"}}}}`, number, updated)
}

type fakeGitHub struct {
	mu       sync.Mutex
	pages    map[string]string // page number -> body
	etag     string
	status   int
	headers  map[string]string
	requests []*http.Request
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r)
	for k, v := range g.headers {
		w.Header().Set(k, v)
	}
	if g.status != 0 {
		w.WriteHeader(g.status)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
		return
	}
	page := r.URL.Query().Get("page")
	if page == "1" && g.etag != "" {
		if r.Header.Get("If-None-Match") == g.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", g.etag)
	}
	body, ok := g.pages[page]
	if !ok {
		body = "[]"
	}
	_, _ = w.Write([]byte(body))
}

func (g *fakeGitHub) count() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.requests) }

func newPoller(t *testing.T, g *fakeGitHub, sink *fakeSink, watches ...Watch) (*Poller, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	if len(watches) == 0 {
		watches = []Watch{{Repo: RepoRef{"org", "repo"}, Workspaces: []pgtype.UUID{{Bytes: [16]byte{1}, Valid: true}}}}
	}
	p := New(Config{Token: "ghp_secret", Interval: time.Minute, APIBase: srv.URL}, fakeSource(watches), sink)
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	return p, &clock
}

func TestAGPatchGHPollFirstPassAndCursor(t *testing.T) {
	g := &fakeGitHub{pages: map[string]string{"1": "[" + prJSON(3, "2026-10-16T11:00:00Z") + "," + prJSON(2, "2026-10-16T10:00:00Z") + "]"}}
	sink := &fakeSink{}
	p, _ := newPoller(t, g, sink)

	t.Run("AG patch: the first pass mirrors the page oldest first, refreshes each, and sends the token", func(t *testing.T) {
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); got != "org/repo#2,org/repo#3" {
			t.Fatalf("mirrored %s", got)
		}
		r := g.requests[0]
		if r.Header.Get("Authorization") != "Bearer ghp_secret" || r.URL.Path != "/repos/org/repo/pulls" {
			t.Fatalf("bad request %s %v", r.URL, r.Header)
		}
		q := r.URL.Query()
		if q.Get("state") != "all" || q.Get("sort") != "updated" || q.Get("direction") != "desc" {
			t.Fatalf("bad query %s", r.URL.RawQuery)
		}
	})

	t.Run("AG patch: later ticks mirror only PRs at or after the cursor", func(t *testing.T) {
		g.pages["1"] = "[" + prJSON(4, "2026-10-16T11:30:00Z") + "," + prJSON(3, "2026-10-16T11:00:00Z") + "," + prJSON(2, "2026-10-16T10:00:00Z") + "]"
		p.Tick(context.Background())
		// #3 sits exactly on the cursor second and is re-mirrored on purpose.
		if got := strings.Join(sink.take(), ","); got != "org/repo#3,org/repo#4" {
			t.Fatalf("mirrored %s", got)
		}
	})
}

func TestAGPatchGHPollETag(t *testing.T) {
	g := &fakeGitHub{etag: `"abc"`, pages: map[string]string{"1": "[" + prJSON(1, "2026-10-16T11:00:00Z") + "]"}}
	sink := &fakeSink{}
	p, _ := newPoller(t, g, sink)
	p.Tick(context.Background())
	sink.take()

	t.Run("AG patch: an unchanged list answers 304 and does nothing", func(t *testing.T) {
		p.Tick(context.Background())
		if got := g.requests[len(g.requests)-1].Header.Get("If-None-Match"); got != `"abc"` {
			t.Fatalf("If-None-Match = %q", got)
		}
		if n := len(sink.take()); n != 0 {
			t.Fatalf("a 304 mirrored %d PRs", n)
		}
	})
}

func TestAGPatchGHPollPaging(t *testing.T) {
	full := func(from, count int, updated string) string {
		var items []string
		for i := 0; i < count; i++ {
			items = append(items, prJSON(from-i, updated))
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	g := &fakeGitHub{
		headers: map[string]string{"Link": `<http://x/?page=2>; rel="next"`},
		pages: map[string]string{
			"1": full(300, 100, "2026-10-16T11:00:00Z"),
			"2": full(200, 100, "2026-10-16T10:00:00Z"),
			"3": full(100, 100, "2026-10-16T09:00:00Z"),
		},
	}
	sink := &fakeSink{}
	p, _ := newPoller(t, g, sink)

	t.Run("AG patch: the first pass reads one page only", func(t *testing.T) {
		p.Tick(context.Background())
		if g.count() != 1 || len(sink.take()) != 100 {
			t.Fatalf("requests = %d", g.count())
		}
	})

	t.Run("AG patch: paging continues while PRs are newer and stops at the first older one", func(t *testing.T) {
		p.state["org/repo"].cursor = time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
		p.state["org/repo"].etag = ""
		before := g.count()
		p.Tick(context.Background())
		if got := g.count() - before; got != 3 {
			t.Fatalf("fetched %d pages, want 3 (two full pages, then the page holding the older PRs)", got)
		}
		if n := len(sink.take()); n != 200 {
			t.Fatalf("mirrored %d, want 200", n)
		}
	})
}

func TestAGPatchGHPollFailurePolicy(t *testing.T) {
	t.Run("AG patch: a failed mirror holds the cursor and drops the ETag so it retries", func(t *testing.T) {
		g := &fakeGitHub{etag: `"e"`, pages: map[string]string{"1": "[" + prJSON(2, "2026-10-16T11:00:00Z") + "," + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{failOn: map[int32]bool{2: true}}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		if got := sink.take(); len(got) != 1 || got[0] != "org/repo#1" {
			t.Fatalf("mirrored %v", got)
		}
		if len(sink.refreshed) != 0 && strings.Contains(strings.Join(sink.refreshed, ","), "#2") {
			t.Fatalf("a failed PR must not be refreshed")
		}
		sink.failOn = nil
		p.Tick(context.Background())
		if got := strings.Join(sink.take(), ","); !strings.Contains(got, "org/repo#2") {
			t.Fatalf("PR 2 was not retried: %s", got)
		}
	})

	t.Run("AG patch: a 5xx skips the repo and keeps its cursor and ETag", func(t *testing.T) {
		g := &fakeGitHub{etag: `"e"`, pages: map[string]string{"1": "[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		sink.take()
		st := *p.state["org/repo"]
		g.mu.Lock()
		g.status = 502
		g.mu.Unlock()
		p.Tick(context.Background())
		if *p.state["org/repo"] != st {
			t.Fatalf("state changed on a 5xx")
		}
		g.mu.Lock()
		g.status = 0
		g.mu.Unlock()
		p.Tick(context.Background()) // 304 again: still works
	})

	t.Run("AG patch: a 401 on one repo does not stop the others", func(t *testing.T) {
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.URL.Path)
			if strings.Contains(r.URL.Path, "/bad/") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("[" + prJSON(5, "2026-10-16T10:00:00Z") + "]"))
		}))
		defer srv.Close()
		sink := &fakeSink{}
		ws := []pgtype.UUID{{Bytes: [16]byte{1}, Valid: true}}
		p := New(Config{Token: "t", APIBase: srv.URL}, fakeSource{{Repo: RepoRef{"o", "bad"}, Workspaces: ws}, {Repo: RepoRef{"o", "good"}, Workspaces: ws}}, sink)
		p.Tick(context.Background())
		if len(seen) != 2 || len(sink.take()) != 1 {
			t.Fatalf("seen %v", seen)
		}
	})

	t.Run("AG patch: a rate-limit response pauses the poller until the reset", func(t *testing.T) {
		g := &fakeGitHub{status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(time.Date(2026, 10, 16, 12, 10, 0, 0, time.UTC).Unix())}}
		sink := &fakeSink{}
		p, clock := newPoller(t, g, sink)
		p.Tick(context.Background())
		if g.count() != 1 {
			t.Fatalf("requests = %d", g.count())
		}
		p.Tick(context.Background())
		if g.count() != 1 {
			t.Fatalf("polled while paused")
		}
		*clock = clock.Add(11 * time.Minute)
		g.mu.Lock()
		g.status, g.headers = 0, nil
		g.mu.Unlock()
		p.Tick(context.Background())
		if g.count() != 2 {
			t.Fatalf("did not resume after the reset")
		}
	})

	t.Run("AG patch: a plain 403 is an auth error, not a pause", func(t *testing.T) {
		g := &fakeGitHub{status: 403}
		p, _ := newPoller(t, g, &fakeSink{})
		p.Tick(context.Background())
		p.Tick(context.Background())
		if g.count() != 2 {
			t.Fatalf("requests = %d, want 2", g.count())
		}
	})

	t.Run("AG patch: running out of quota on a good answer pauses the next poll", func(t *testing.T) {
		g := &fakeGitHub{headers: map[string]string{"X-RateLimit-Remaining": "0", "Retry-After": "120"}, pages: map[string]string{"1": "[" + prJSON(1, "2026-10-16T10:00:00Z") + "]"}}
		sink := &fakeSink{}
		p, _ := newPoller(t, g, sink)
		p.Tick(context.Background())
		p.Tick(context.Background())
		if g.count() != 1 {
			t.Fatalf("requests = %d, want 1", g.count())
		}
	})

	t.Run("AG patch: a panic inside a tick is contained", func(t *testing.T) {
		g := &fakeGitHub{pages: map[string]string{"1": "[" + prJSON(9, "2026-10-16T10:00:00Z") + "]"}}
		p, _ := newPoller(t, g, &fakeSink{panicOn: 9})
		p.safeTick(context.Background())
	})
}

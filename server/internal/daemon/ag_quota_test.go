package daemon

// AG patch: Claude quota tracker and hold tests; TestAGPatch* with "AG patch:"
// subtests, as counted by ag-patches/test.sh.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// agQuotaResets is the resetsAt in the rate-limit fixtures (2026-10-17 21:30 UTC).
var agQuotaResets = time.Unix(1792272600, 0).UTC()

type agQuotaClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *agQuotaClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *agQuotaClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// agQuotaClaimServer records the runtime_ids of every batch claim.
type agQuotaClaimServer struct {
	*httptest.Server
	mu     sync.Mutex
	claims [][]string
}

func (s *agQuotaClaimServer) claimed() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.claims...)
}

func newAGQuotaClaimServer(t *testing.T) *agQuotaClaimServer {
	t.Helper()
	s := &agQuotaClaimServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim") {
			var body struct {
				RuntimeIDs []string `json:"runtime_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.claims = append(s.claims, body.RuntimeIDs)
			s.mu.Unlock()
			w.Write([]byte(`{"tasks":[]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// newAGQuotaDaemon builds a daemon with one Claude runtime (rt-claude), one
// custom-profile Claude runtime (rt-claude-b) and one Codex runtime, a fake
// clock and a private state file. The default Claude account is
// <tmp>/default-claude.
func newAGQuotaDaemon(t *testing.T, serverURL string, statePath string, clock *agQuotaClock) *Daemon {
	t.Helper()
	d := New(Config{ServerBaseURL: serverURL, HeartbeatInterval: time.Hour, PollInterval: time.Hour, MaxConcurrentTasks: 2}, discardLogger())
	d.runtimeIndex["rt-claude"] = Runtime{ID: "rt-claude", Provider: "claude"}
	d.runtimeIndex["rt-claude-b"] = Runtime{ID: "rt-claude-b", Provider: "claude"}
	d.runtimeIndex["rt-codex"] = Runtime{ID: "rt-codex", Provider: "codex"}
	d.agQuota.path = statePath
	d.agQuota.now = clock.now
	return d
}

func agQuotaEnv(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "default-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

// agQuotaReplay feeds every rate_limit_event frame of a stream fixture to the
// run's observer, the way the claude backend does.
func agQuotaReplay(t *testing.T, observer func(json.RawMessage), fixture string) int {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "pkg", "agent", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var frame struct {
			Type string          `json:"type"`
			Info json.RawMessage `json:"rate_limit_info"`
		}
		if json.Unmarshal(sc.Bytes(), &frame) != nil || frame.Type != "rate_limit_event" {
			continue
		}
		observer(frame.Info)
		n++
	}
	return n
}

func agQuotaClaimAll(t *testing.T, d *Daemon) {
	t.Helper()
	// These tests are about the hold, not the pacing cap, so the limiter starts
	// at Max instead of the cold-start floor.
	d.agPace.mu.Lock()
	if l := &d.agPace.accountLocked(agQuotaAccountKey(nil)).limiter; !l.set {
		*l = agPaceLimiter{cap: d.cfg.MaxConcurrentTasks, set: true}
	}
	d.agPace.mu.Unlock()
	if _, err := d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex", "rt-claude-b"}, 2); err != nil {
		t.Fatalf("claim: %v", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAGPatchQuotaHoldsClaudeClaims(t *testing.T) {
	t.Run("AG patch: a rejected event drops Claude runtimes from the claim until resetsAt + 2 min", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)

		agQuotaClaimAll(t, d)
		if got := s.claimed(); len(got) != 1 || !sameStrings(got[0], []string{"rt-claude", "rt-codex", "rt-claude-b"}) {
			t.Fatalf("before any event the claim should list every runtime, got %v", got)
		}

		observer := agQuotaObs(d, Task{ID: "t1"}, "claude", nil)
		if n := agQuotaReplay(t, observer, "claude-code-rate-limit-rejected.jsonl"); n != 1 {
			t.Fatalf("replayed %d events, want 1", n)
		}
		agQuotaClaimAll(t, d)
		got := s.claimed()
		if len(got) != 2 || !sameStrings(got[1], []string{"rt-codex"}) {
			t.Fatalf("held: claim should list only Codex, got %v", got)
		}

		clock.set(agQuotaResets.Add(2*time.Minute - time.Second))
		agQuotaClaimAll(t, d)
		if got = s.claimed(); len(got) != 3 || !sameStrings(got[2], []string{"rt-codex"}) {
			t.Fatalf("one second before resetsAt + 2 min the hold must stand, got %v", got)
		}

		clock.set(agQuotaResets.Add(2 * time.Minute))
		agQuotaClaimAll(t, d)
		if got = s.claimed(); len(got) != 4 || !sameStrings(got[3], []string{"rt-claude", "rt-codex", "rt-claude-b"}) {
			t.Fatalf("at resetsAt + 2 min the hold must clear itself, got %v", got)
		}
	})

	t.Run("AG patch: with only Claude runtimes held, no claim reaches the server", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t1"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")

		res, err := d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-claude-b"}, 2)
		if err != nil || len(res.Tasks) != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if got := s.claimed(); len(got) != 0 {
			t.Fatalf("server was called with %v", got)
		}
	})

	t.Run("AG patch: an allowed event never holds, and neither does a non-Claude run", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t1"}, "claude", nil), "claude-code-2.1.299-rate-limit-allowed.jsonl")
		if obs := agQuotaObs(d, Task{ID: "t2"}, "codex", nil); obs != nil {
			t.Fatal("a Codex run must get no observer")
		}
		agQuotaClaimAll(t, d)
		if got := s.claimed(); len(got) != 1 || len(got[0]) != 3 {
			t.Fatalf("claim = %v, want all three runtimes", got)
		}
		h := d.agQuotaHealth().ClaudeQuota
		if h == nil || h.Held || len(h.Accounts) != 1 {
			t.Fatalf("health = %+v", h)
		}
		five := h.Accounts[0].Windows["five_hour"]
		if five.Utilization == nil || *five.Utilization != 0.07 {
			t.Fatalf("five_hour = %+v, want utilization 0.07 from the captured event", five)
		}
	})

	t.Run("AG patch: a hold on an agent's own CLAUDE_CONFIG_DIR is tracked apart and does not gate the default account", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
		other := filepath.Join(t.TempDir(), "other-claude")
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t1"}, "claude", map[string]string{"CLAUDE_CONFIG_DIR": other}), "claude-code-rate-limit-rejected.jsonl")

		agQuotaClaimAll(t, d)
		if got := s.claimed(); len(got) != 1 || len(got[0]) != 3 {
			t.Fatalf("claim = %v, want all three runtimes", got)
		}
		h := d.agQuotaHealth().ClaudeQuota
		if h == nil || h.Held || len(h.Accounts) != 1 || h.Accounts[0].Account != other || h.Accounts[0].Default || h.Accounts[0].Hold == nil || h.Accounts[0].Hold.Gating {
			t.Fatalf("health = %+v", h)
		}
	})
}

func TestAGPatchQuotaClearOverridesHold(t *testing.T) {
	agQuotaEnv(t)
	s := newAGQuotaClaimServer(t)
	clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
	d := newAGQuotaDaemon(t, s.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)
	observer := agQuotaObs(d, Task{ID: "t1"}, "claude", nil)
	agQuotaReplay(t, observer, "claude-code-rate-limit-rejected.jsonl")

	post := func() map[string]int {
		rec := httptest.NewRecorder()
		d.agQuotaClearHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quota/clear", nil))
		var out map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
		return out
	}

	t.Run("AG patch: quota clear ends the hold at once and a second clear finds nothing", func(t *testing.T) {
		clock.set(clock.now().Add(time.Minute))
		if got := post()["cleared"]; got != 1 {
			t.Fatalf("cleared = %d, want 1", got)
		}
		agQuotaClaimAll(t, d)
		if got := s.claimed(); len(got) != 1 || len(got[0]) != 3 {
			t.Fatalf("claim after clear = %v", got)
		}
		if got := post()["cleared"]; got != 0 {
			t.Fatalf("second clear = %d, want 0", got)
		}
	})

	t.Run("AG patch: a rejected event after the clear holds again", func(t *testing.T) {
		clock.set(clock.now().Add(time.Minute))
		agQuotaReplay(t, observer, "claude-code-rate-limit-rejected.jsonl")
		agQuotaClaimAll(t, d)
		got := s.claimed()
		if len(got) != 2 || !sameStrings(got[1], []string{"rt-codex"}) {
			t.Fatalf("claim = %v, want only Codex", got)
		}
	})

	t.Run("AG patch: only POST is accepted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		d.agQuotaClearHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quota/clear", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d", rec.Code)
		}
	})
}

func TestAGPatchQuotaHoldSurvivesRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "sub", "ag_quota.json")
	agQuotaEnv(t)
	s := newAGQuotaClaimServer(t)
	clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}

	first := newAGQuotaDaemon(t, s.URL, statePath, clock)
	agQuotaReplay(t, agQuotaObs(first, Task{ID: "t1"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")

	t.Run("AG patch: a fresh daemon on the same state file starts held", func(t *testing.T) {
		second := newAGQuotaDaemon(t, s.URL, statePath, clock)
		agQuotaClaimAll(t, second)
		got := s.claimed()
		if len(got) != 1 || !sameStrings(got[0], []string{"rt-codex"}) {
			t.Fatalf("claim = %v, want only Codex", got)
		}
	})

	t.Run("AG patch: the state file is written atomically and leaves no temp files", func(t *testing.T) {
		entries, err := os.ReadDir(filepath.Dir(statePath))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "ag_quota.json" {
			t.Fatalf("dir = %v", entries)
		}
		raw, _ := os.ReadFile(statePath)
		var f agQuotaFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Version != agQuotaFileVersion || len(f.Accounts) != 1 {
			t.Fatalf("state file = %s (err %v)", raw, err)
		}
	})

	t.Run("AG patch: a clear in one daemon reaches the other through the file", func(t *testing.T) {
		second := newAGQuotaDaemon(t, s.URL, statePath, clock)
		agQuotaClaimAll(t, second) // loads the hold
		before := len(s.claimed())

		clock.set(clock.now().Add(time.Minute))
		first.agQuotaClear()
		agQuotaClaimAll(t, second)
		got := s.claimed()
		if len(got) != before+1 || len(got[before]) != 3 {
			t.Fatalf("claim after the other daemon cleared = %v", got[before:])
		}
	})

	t.Run("AG patch: a restart after the reset comes back claiming", func(t *testing.T) {
		clock.set(agQuotaResets.Add(3 * time.Minute))
		third := newAGQuotaDaemon(t, s.URL, statePath, clock)
		before := len(s.claimed())
		agQuotaClaimAll(t, third)
		if got := s.claimed(); len(got[before]) != 3 {
			t.Fatalf("claim = %v", got[before:])
		}
	})
}

func TestAGPatchQuotaFailPolicy(t *testing.T) {
	newDaemon := func(t *testing.T) (*Daemon, *agQuotaClock, string) {
		agQuotaEnv(t)
		s := newAGQuotaClaimServer(t)
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		path := filepath.Join(t.TempDir(), "ag_quota.json")
		return newAGQuotaDaemon(t, s.URL, path, clock), clock, path
	}
	send := func(d *Daemon, raw string) {
		agQuotaObs(d, Task{ID: "t"}, "claude", nil)(json.RawMessage(raw))
	}
	held := func(d *Daemon) *agQuotaHold {
		_, h := d.agQuotaDefaultHold()
		return h
	}

	t.Run("AG patch: no state at all is open", func(t *testing.T) {
		d, _, path := newDaemon(t)
		if held(d) != nil {
			t.Fatal("held with no state")
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatal("reading must not create the state file")
		}
		if h := d.agQuotaHealth(); h.ClaudeQuota != nil {
			t.Fatalf("health = %+v, want no quota block", h.ClaudeQuota)
		}
	})

	t.Run("AG patch: a garbled event is dropped and counted, and the warning is rate limited", func(t *testing.T) {
		d, clock, _ := newDaemon(t)
		var buf bytes.Buffer
		d.logger = slog.New(slog.NewTextHandler(&buf, nil))
		for i := 0; i < 5; i++ {
			send(d, `{"type":"rate_limit_event","rate_limit_info":{"status":"rej`)
		}
		if held(d) != nil {
			t.Fatal("a garbled event held the account")
		}
		if got := strings.Count(buf.String(), "unreadable rate_limit_event"); got != 1 {
			t.Fatalf("warnings = %d, want 1:\n%s", got, buf.String())
		}
		if d.agQuota.garbled != 5 {
			t.Fatalf("garbled = %d, want 5", d.agQuota.garbled)
		}
		if h := d.agQuotaHealth().ClaudeQuota; h == nil || h.DroppedEvents != 5 {
			t.Fatalf("health = %+v", h)
		}
		clock.set(clock.now().Add(11 * time.Minute))
		send(d, `garbage`)
		if got := strings.Count(buf.String(), "unreadable rate_limit_event"); got != 2 {
			t.Fatalf("warnings after 10 min = %d, want 2", got)
		}
	})

	t.Run("AG patch: absent utilization leaves the window unconstrained but keeps status and resetsAt", func(t *testing.T) {
		d, _, _ := newDaemon(t)
		send(d, `{"status":"allowed","rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"resetsAt":1792272600}}}`)
		if held(d) != nil {
			t.Fatal("held")
		}
		acct := d.agQuotaHealth().ClaudeQuota.Accounts[0]
		w := acct.Windows["five_hour"]
		if w.Utilization != nil || w.ResetsAt != agQuotaResets.Format(time.RFC3339) || acct.Status != "allowed" {
			t.Fatalf("account = %+v", acct)
		}
	})

	t.Run("AG patch: rejected with an unknown or past resetsAt holds for 1h with reason reset_unknown", func(t *testing.T) {
		for name, raw := range map[string]string{
			"absent": `{"status":"rejected","rateLimitType":"five_hour"}`,
			"past":   `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1000000000}`,
		} {
			d, clock, _ := newDaemon(t)
			send(d, raw)
			h := held(d)
			if h == nil || h.Reason != agQuotaReasonResetUnknown || !h.Until.Equal(clock.now().Add(time.Hour)) {
				t.Fatalf("%s: hold = %+v", name, h)
			}
			clock.set(clock.now().Add(61 * time.Minute))
			if held(d) != nil {
				t.Fatalf("%s: the 1h hold did not expire", name)
			}
		}
	})

	t.Run("AG patch: the next event re-evaluates a reset_unknown hold", func(t *testing.T) {
		d, clock, _ := newDaemon(t)
		send(d, `{"status":"rejected","rateLimitType":"five_hour"}`)
		clock.set(clock.now().Add(time.Minute))
		send(d, `{"status":"allowed","rateLimitType":"five_hour","resetsAt":1792272600}`)
		if held(d) != nil {
			t.Fatal("an allowed event should release a hold with no known reset")
		}

		// ...and a known-reset hold is not released by an allowed event.
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		clock.set(clock.now().Add(time.Minute))
		send(d, `{"status":"allowed","rateLimitType":"seven_day","resetsAt":1792821600}`)
		if h := held(d); h == nil || h.Reason != agQuotaReasonRejected {
			t.Fatalf("hold = %+v, want the rejected hold to stand", h)
		}
	})

	t.Run("AG patch: a corrupt or unreadable state file runs on in-memory state, warns once and is healed by the next write", func(t *testing.T) {
		d, clock, path := newDaemon(t)
		var buf bytes.Buffer
		d.logger = slog.New(slog.NewTextHandler(&buf, nil))
		if err := os.WriteFile(path, []byte(`{"version":1,"accounts":{"x":`), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if held(d) != nil {
				t.Fatal("held on a corrupt file")
			}
		}
		if got := strings.Count(buf.String(), "state file unreadable"); got != 1 {
			t.Fatalf("warnings = %d, want 1:\n%s", got, buf.String())
		}
		send(d, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		if held(d) == nil {
			t.Fatal("in-memory hold lost to the corrupt file")
		}
		raw, _ := os.ReadFile(path)
		var f agQuotaFile
		if err := json.Unmarshal(raw, &f); err != nil || len(f.Accounts) != 1 {
			t.Fatalf("file not healed: %s (%v)", raw, err)
		}

		// A path that cannot be read at all (a directory) is open as well.
		d2, _, path2 := newDaemon(t)
		if err := os.MkdirAll(path2, 0o755); err != nil {
			t.Fatal(err)
		}
		send(d2, `{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}`)
		if held(d2) == nil {
			t.Fatal("in-memory hold lost when the state path is unusable")
		}
		_ = clock
	})
}

func TestAGPatchQuotaMerge(t *testing.T) {
	t0 := agQuotaResets.Add(-time.Hour)
	u := func(v float64) *float64 { return &v }

	t.Run("AG patch: each window keeps its newest observation", func(t *testing.T) {
		a := &agQuotaAccount{Windows: map[string]agQuotaWindow{
			"five_hour": {Utilization: u(0.5), ObservedAt: t0.Add(2 * time.Minute)},
			"seven_day": {Utilization: u(0.1), ObservedAt: t0},
		}, Status: "allowed", ObservedAt: t0.Add(2 * time.Minute)}
		a.merge(&agQuotaAccount{Windows: map[string]agQuotaWindow{
			"five_hour": {Utilization: u(0.2), ObservedAt: t0},
			"seven_day": {Utilization: u(0.3), ObservedAt: t0.Add(time.Minute)},
		}, Status: "allowed_warning", ObservedAt: t0.Add(time.Minute)})
		if *a.Windows["five_hour"].Utilization != 0.5 || *a.Windows["seven_day"].Utilization != 0.3 || a.Status != "allowed" {
			t.Fatalf("merged = %+v", a)
		}
	})

	t.Run("AG patch: the later of a hold and a clear wins", func(t *testing.T) {
		held := &agQuotaAccount{Hold: &agQuotaHold{Until: t0.Add(time.Hour), Reason: agQuotaReasonRejected, SetAt: t0}}
		cleared := &agQuotaAccount{ClearedAt: t0.Add(time.Minute)}
		a := &agQuotaAccount{}
		a.merge(held)
		a.merge(cleared)
		if a.activeHold(t0.Add(2*time.Minute)) != nil {
			t.Fatal("a clear after the hold must end it")
		}
		a.merge(&agQuotaAccount{Hold: &agQuotaHold{Until: t0.Add(time.Hour), Reason: agQuotaReasonRejected, SetAt: t0.Add(5 * time.Minute)}})
		if a.activeHold(t0.Add(6*time.Minute)) == nil {
			t.Fatal("a hold set after the clear must count")
		}
		b := &agQuotaAccount{}
		b.merge(cleared)
		b.merge(held)
		if b.activeHold(t0.Add(2*time.Minute)) != nil {
			t.Fatal("merge order must not matter")
		}
	})
}

func TestAGPatchQuotaRetryNotBefore(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies[r.URL.Path] = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	client := NewClient(srv.URL)
	fail := func(taskID string) map[string]any {
		t.Helper()
		if err := client.failTaskWithRetrySchedule(context.Background(), taskID, "limit", "", "", "", "", false, "", "", nil); err != nil {
			t.Fatalf("fail %s: %v", taskID, err)
		}
		mu.Lock()
		defer mu.Unlock()
		return bodies["/api/daemon/tasks/"+taskID+"/fail"]
	}

	agQuotaEnv(t)
	clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
	d := newAGQuotaDaemon(t, srv.URL, filepath.Join(t.TempDir(), "ag_quota.json"), clock)

	t.Run("AG patch: the fail body carries the resetsAt of the rejected event the run saw", func(t *testing.T) {
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "quota-t1"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")
		body := fail("quota-t1")
		if body["ag_retry_not_before"] != agQuotaResets.Format(time.RFC3339) {
			t.Fatalf("body = %v", body)
		}
		if body["error"] != "limit" {
			t.Fatalf("the rest of the body must be unchanged: %v", body)
		}
	})

	t.Run("AG patch: a run that saw only allowed events, or none, sends no hint", func(t *testing.T) {
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "quota-t2"}, "claude", nil), "claude-code-2.1.299-rate-limit-allowed.jsonl")
		if body := fail("quota-t2"); body["ag_retry_not_before"] != nil {
			t.Fatalf("body = %v", body)
		}
		if body := fail("quota-t3"); body["ag_retry_not_before"] != nil {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("AG patch: a rejection with no usable resetsAt sends no hint", func(t *testing.T) {
		// A separate account, so no resetsAt seen earlier fills the gap.
		env := map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(t.TempDir(), "unknown-reset")}
		agQuotaObs(d, Task{ID: "quota-t4"}, "claude", env)(json.RawMessage(`{"status":"rejected","rateLimitType":"five_hour"}`))
		if body := fail("quota-t4"); body["ag_retry_not_before"] != nil {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("AG patch: the hint survives a replayed report and old hints are pruned", func(t *testing.T) {
		if body := fail("quota-t1"); body["ag_retry_not_before"] == nil {
			t.Fatalf("replayed body = %v", body)
		}
		now := time.Now()
		agRecordRetryNotBefore("quota-old", now, now.Add(-2*agRetryNotBeforeTTL))
		agRecordRetryNotBefore("quota-new", now, now)
		body := map[string]any{}
		agAddRetryNotBefore(body, "quota-old")
		if len(body) != 0 {
			t.Fatalf("stale hint was not pruned: %v", body)
		}
	})
}

func TestAGPatchQuotaHealthAndStatus(t *testing.T) {
	agQuotaEnv(t)
	clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
	d := newAGQuotaDaemon(t, "http://127.0.0.1:1", filepath.Join(t.TempDir(), "ag_quota.json"), clock)
	d.cfg.DaemonID = "daemon-test"
	d.workspaces = map[string]*workspaceState{}

	t.Run("AG patch: /health has no quota block until an event arrives", func(t *testing.T) {
		if _, ok := agDrainHealthJSON(t, d)["claude_quota"]; ok {
			t.Fatal("claude_quota present with no events")
		}
	})

	t.Run("AG patch: /health reports the hold, windows and last seen", func(t *testing.T) {
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t1"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")
		quota, _ := agDrainHealthJSON(t, d)["claude_quota"].(map[string]any)
		if quota == nil || quota["held"] != true {
			t.Fatalf("claude_quota = %v", quota)
		}
		hold, _ := quota["hold"].(map[string]any)
		wantUntil := agQuotaResets.Add(agQuotaResetGrace).Format(time.RFC3339)
		if hold["until"] != wantUntil || hold["window"] != "five_hour" || hold["reason"] != "rejected" || hold["gating"] != true {
			t.Fatalf("hold = %v, want until %s", hold, wantUntil)
		}
		accounts, _ := quota["accounts"].([]any)
		acct, _ := accounts[0].(map[string]any)
		if acct["last_seen"] != clock.now().Format(time.RFC3339) || acct["default"] != true {
			t.Fatalf("account = %v", acct)
		}
	})

	t.Run("AG patch: the hold is gone from /health after the reset", func(t *testing.T) {
		clock.set(agQuotaResets.Add(3 * time.Minute))
		quota, _ := agDrainHealthJSON(t, d)["claude_quota"].(map[string]any)
		if quota == nil || quota["held"] != false || quota["hold"] != nil {
			t.Fatalf("claude_quota = %v", quota)
		}
	})
}

func TestAGPatchQuotaHoldInPollLoop(t *testing.T) {
	t.Run("AG patch: the batch poller claims nothing for a held Claude runtime and claims at once after quota clear", func(t *testing.T) {
		agQuotaEnv(t)
		s := newAGDrainClaimServer(t, false)
		d := newAGDrainPollDaemon(t, s, time.Hour)
		d.runtimeIndex["rt-1"] = Runtime{ID: "rt-1", Provider: "claude"}
		clock := &agQuotaClock{t: agQuotaResets.Add(-time.Hour)}
		d.agQuota.now = clock.now
		d.agQuota.path = filepath.Join(t.TempDir(), "ag_quota.json")
		ran := agDrainCompletingRunner(d)
		agQuotaReplay(t, agQuotaObs(d, Task{ID: "t0"}, "claude", nil), "claude-code-rate-limit-rejected.jsonl")

		wakeups := make(chan taskWakeup, 8)
		startAGDrainPollLoop(t, d, wakeups)
		for i := 0; i < 3; i++ {
			wakeups <- taskWakeup{runtimeID: "rt-1"}
		}
		waitFor(t, func() bool { return len(wakeups) == 0 }, "poller to consume the wake-ups")
		time.Sleep(100 * time.Millisecond)
		if s.claims.Load() != 0 || ran.Load() != 0 {
			t.Fatalf("work started while held: claims=%d ran=%d", s.claims.Load(), ran.Load())
		}

		clock.set(clock.now().Add(time.Minute))
		d.agQuotaClear()
		waitFor(t, func() bool { return ran.Load() == 1 && s.completed.Load() == 1 }, "queued task to run after quota clear")
	})
}

// agQuotaObs returns a run's observer with its pacing count already ended, so
// tests of the hold do not leave a Claude run counted in flight.
func agQuotaObs(d *Daemon, task Task, provider string, env map[string]string) func(json.RawMessage) {
	observe, done := d.agQuotaObserver(task, provider, env)
	done()
	return observe
}

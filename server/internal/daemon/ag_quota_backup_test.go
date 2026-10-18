package daemon

// AG patch: backup Claude account failover. Detection runs a test-created claude
// that answers `auth status --json` from a file in its CLAUDE_CONFIG_DIR; no
// installed CLI is used. The claim tests use a fake server that records bodies.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	agBackupAuthPrimary = `{"loggedIn":true,"authMethod":"claude.ai","email":"alan@primary.test","subscriptionType":"max"}`
	agBackupAuthBackup  = `{"loggedIn":true,"authMethod":"claude.ai","email":"alan@backup.test","subscriptionType":"max"}`

	// agBackupFakeClaude answers `auth status --json` from $CLAUDE_CONFIG_DIR/auth.json
	// (missing file: exit 3, no output). Any other call is a run: it logs its
	// config dir and arguments, then prints the first-run output, or the resume
	// output when it was given --resume.
	agBackupFakeClaude = `#!/bin/sh
case "$*" in
  *--version*) echo '2.1.299'; exit 0;;
  "auth status --json")
    if [ -f "$CLAUDE_CONFIG_DIR/slow" ]; then exec sleep 5; fi
    cat "$CLAUDE_CONFIG_DIR/auth.json" 2>/dev/null || exit 3
    exit 0;;
esac
echo "$CLAUDE_CONFIG_DIR $*" >> "$AG_TEST_LOG"
read -r _
case "$*" in
  *--resume*) cat "$AG_TEST_RESUME_OUT";;
  *) cat "$AG_TEST_FIRST_OUT";;
esac
`
)

// agBackupClaimServer records the batch claim bodies and legacy per-runtime claims.
type agBackupClaimServer struct {
	*httptest.Server
	mu      sync.Mutex
	batches []map[string]any
	legacy  []string
	noBatch bool
}

func (s *agBackupClaimServer) bodies() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.batches...)
}

func (s *agBackupClaimServer) legacyHits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.legacy...)
}

func newAGBackupClaimServer(t *testing.T) *agBackupClaimServer {
	t.Helper()
	s := &agBackupClaimServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/daemon/tasks/claim"):
			if s.noBatch {
				http.NotFound(w, r)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.batches = append(s.batches, body)
			s.mu.Unlock()
			w.Write([]byte(`{"tasks":[]}`))
		case strings.HasSuffix(r.URL.Path, "/tasks/claim"):
			s.mu.Lock()
			s.legacy = append(s.legacy, r.URL.Path)
			s.mu.Unlock()
			w.Write([]byte(`{"task":null}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

type agBackupFixture struct {
	*agPaceFixture
	srv       *agBackupClaimServer
	backupDir string
}

func newAGBackupFixture(t *testing.T) *agBackupFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	srv := newAGBackupClaimServer(t)
	f := &agBackupFixture{agPaceFixture: newAGPaceFixtureAt(t, 5, srv.URL), srv: srv}
	f.backupDir = filepath.Join(t.TempDir(), ".claude-backup")
	if err := os.MkdirAll(f.backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.acct, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(agBackupDirEnv, f.backupDir)
	fake := filepath.Join(t.TempDir(), "claude")
	writeTestExecutable(t, fake, []byte(agBackupFakeClaude))
	agents := map[string]AgentEntry{"claude": {Path: fake}}
	f.d.cfg.Agents = agents
	f.d.agentsAvailable.Store(&agents)
	f.setAuth(f.acct, agBackupAuthPrimary)
	f.setAuth(f.backupDir, agBackupAuthBackup)
	return f
}

func (f *agBackupFixture) setAuth(dir, body string) {
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o644); err != nil {
		panic(err)
	}
}

// ready enables the backup for rt-claude, as an ack from a patched server does,
// and runs detection.
func (f *agBackupFixture) ready(t *testing.T) {
	t.Helper()
	f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}, []string{"claim_filter"})
	f.d.agBackupCheck(context.Background())
	if st := f.d.agBackupStatusNow(); !st.Valid {
		t.Fatalf("backup not valid: %+v", st)
	}
}

// holdPrimary puts the daemon's own account on hold until 30 minutes from now.
func (f *agBackupFixture) holdPrimary(t *testing.T) {
	t.Helper()
	f.hold(t, nil)
}

func (f *agBackupFixture) hold(t *testing.T, env map[string]string) {
	t.Helper()
	resets := f.clock.now().Add(30 * time.Minute)
	raw := `{"status":"rejected","rateLimitType":"five_hour","resetsAt":` + itoa(resets.Unix()) + `}`
	agQuotaObs(f.d, Task{ID: "hold-" + itoa(int64(len(env)))}, "claude", env)(json.RawMessage(raw))
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func urgentTask() Task {
	return Task{ID: "urgent-1", RuntimeID: "rt-claude", AGIssuePriority: "urgent"}
}

func TestAGPatchBackupDetection(t *testing.T) {
	check := func(t *testing.T, f *agBackupFixture) agBackupStatus {
		t.Helper()
		f.d.agBackupCheck(context.Background())
		return f.d.agBackupStatusNow()
	}

	t.Run("AG patch: a missing backup dir is not detected and the CLI is not run", func(t *testing.T) {
		f := newAGBackupFixture(t)
		if err := os.RemoveAll(f.backupDir); err != nil {
			t.Fatal(err)
		}
		f.setAuth(f.acct, `not json`) // would be check_failed if the CLI ran
		st := check(t, f)
		if st.Detected || st.Valid || st.Reason != "not_detected" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: a logged-out backup is not_logged_in", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.setAuth(f.backupDir, `{"loggedIn":false}`)
		if st := check(t, f); !st.Detected || st.Valid || st.Reason != "not_logged_in" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: an API-key login or a missing subscription is not_logged_in", func(t *testing.T) {
		f := newAGBackupFixture(t)
		for _, body := range []string{
			`{"loggedIn":true,"authMethod":"api_key","email":"x@backup.test","subscriptionType":"max"}`,
			`{"loggedIn":true,"authMethod":"claude.ai","email":"x@backup.test"}`,
			`{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`,
		} {
			f.setAuth(f.backupDir, body)
			if st := check(t, f); st.Valid || st.Reason != "not_logged_in" {
				t.Fatalf("%s: status = %+v", body, st)
			}
		}
	})

	t.Run("AG patch: the same email as the primary is same_account, whatever the case", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.setAuth(f.backupDir, `{"loggedIn":true,"authMethod":"claude.ai","email":"ALAN@primary.test","subscriptionType":"pro"}`)
		if st := check(t, f); !st.Detected || st.Valid || st.Reason != "same_account" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: a backup dir equal to the primary dir is same_account", func(t *testing.T) {
		f := newAGBackupFixture(t)
		t.Setenv(agBackupDirEnv, f.acct)
		if st := check(t, f); st.Valid || st.Reason != "same_account" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: a failing or garbled check is check_failed, and a later good one recovers", func(t *testing.T) {
		f := newAGBackupFixture(t)
		if err := os.Remove(filepath.Join(f.backupDir, "auth.json")); err != nil {
			t.Fatal(err)
		}
		if st := check(t, f); !st.Detected || st.Valid || st.Reason != "check_failed" {
			t.Fatalf("backup check missing: %+v", st)
		}
		f.setAuth(f.backupDir, `garbage`)
		if st := check(t, f); st.Valid || st.Reason != "check_failed" {
			t.Fatalf("backup check garbled: %+v", st)
		}
		f.setAuth(f.backupDir, agBackupAuthBackup)
		f.setAuth(f.acct, `garbage`)
		if st := check(t, f); st.Valid || st.Reason != "check_failed" {
			t.Fatalf("primary check garbled: %+v", st)
		}
		f.setAuth(f.acct, agBackupAuthPrimary)
		if st := check(t, f); !st.Valid || st.Reason != "" {
			t.Fatalf("recovered status = %+v", st)
		}
	})

	t.Run("AG patch: a check that hangs is cut off as check_failed", func(t *testing.T) {
		f := newAGBackupFixture(t)
		old := agBackupCheckTimeout
		agBackupCheckTimeout = 200 * time.Millisecond
		t.Cleanup(func() { agBackupCheckTimeout = old })
		if err := os.WriteFile(filepath.Join(f.backupDir, "slow"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if st := check(t, f); st.Valid || st.Reason != "check_failed" {
			t.Fatalf("status = %+v", st)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatalf("check took %s", time.Since(start))
		}
	})

	t.Run("AG patch: no claude CLI is check_failed", func(t *testing.T) {
		f := newAGBackupFixture(t)
		none := map[string]AgentEntry{}
		f.d.cfg.Agents = none
		f.d.agentsAvailable.Store(&none)
		if st := check(t, f); !st.Detected || st.Valid || st.Reason != "check_failed" {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: a valid backup is valid, and the result goes stale when the loop stops", func(t *testing.T) {
		f := newAGBackupFixture(t)
		if st := check(t, f); !st.Detected || !st.Valid || st.Reason != "" || st.Dir != filepath.Clean(f.backupDir) {
			t.Fatalf("status = %+v", st)
		}
		f.clock.set(f.clock.now().Add(agBackupStaleAfter - time.Second))
		if !f.d.agBackupStatusNow().Valid {
			t.Fatal("a recent check went stale")
		}
		f.clock.set(f.clock.now().Add(2 * time.Second))
		if st := f.d.agBackupStatusNow(); st.Valid || st.Reason != "check_failed" {
			t.Fatalf("stale status = %+v", st)
		}
	})

	t.Run("AG patch: before the first check the backup is invalid", func(t *testing.T) {
		f := newAGBackupFixture(t)
		if st := f.d.agBackupStatusNow(); st.Valid || st.Detected {
			t.Fatalf("status = %+v", st)
		}
	})

	t.Run("AG patch: the backup dir defaults to ~/.claude-backup and the env var overrides it", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(agBackupDirEnv, "")
		if got := agBackupDir(); got != filepath.Join(home, ".claude-backup") {
			t.Fatalf("default = %s", got)
		}
		t.Setenv(agBackupDirEnv, "~/elsewhere/")
		if got := agBackupDir(); got != filepath.Join(home, "elsewhere") {
			t.Fatalf("tilde override = %s", got)
		}
		t.Setenv(agBackupDirEnv, "/srv/claude-b")
		if got := agBackupDir(); got != "/srv/claude-b" {
			t.Fatalf("override = %s", got)
		}
	})
}

func TestAGPatchBackupAckGates(t *testing.T) {
	t.Run("AG patch: usable needs the setting, a valid backup and the claim_filter capability", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.d.agBackupCheck(context.Background())
		on := &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}

		f.d.agApplyAck("rt-claude", on, []string{"claim_filter"})
		if !f.d.agBackupUsable("rt-claude") {
			t.Fatal("not usable with everything in place")
		}
		if f.d.agBackupUsable("rt-claude-b") {
			t.Fatal("another runtime picked up the setting")
		}

		f.d.agApplyAck("rt-claude", on, nil)
		if f.d.agBackupUsable("rt-claude") {
			t.Fatal("usable without the capability")
		}
		f.d.agApplyAck("rt-claude", on, []string{"something_else"})
		if f.d.agBackupUsable("rt-claude") {
			t.Fatal("usable with only an unrelated capability")
		}
		f.d.agApplyAck("rt-claude", nil, []string{"claim_filter"})
		if f.d.agBackupUsable("rt-claude") {
			t.Fatal("usable without a setting")
		}
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: 2}, []string{"claim_filter"})
		if f.d.agBackupUsable("rt-claude") {
			t.Fatal("usable while disabled")
		}
	})

	t.Run("AG patch: the capability expires after ten minutes without an ack", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.clock.set(f.clock.now().Add(agSettingsTTL - time.Second))
		if !f.d.agBackupCapability() {
			t.Fatal("capability expired early")
		}
		f.clock.set(f.clock.now().Add(2 * time.Second))
		if f.d.agBackupCapability() || f.d.agBackupUsable("rt-claude") {
			t.Fatal("capability outlived the TTL")
		}
	})

	t.Run("AG patch: a heartbeat ack carries the capability into the gate", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.d.agBackupCheck(context.Background())
		var ack HeartbeatResponse
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-claude","status":"ok","ag_capabilities":["claim_filter"],"ag_settings":{"backup_enabled":true,"backup_max_concurrent":2}}`), &ack); err != nil {
			t.Fatal(err)
		}
		f.d.handleWSHeartbeatAck(context.Background(), &ack)
		if !f.d.agBackupUsable("rt-claude") {
			t.Fatal("ack did not open the gate")
		}
		var stock HeartbeatResponse
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-claude","status":"ok"}`), &stock); err != nil {
			t.Fatal(err)
		}
		f.d.handleWSHeartbeatAck(context.Background(), &stock)
		if f.d.agBackupUsable("rt-claude") {
			t.Fatal("a stock ack left the gate open")
		}
	})

	t.Run("AG patch: a claimed task carries ag_issue_priority", func(t *testing.T) {
		var task Task
		if err := json.Unmarshal([]byte(`{"id":"t1","runtime_id":"rt-claude","ag_issue_priority":"urgent"}`), &task); err != nil {
			t.Fatal(err)
		}
		if task.AGIssuePriority != "urgent" {
			t.Fatalf("task = %+v", task)
		}
		if err := json.Unmarshal([]byte(`{"id":"t2"}`), &task); err != nil || task.ID != "t2" {
			t.Fatalf("task = %+v err %v", task, err)
		}
	})
}

func TestAGPatchBackupEnvSwap(t *testing.T) {
	swapped := func(f *agBackupFixture, task Task, env map[string]string) string {
		f.d.agApplyClaudeAccount("claude", task, env)
		return env["CLAUDE_CONFIG_DIR"]
	}

	t.Run("AG patch: an urgent task gets the backup config dir while the primary is held", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		if got := swapped(f, urgentTask(), map[string]string{}); got != filepath.Clean(f.backupDir) {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got, f.backupDir)
		}
	})

	t.Run("AG patch: it switches back to the primary at the primary's reset", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.clock.set(f.clock.now().Add(33 * time.Minute)) // resetsAt + 2 min grace has passed
		f.d.agBackupCheck(context.Background())
		if got := swapped(f, urgentTask(), map[string]string{}); got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q after the reset, want it untouched", got)
		}
	})

	t.Run("AG patch: nothing changes while the primary is not held", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		if got := swapped(f, urgentTask(), map[string]string{}); got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q", got)
		}
	})

	t.Run("AG patch: a task that is not urgent stays on the primary", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		for _, prio := range []string{"", "high", "medium", "low", "none"} {
			task := urgentTask()
			task.AGIssuePriority = prio
			if got := swapped(f, task, map[string]string{}); got != "" {
				t.Fatalf("priority %q: CLAUDE_CONFIG_DIR = %q", prio, got)
			}
		}
	})

	t.Run("AG patch: an agent's own CLAUDE_CONFIG_DIR is left alone", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		own := filepath.Join(t.TempDir(), "agent-claude")
		if got := swapped(f, urgentTask(), map[string]string{"CLAUDE_CONFIG_DIR": own}); got != own {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q, want the agent's %q", got, own)
		}
	})

	t.Run("AG patch: a non-Claude provider is left alone", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		env := map[string]string{}
		f.d.agApplyClaudeAccount("codex", urgentTask(), env)
		if len(env) != 0 {
			t.Fatalf("env = %v", env)
		}
	})

	t.Run("AG patch: no swap when the backup is disabled, invalid or the server cannot filter", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: 2}, []string{"claim_filter"})
		if got := swapped(f, urgentTask(), map[string]string{}); got != "" {
			t.Fatalf("disabled: %q", got)
		}
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}, nil)
		if got := swapped(f, urgentTask(), map[string]string{}); got != "" {
			t.Fatalf("no capability: %q", got)
		}
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}, []string{"claim_filter"})
		f.setAuth(f.backupDir, `{"loggedIn":false}`)
		f.d.agBackupCheck(context.Background())
		if got := swapped(f, urgentTask(), map[string]string{}); got != "" {
			t.Fatalf("invalid backup: %q", got)
		}
	})
}

// claimShapes reduces the recorded batch claims to runtime ids, max_tasks and filter.
type agClaimShape struct {
	Runtimes []string
	Max      int
	Filter   map[string]string
}

func claimShapes(t *testing.T, s *agBackupClaimServer) []agClaimShape {
	t.Helper()
	var out []agClaimShape
	for _, body := range s.bodies() {
		var shape agClaimShape
		for _, id := range body["runtime_ids"].([]any) {
			shape.Runtimes = append(shape.Runtimes, id.(string))
		}
		shape.Max = int(body["max_tasks"].(float64))
		if raw, ok := body["ag_min_issue_priority"].(map[string]any); ok {
			shape.Filter = map[string]string{}
			for k, v := range raw {
				shape.Filter[k] = v.(string)
			}
		}
		out = append(out, shape)
	}
	return out
}

func claimWith(t *testing.T, f *agBackupFixture, maxTasks int) {
	t.Helper()
	if _, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude", "rt-codex", "rt-claude-b"}, maxTasks); err != nil {
		t.Fatalf("claim: %v", err)
	}
}

func TestAGPatchBackupClaim(t *testing.T) {
	t.Run("AG patch: on the backup the Claude claim asks for urgent only, with max_tasks from the backup's room", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		claimWith(t, f, 5)
		want := []agClaimShape{
			{Runtimes: []string{"rt-claude"}, Max: 2, Filter: map[string]string{"rt-claude": "urgent"}},
			{Runtimes: []string{"rt-codex"}, Max: 5},
		}
		if got := claimShapes(t, f.srv); !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %+v, want %+v", got, want)
		}
	})

	t.Run("AG patch: max_tasks is backup_max_concurrent minus the runs already on the backup", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		done := f.d.agPaceRunStart("b1", filepath.Clean(f.backupDir), f.clock.now())
		claimWith(t, f, 5)
		got := claimShapes(t, f.srv)
		if len(got) != 2 || got[0].Max != 1 || got[0].Filter["rt-claude"] != "urgent" {
			t.Fatalf("claims = %+v, want one Claude slot left", got)
		}

		f.d.agPaceRunStart("b2", filepath.Clean(f.backupDir), f.clock.now())
		before := len(f.srv.bodies())
		claimWith(t, f, 5)
		got = claimShapes(t, f.srv)[before:]
		if len(got) != 1 || !reflect.DeepEqual(got[0].Runtimes, []string{"rt-codex"}) || got[0].Filter != nil {
			t.Fatalf("claims at the backup limit = %+v, want only the Codex runtime", got)
		}
		done()
	})

	t.Run("AG patch: tasks claimed but not yet running count against the backup limit", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.d.agPace.mu.Lock()
		f.d.agPace.claimed = map[string]struct{}{"c1": {}, "c2": {}}
		f.d.agPace.mu.Unlock()
		claimWith(t, f, 5)
		got := claimShapes(t, f.srv)
		if len(got) != 1 || !reflect.DeepEqual(got[0].Runtimes, []string{"rt-codex"}) {
			t.Fatalf("claims = %+v", got)
		}
	})

	t.Run("AG patch: max_tasks never exceeds the claim's own slots", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 4}, []string{"claim_filter"})
		f.holdPrimary(t)
		claimWith(t, f, 1)
		got := claimShapes(t, f.srv)
		if len(got) != 2 || got[0].Max != 1 || got[0].Filter["rt-claude"] != "urgent" || got[1].Max != 1 || got[1].Filter != nil {
			t.Fatalf("claims = %+v, want one Claude slot of the one asked for, then the Codex fill", got)
		}
	})

	t.Run("AG patch: no Claude runtime is claimed on the backup without the capability, setting or a valid backup", func(t *testing.T) {
		cases := map[string]func(f *agBackupFixture){
			"no capability": func(f *agBackupFixture) {
				f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}, nil)
			},
			"ack without ag_settings": func(f *agBackupFixture) { f.d.agApplyAck("rt-claude", nil, []string{"claim_filter"}) },
			"disabled": func(f *agBackupFixture) {
				f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: 2}, []string{"claim_filter"})
			},
			"settings older than ten minutes": func(f *agBackupFixture) {
				f.clock.set(f.clock.now().Add(agSettingsTTL + time.Second))
			},
			"backup not logged in": func(f *agBackupFixture) {
				f.setAuth(f.backupDir, `{"loggedIn":false}`)
				f.d.agBackupCheck(context.Background())
			},
			"backup held too": func(f *agBackupFixture) { f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir}) },
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				f := newAGBackupFixture(t)
				f.ready(t)
				f.holdPrimary(t)
				mutate(f)
				claimWith(t, f, 5)
				got := claimShapes(t, f.srv)
				if len(got) != 1 || !reflect.DeepEqual(got[0].Runtimes, []string{"rt-codex"}) || got[0].Filter != nil {
					t.Fatalf("claims = %+v, want only the Codex runtime, unfiltered", got)
				}
			})
		}
	})

	t.Run("AG patch: with the primary healthy the claim is unfiltered and includes Claude as before", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		claimWith(t, f, 2)
		got := claimShapes(t, f.srv)
		if len(got) != 1 || len(got[0].Runtimes) != 3 || got[0].Filter != nil {
			t.Fatalf("claims = %+v", got)
		}
	})

	t.Run("AG patch: the legacy per-runtime claim never serves a Claude runtime while filtering", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.srv.noBatch = true // an old server: the batch route 404s
		f.ready(t)
		f.holdPrimary(t)
		claimWith(t, f, 5) // first cycle: the 404 flips the daemon to the legacy claim mid-call
		claimWith(t, f, 5) // second cycle: legacy from the start
		hits := f.srv.legacyHits()
		if len(hits) == 0 {
			t.Fatal("the Codex runtime was not claimed through the legacy route")
		}
		for _, h := range hits {
			if strings.Contains(h, "rt-claude") {
				t.Fatalf("legacy claim for a Claude runtime on the backup: %v", hits)
			}
		}
	})

	t.Run("AG patch: the claim filter names only the runtimes in the request", func(t *testing.T) {
		ctx := agWithMinPriority(context.Background(), map[string]string{"a": "urgent", "z": "urgent"})
		body := agClaimBody(ctx, map[string]any{"runtime_ids": []string{"a", "b"}, "max_tasks": 1})
		if got := body["ag_min_issue_priority"]; !reflect.DeepEqual(got, map[string]string{"a": "urgent"}) {
			t.Fatalf("filter = %v", got)
		}
		body = agClaimBody(ctx, map[string]any{"runtime_ids": []string{"b"}})
		if _, ok := body["ag_min_issue_priority"]; ok {
			t.Fatalf("filter on a request without its runtimes: %v", body)
		}
		body = agClaimBody(context.Background(), map[string]any{"runtime_ids": []string{"a"}})
		if _, ok := body["ag_min_issue_priority"]; ok {
			t.Fatalf("filter without a context filter: %v", body)
		}
		if got := agLegacyClaimIDs(ctx, []string{"a", "b", "z"}); !reflect.DeepEqual(got, []string{"b"}) {
			t.Fatalf("legacy ids = %v", got)
		}
		if got := agLegacyClaimIDs(context.Background(), []string{"a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("legacy ids without a filter = %v", got)
		}
	})
}

func TestAGPatchBackupReport(t *testing.T) {
	t.Run("AG patch: the report carries detected, valid, reason and the backup's windows", func(t *testing.T) {
		f := newAGBackupFixture(t)
		now := f.clock.now()
		raw := `{"status":"allowed","rateLimitType":"five_hour","resetsAt":` + itoa(now.Add(2*time.Hour).Unix()) +
			`,"unifiedWindows":{"five_hour":{"utilization":0.4321,"resetsAt":` + itoa(now.Add(2*time.Hour).Unix()) + `}}}`
		agQuotaObs(f.d, Task{ID: "b-evt"}, "claude", map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})(json.RawMessage(raw))

		f.setAuth(f.backupDir, `{"loggedIn":false}`)
		f.d.agBackupCheck(context.Background())
		r := f.d.agQuotaReport("rt-claude")
		if !r.Backup.Detected || r.Backup.Valid || r.Backup.Reason != "not_logged_in" || r.Account != "primary" {
			t.Fatalf("report = %+v account %s", r.Backup, r.Account)
		}
		if w, ok := r.BackupWindows["five_hour"]; !ok || w.Utilization == nil || *w.Utilization != 0.43 {
			t.Fatalf("backup windows = %+v", r.BackupWindows)
		}
		if len(r.Windows) != 0 {
			t.Fatalf("the backup's windows leaked into the primary's: %+v", r.Windows)
		}
	})

	t.Run("AG patch: a missing backup reports detected false", func(t *testing.T) {
		f := newAGBackupFixture(t)
		if err := os.RemoveAll(f.backupDir); err != nil {
			t.Fatal(err)
		}
		f.d.agBackupCheck(context.Background())
		r := f.d.agQuotaReport("rt-claude")
		if r.Backup.Detected || r.Backup.Valid || r.Backup.Reason != "not_detected" || len(r.BackupWindows) != 0 {
			t.Fatalf("report = %+v", r)
		}
	})

	t.Run("AG patch: while failing over the report says backup, with the backup's cap and max", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		r := f.d.agQuotaReport("rt-claude")
		if r.Account != "backup" || r.Max != 2 || r.EffectiveCap != 2 || r.Hold == nil || !r.Backup.Valid || !r.Backup.Enabled {
			t.Fatalf("report = %+v", r)
		}
		f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})
		if r := f.d.agQuotaReport("rt-claude"); r.Account != "backup" || r.EffectiveCap != 0 {
			t.Fatalf("with the backup held too: account %s cap %d", r.Account, r.EffectiveCap)
		}
	})

	t.Run("AG patch: a disabled backup keeps reporting the primary", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.d.agApplyAck("rt-claude", nil, []string{"claim_filter"})
		r := f.d.agQuotaReport("rt-claude")
		if r.Account != "primary" || r.EffectiveCap != 0 || r.Backup.Enabled {
			t.Fatalf("report = %+v", r)
		}
	})
}

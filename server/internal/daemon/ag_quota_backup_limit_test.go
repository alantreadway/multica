package daemon

// AG patch: backup account, backup_max_concurrent and pacing. The slot tests drive
// the swap and the in-place resume the way runTask does (reserve, then count the
// run); the pacing test feeds the backup's own windows and works the expected
// claim size out of unit 2's curve; the runTask test proves the env swap hook is
// wired in, with a test-created claude.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// agGateBackend holds every Execute open until release is closed, so a resume stays
// "running" while the next one starts.
type agGateBackend struct {
	started chan string
	release chan struct{}
}

func (b *agGateBackend) Execute(_ context.Context, _ string, opts agent.ExecOptions) (*agent.Session, error) {
	b.started <- opts.ResumeSessionID
	<-b.release
	messages := make(chan agent.Message)
	close(messages)
	results := make(chan agent.Result, 1)
	results <- agent.Result{Status: "completed", SessionID: opts.ResumeSessionID}
	return &agent.Session{Messages: messages, Result: results}, nil
}

func TestAGPatchBackupConcurrentResumes(t *testing.T) {
	t.Run("AG patch: two urgent runs rejected together both resume at backup_max_concurrent 2, a third does not", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		b := &agGateBackend{started: make(chan string, 4), release: make(chan struct{})}
		type outcome struct {
			res agent.Result
			env map[string]string
		}
		resume := func(id string) chan outcome {
			f.d.agBackupNoteRejected(id, agQuotaAccountKey(nil))
			task := Task{ID: id, RuntimeID: "rt-claude", AGIssuePriority: "urgent"}
			env := map[string]string{}
			done := make(chan outcome, 1)
			go func() {
				res, _ := f.d.agQuotaResumeOnBackup(context.Background(), b, task, "claude", env, agent.ExecOptions{}, discardLogger(), "",
					new(atomic.Int32), agent.Result{Status: "failed", SessionID: "sess-" + id}, 0)
				done <- outcome{res, env}
			}()
			return done
		}
		await := func(want string) {
			t.Helper()
			select {
			case got := <-b.started:
				if got != want {
					t.Fatalf("resumed %q, want %q", got, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never resumed", want)
			}
		}

		first := resume("r1")
		await("sess-r1")
		second := resume("r2")
		await("sess-r2") // the first resume's run is counted once, so there is room for this one

		runningNow := f.d.agBackupRunning(filepath.Clean(f.backupDir))
		if runningNow != 2 {
			t.Fatalf("running on the backup = %d, want 2", runningNow)
		}
		f.d.agBackupNoteRejected("r3", agQuotaAccountKey(nil))
		res, _ := f.d.agQuotaResumeOnBackup(context.Background(), b, Task{ID: "r3", RuntimeID: "rt-claude", AGIssuePriority: "urgent"}, "claude",
			map[string]string{}, agent.ExecOptions{}, discardLogger(), "", new(atomic.Int32), agent.Result{Status: "failed", SessionID: "sess-r3"}, 0)
		if res.Status != "failed" {
			t.Fatalf("third resume = %+v, want the original failure (the backup is full)", res)
		}
		select {
		case id := <-b.started:
			t.Fatalf("%s resumed past the limit", id)
		default:
		}

		close(b.release)
		for _, ch := range []chan outcome{first, second} {
			select {
			case o := <-ch:
				if o.res.Status != "completed" || o.env["CLAUDE_CONFIG_DIR"] != filepath.Clean(f.backupDir) {
					t.Fatalf("outcome = %+v env %v, want a completed resume on the backup", o.res, o.env)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("resume never finished")
			}
		}
		if n := len(f.d.agBackup.slots); n != 0 {
			t.Fatalf("slots leaked: %d", n)
		}
		if n := f.d.agBackupRunning(filepath.Clean(f.backupDir)); n != 0 {
			t.Fatalf("runs still counted on the backup: %d", n)
		}
	})
}

func TestAGPatchBackupSwapLimit(t *testing.T) {
	backup := func(f *agBackupFixture) string { return filepath.Clean(f.backupDir) }
	swap := func(f *agBackupFixture, id string) string {
		env := map[string]string{}
		f.d.agApplyClaudeAccount("claude", Task{ID: id, RuntimeID: "rt-claude", AGIssuePriority: "urgent"}, env)
		return env["CLAUDE_CONFIG_DIR"]
	}

	t.Run("AG patch: urgent tasks claimed before the hold stay on the primary once the backup is full", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.d.agPaceRunStart("b1", backup(f), f.clock.now())
		f.d.agPaceRunStart("b2", backup(f), f.clock.now())
		if got := swap(f, "u1"); got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q with two runs on the backup, want it untouched", got)
		}
	})

	t.Run("AG patch: tasks that start together never exceed the limit, whether or not their runs are counted yet", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		if swap(f, "u1") != backup(f) || swap(f, "u2") != backup(f) {
			t.Fatal("the first two tasks should move to the backup")
		}
		if got := swap(f, "u3"); got != "" {
			t.Fatalf("third task = %q, want it left on the primary", got)
		}
		// u1's run is counted: its slot is released, so it is not counted twice.
		doneU1 := f.d.agPaceRunStart("u1", backup(f), f.clock.now())
		f.d.agBackupSlotDone("u1")
		if got := swap(f, "u3"); got != "" {
			t.Fatalf("u1 running + u2 about to start = 2: u3 = %q, want it left on the primary", got)
		}
		doneU1()
		if got := swap(f, "u3"); got != backup(f) {
			t.Fatalf("after u1 ended u3 = %q, want the backup", got)
		}
	})

	t.Run("AG patch: a task whose slot is never counted gives it back when its dispatch ends", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		swap(f, "u1")
		swap(f, "u2")
		f.d.agUntrackTask("u1")
		if got := swap(f, "u3"); got != backup(f) {
			t.Fatalf("u3 = %q, want the backup once u1's slot was released", got)
		}
	})

	t.Run("AG patch: a task stays on the primary when the backup is on hold", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		f.holdPrimary(t)
		f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})
		if got := swap(f, "u1"); got != "" {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q with the backup held, want it untouched", got)
		}
	})
}

// backupEvent feeds one allowed five-hour event to the backup account's observer.
func backupEvent(f *agBackupFixture, util float64, resets time.Time) {
	raw := `{"status":"allowed","rateLimitType":"five_hour","resetsAt":` + itoa(resets.Unix()) +
		`,"unifiedWindows":{"five_hour":{"utilization":` + string(mustJSON(util)) + `,"resetsAt":` + itoa(resets.Unix()) + `}}}`
	agQuotaObs(f.d, Task{ID: "backup-evt"}, "claude", map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})(json.RawMessage(raw))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestAGPatchBackupPacing(t *testing.T) {
	t.Run("AG patch: the backup's own utilisation paces its claim below backup_max_concurrent", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.ready(t)
		now := f.clock.now()
		reset5 := now.Add(3 * time.Hour)

		// Unit 2's curve on the backup account: 0.70 -> 0.80 in 30 min with two runs
		// is 0.10 per run-hour, then 0.85 an hour in with the EWMA at 0.085, so
		// n = (0.95 - 0.85) / (2h * 0.085) = 0.59 -> target 1. The cap starts at the
		// limit (2) and the first step after a quiet period is immediate.
		backupEvent(f, 0.70, reset5)
		d1 := f.d.agPaceRunStart("b1", filepath.Clean(f.backupDir), f.clock.now())
		d2 := f.d.agPaceRunStart("b2", filepath.Clean(f.backupDir), f.clock.now())
		f.clock.set(now.Add(30 * time.Minute))
		backupEvent(f, 0.80, reset5)
		f.clock.set(now.Add(time.Hour))
		backupEvent(f, 0.85, reset5)
		d1()
		d2()

		f.holdPrimary(t)
		f.ready(t) // a fresh ack and check: the setting expires after ten minutes without one
		claimWith(t, f, 5)
		got := claimShapes(t, f.srv)
		if len(got) == 0 || got[0].Max != 1 || got[0].Filter["rt-claude"] != "urgent" {
			t.Fatalf("claims = %+v, want one Claude slot (paced), not backup_max_concurrent's 2", got)
		}

		// The report shows the same pacing cap.
		r := f.d.agQuotaReport("rt-claude")
		if r.Account != "backup" || r.Max != 2 || r.EffectiveCap != 1 {
			t.Fatalf("report account %s max %d cap %d, want backup 2 1", r.Account, r.Max, r.EffectiveCap)
		}
	})
}

// --- through runTask ---------------------------------------------------------------------

func TestAGPatchBackupRunTaskSwap(t *testing.T) {
	const successWithWindow = `{"type":"system","subtype":"init","session_id":"sess-ok"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","resetsAt":RESETS,"unifiedWindows":{"five_hour":{"utilization":0.4,"resetsAt":RESETS}}}}
{"type":"result","subtype":"success","is_error":false,"session_id":"sess-ok","result":"all done"}
`
	// The task's first run succeeds, so the only account choice is the env swap.
	succeed := func(f *agBackupFixture) {
		out := strings.ReplaceAll(successWithWindow, "RESETS", itoa(f.clock.now().Add(2*time.Hour).Unix()))
		if err := os.WriteFile(os.Getenv("AG_TEST_FIRST_OUT"), []byte(out), 0o644); err != nil {
			panic(err)
		}
	}

	t.Run("AG patch: runTask runs an urgent task under the backup dir while the primary is held, and counts it there", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, func(f *agBackupFixture) {
			succeed(f)
			f.holdPrimary(t)
		})
		runs := r.runs()
		if len(runs) != 1 || !strings.HasPrefix(runs[0], filepath.Clean(r.f.backupDir)+" ") {
			t.Fatalf("runs = %q, want one run under the backup dir", runs)
		}
		if r.result.Status != "completed" {
			t.Fatalf("result = %+v", r.result)
		}
		if _, ok := r.d.agQuotaAccountWindows(filepath.Clean(r.f.backupDir))["five_hour"]; !ok {
			t.Fatal("the run's rate-limit event was not recorded against the backup account")
		}
		r.d.agPace.mu.Lock()
		_, counted := r.d.agPace.accounts[filepath.Clean(r.f.backupDir)]
		r.d.agPace.mu.Unlock()
		if !counted {
			t.Fatal("the run was not paced under the backup account")
		}
		if n := len(r.d.agBackup.slots); n != 0 {
			t.Fatalf("backup slot leaked: %d", n)
		}
	})

	t.Run("AG patch: runTask runs the same task under the primary dir once the hold has passed", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, func(f *agBackupFixture) {
			succeed(f)
			f.holdPrimary(t)
			f.clock.set(f.clock.now().Add(33 * time.Minute)) // resetsAt + 2 min grace has passed
			f.d.agBackupCheck(context.Background())
		})
		runs := r.runs()
		if len(runs) != 1 || !strings.HasPrefix(runs[0], filepath.Clean(r.f.acct)+" ") {
			t.Fatalf("runs = %q, want one run under the primary dir", runs)
		}
	})
}

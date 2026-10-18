package daemon

// AG patch: backup account, in-place resume through runTask. The agent is a
// test-created claude that prints the rejected fixture on a first run and a
// scripted result when it is given --resume; it logs the config dir and args of
// every run. No installed CLI is used.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	agBackupRejectedFirst = `{"type":"system","subtype":"init","session_id":"sess-first"}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":RESETS,"rateLimitType":"five_hour"}}
{"type":"result","subtype":"success","is_error":true,"session_id":"sess-first","result":"5-hour limit reached"}
`
	agBackupResumeOK = `{"type":"system","subtype":"init","session_id":"sess-first"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"picked up where I left off"}]},"session_id":"sess-first"}
{"type":"result","subtype":"success","is_error":false,"session_id":"sess-first","result":"picked up where I left off"}
`
	// The resume answers with a different session: Claude could not load the transcript.
	agBackupResumeRejected = `{"type":"system","subtype":"init","session_id":"sess-other"}
{"type":"result","subtype":"success","is_error":true,"session_id":"sess-other","result":"No conversation found with session ID: sess-first"}
`
)

type agBackupRun struct {
	d      *Daemon
	f      *agBackupFixture
	log    string
	result TaskResult
}

// runBackupTask runs one claude task through runTask. prep runs after the backup is
// ready and before the task starts.
func runBackupTask(t *testing.T, task Task, customEnv map[string]string, resumeOut string, prep func(f *agBackupFixture)) *agBackupRun {
	t.Helper()
	f := newAGBackupFixture(t)
	t.Setenv("IS_SANDBOX", "1") // the claude backend refuses bypassPermissions as root, e.g. in a container
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runs.log")
	first := filepath.Join(dir, "first.out")
	resume := filepath.Join(dir, "resume.out")
	resets := f.clock.now().Add(30 * time.Minute)
	if err := os.WriteFile(first, []byte(strings.ReplaceAll(agBackupRejectedFirst, "RESETS", itoa(resets.Unix()))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resume, []byte(resumeOut), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AG_TEST_LOG", logPath)
	t.Setenv("AG_TEST_FIRST_OUT", first)
	t.Setenv("AG_TEST_RESUME_OUT", resume)

	f.ready(t)
	if prep != nil {
		prep(f)
	}
	f.d.executionEnvironmentCommand = nil
	f.d.workspaces = make(map[string]*workspaceState)
	f.d.activeEnvRoots = make(map[string]int)
	f.d.runtimeIndex["rt-claude"] = Runtime{ID: "rt-claude", Provider: "claude"}
	f.d.cfg.WorkspacesRoot = t.TempDir()
	f.d.cfg.AgentTimeout = 10 * time.Second

	task.WorkspaceID, task.IssueID, task.AgentID, task.AuthToken = "test-ws", "test-issue", "test-agent", "mat_test_backup"
	task.Agent = &AgentData{ID: "test-agent", Name: "test", CustomEnv: customEnv}
	res, err := f.d.runTask(context.Background(), task, "claude", 0, discardLogger())
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	raw, _ := os.ReadFile(logPath)
	return &agBackupRun{d: f.d, f: f, log: string(raw), result: res}
}

func (r *agBackupRun) runs() []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(r.log), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestAGPatchBackupResumeInPlace(t *testing.T) {
	t.Run("AG patch: an urgent run rejected on the primary resumes with --resume under the backup env", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, nil)
		runs := r.runs()
		if len(runs) != 2 {
			t.Fatalf("runs = %q, want a first run and a resume", runs)
		}
		if !strings.HasPrefix(runs[0], filepath.Clean(r.f.acct)+" ") || strings.Contains(runs[0], "--resume") {
			t.Fatalf("first run = %q, want the primary dir and no --resume", runs[0])
		}
		if !strings.HasPrefix(runs[1], filepath.Clean(r.f.backupDir)+" ") || !strings.Contains(runs[1], "--resume sess-first") {
			t.Fatalf("resume = %q, want the backup dir and --resume sess-first", runs[1])
		}
		if r.result.Status != "completed" || !strings.Contains(r.result.Comment, "picked up where I left off") {
			t.Fatalf("result = %+v, want the resumed run's outcome", r.result)
		}
		if r.result.SessionID != "sess-first" {
			t.Fatalf("session = %q", r.result.SessionID)
		}
	})

	t.Run("AG patch: the resumed run is counted and observed on the backup account", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, nil)
		if r.d.agBackupRunning(filepath.Clean(r.f.backupDir)) != 0 {
			t.Fatal("the backup run was not ended")
		}
		h := r.d.agQuotaHealth().ClaudeQuota
		if h == nil || !h.Held {
			t.Fatalf("health = %+v, want the primary held", h)
		}
		if r.d.agBackup.resuming != 0 {
			t.Fatalf("resume slot leaked: %d", r.d.agBackup.resuming)
		}
	})

	t.Run("AG patch: a run that is not urgent fails as quota and is not resumed", func(t *testing.T) {
		task := urgentTask()
		task.AGIssuePriority = "high"
		r := runBackupTask(t, task, nil, agBackupResumeOK, nil)
		if len(r.runs()) != 1 || r.result.Status == "completed" {
			t.Fatalf("runs = %q status %s", r.runs(), r.result.Status)
		}
	})

	t.Run("AG patch: no resume when the backup is disabled, or the server cannot filter", func(t *testing.T) {
		for name, ack := range map[string]func(f *agBackupFixture){
			"disabled": func(f *agBackupFixture) {
				f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: false, BackupMaxConcurrent: 2}, []string{"claim_filter"})
			},
			"no capability": func(f *agBackupFixture) {
				f.d.agApplyAck("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2}, nil)
			},
			"no ag_settings": func(f *agBackupFixture) { f.d.agApplyAck("rt-claude", nil, []string{"claim_filter"}) },
		} {
			t.Run(name, func(t *testing.T) {
				r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, ack)
				if len(r.runs()) != 1 || r.result.Status == "completed" {
					t.Fatalf("runs = %q status %s", r.runs(), r.result.Status)
				}
			})
		}
	})

	t.Run("AG patch: no resume while the backup is at backup_max_concurrent", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, func(f *agBackupFixture) {
			f.d.agPaceRunStart("other-1", filepath.Clean(f.backupDir), f.clock.now())
			f.d.agPaceRunStart("other-2", filepath.Clean(f.backupDir), f.clock.now())
		})
		if len(r.runs()) != 1 || r.result.Status == "completed" {
			t.Fatalf("runs = %q status %s", r.runs(), r.result.Status)
		}
	})

	t.Run("AG patch: no resume when the backup is held too", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeOK, func(f *agBackupFixture) {
			f.hold(t, map[string]string{"CLAUDE_CONFIG_DIR": f.backupDir})
		})
		if len(r.runs()) != 1 {
			t.Fatalf("runs = %q", r.runs())
		}
	})

	t.Run("AG patch: an agent's own config dir is not resumed on the backup", func(t *testing.T) {
		own := filepath.Join(t.TempDir(), "agent-claude")
		r := runBackupTask(t, urgentTask(), map[string]string{"CLAUDE_CONFIG_DIR": own}, agBackupResumeOK, nil)
		if len(r.runs()) != 1 {
			t.Fatalf("runs = %q", r.runs())
		}
	})

	t.Run("AG patch: a resume the backup rejects is a normal failure with no fresh-session retry", func(t *testing.T) {
		r := runBackupTask(t, urgentTask(), nil, agBackupResumeRejected, nil)
		runs := r.runs()
		if len(runs) != 2 || !strings.Contains(runs[1], "--resume sess-first") {
			t.Fatalf("runs = %q, want exactly the first run and one resume", runs)
		}
		if r.result.Status == "completed" {
			t.Fatalf("result = %+v, want a failure", r.result)
		}
		if strings.Contains(r.result.Comment, "No conversation found") {
			t.Fatalf("the rejected resume's output replaced the quota failure: %q", r.result.Comment)
		}
	})
}

package daemon

// AG patch: Claude resume across worktree directories. A worktree-mode run
// starts in a new directory every time. A Claude session follows it when the
// prior run's work is in the new tree (a continued branch, a merged branch) or
// the prior run left none (no commits); otherwise the run starts fresh with the
// "prior session unavailable" notice. A resume whose directory changed tells
// the agent which directory it is in. Codex and other providers are untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestAGPatchClaudeWorktreeResumable(t *testing.T) {
	t.Parallel()
	prior := filepath.Join("/data", "task-1", "worktree", "repo")
	managed := filepath.Join("/data", "task-1", "workdir")
	for _, tt := range []struct {
		name  string
		env   *execenv.Environment
		prior string
		want  bool
	}{
		{"continued branch on its own branch", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}}, prior, true},
		{"continued branch forked off a held branch", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{Continued: true}}, prior, false},
		{"continued branch, prior run in a managed workdir", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}}, managed, false},
		{"continued branch, prior dir unknown", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}}, "", false},
		{"own branch created or reset, prior run in a worktree", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}, prior, true},
		{"own branch created or reset, prior run in a worktree root", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}, filepath.Join("/data", "task-1", "worktree"), true},
		{"own branch created or reset, prior run in a workdir", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}, managed, false},
		{"own branch created or reset, prior dir unknown", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}, "", false},
		{"own branch created or reset, prior run outside any env root", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}, "/home/u/repo", false},
		{"forked or task-scoped branch", &execenv.Environment{LocalWorktree: &execenv.LocalWorktree{}}, prior, false},
		{"not a worktree run", &execenv.Environment{}, prior, false},
		{"no environment", nil, prior, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := agClaudeWorktreeResumable(tt.env, tt.prior); got != tt.want {
				t.Fatalf("agClaudeWorktreeResumable = %v, want %v", got, tt.want)
			}
		})
	}
}

// A managed workdir that happens to sit under a path with a "worktree"
// component is still a workdir: the nearest of the two decides.
func TestAGPatchIsWorktreePath(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"/data/t/worktree":             true,
		"/data/t/worktree/sub/dir":     true,
		"/data/worktree/t/workdir":     false,
		"/data/worktree/t/workdir/sub": false,
		"/data/t/workdir/worktree":     true,
		"/data/t/repo":                 false,
		"":                             false,
		"/":                            false,
		"relative/worktree/x":          true,
		"/data/t/worktrees/x":          false,
		"/data/t/worktree-old/x":       false,
	} {
		if got := agIsWorktreePath(path); got != want {
			t.Errorf("agIsWorktreePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// Writes the prior transcript, last written age ago, where Claude Code keeps it
// for the prior directory.
func agWTWriteTranscript(t *testing.T, configDir, priorDir, sessionID string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(configDir, "projects", encodeClaudeCwd(priorDir), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"type\":\"user\",\"message\":{\"content\":\"prior discussion\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAGPatchMigrateClaudeWorktreeTranscript(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		lw         execenv.LocalWorktree
		priorDir   func(root string) string
		transcript bool
		want       bool
	}{
		{"continued branch", execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "worktree") }, true, true},
		{"own branch created or reset, prior run in a worktree", execenv.LocalWorktree{OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "worktree") }, true, true},
		{"own branch created or reset, prior run in a workdir", execenv.LocalWorktree{OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "workdir") }, true, false},
		{"continued branch, prior run in a workdir", execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "workdir") }, true, false},
		{"neither flag", execenv.LocalWorktree{}, func(r string) string { return filepath.Join(r, "a", "worktree") }, true, false},
		{"own branch, missing transcript", execenv.LocalWorktree{OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "worktree") }, false, false},
		{"continued branch, missing transcript", execenv.LocalWorktree{Continued: true, OwnBranchResumable: true}, func(r string) string { return filepath.Join(r, "a", "worktree") }, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, configDir := t.TempDir(), t.TempDir()
			priorDir := tt.priorDir(root)
			workDir := filepath.Join(root, "b", "worktree")
			const sessionID = "session-wt-migrate"
			if tt.transcript {
				agWTWriteTranscript(t, configDir, priorDir, sessionID, 10*time.Minute)
			}
			lw := tt.lw
			env := &execenv.Environment{WorkDir: workDir, LocalWorktree: &lw}

			got := migrateClaudeWorktreeTranscript(env, sessionID, workDir, priorDir, map[string]string{"CLAUDE_CONFIG_DIR": configDir}, slog.New(slog.DiscardHandler))

			if got != tt.want {
				t.Fatalf("migrate = %v, want %v", got, tt.want)
			}
			_, err := os.Stat(filepath.Join(configDir, "projects", encodeClaudeCwd(workDir), sessionID+".jsonl"))
			if copied := err == nil; copied != tt.want {
				t.Fatalf("transcript copied into the new directory = %v, want %v (%v)", copied, tt.want, err)
			}
		})
	}
}

// Only the Claude gate branch reads claudeResumeReady, and Codex's rule reads
// Continued alone: neither moves for a worktree that is resumable only because
// its own branch was created or reset.
func TestAGPatchOtherProvidersUnchanged(t *testing.T) {
	t.Parallel()
	own := &execenv.Environment{WorkDir: t.TempDir(), CodexHome: t.TempDir(), LocalWorktree: &execenv.LocalWorktree{OwnBranchResumable: true}}
	if codexWorktreeResumeEligible(own, "/store") {
		t.Fatal("Codex must not resume on OwnBranchResumable")
	}
	continued := &execenv.Environment{WorkDir: t.TempDir(), CodexHome: t.TempDir(), LocalWorktree: &execenv.LocalWorktree{Continued: true}}
	if !codexWorktreeResumeEligible(continued, "/store") {
		t.Fatal("Codex must still resume a continued worktree")
	}

	for _, provider := range []string{"codex", "cursor", "gemini", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			priorDir, workDir := t.TempDir(), t.TempDir()
			task := Task{PriorSessionID: "session-1", PriorWorkDir: priorDir}
			taskCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}

			// claudeResumeReady is true here; for any provider but claude it must be ignored.
			reachable := gateResumeToReachableSession(&task, &taskCtx, provider, workDir, true, false, false, true, slog.New(slog.DiscardHandler))

			if reachable || task.PriorSessionID != "" || taskCtx.PriorSessionResumed || !task.PriorSessionResumeUnavailable {
				t.Fatalf("%s: a changed workdir should still drop the session with its notice: reachable=%v task=%+v", provider, reachable, task)
			}
		})
	}
}

func TestAGPatchResumedWorkDirOption(t *testing.T) {
	t.Parallel()
	const workDir = "/data/task-2/worktree/repo"
	resumed := Task{PriorSessionID: "s", PriorWorkDir: "/data/task-1/worktree/repo"}
	build := func(task Task, wasResumed bool, dir string) string {
		return perTurnContextBlocksFor(task, agWithResumedWorkDir(nil, wasResumed, dir))
	}

	got := build(resumed, true, workDir)
	if !strings.Contains(got, "## Working directory") || !strings.Contains(got, "`"+workDir+"`") {
		t.Fatalf("a cross-directory Claude resume should state the directory:\n%s", got)
	}
	if strings.Contains(got, "/data/task-1/") {
		t.Errorf("the line should not repeat the earlier path:\n%s", got)
	}

	for name, got := range map[string]string{
		"not resumed": build(resumed, false, workDir),
		// The fresh-session retry clears the prior session but reuses the options.
		"session cleared after the options were built": build(Task{PriorWorkDir: resumed.PriorWorkDir}, true, workDir),
		"path with a backtick":                         build(resumed, true, "/data/we`ird/worktree"),
		"path with a newline":                          build(resumed, true, "/data/we\nird/worktree"),
		"empty current directory":                      build(resumed, true, ""),
	} {
		if strings.Contains(got, "## Working directory") {
			t.Errorf("%s: unexpected directory line:\n%s", name, got)
		}
	}
}

func TestAGPatchClaudeMayMigrate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		task     Task
		provider string
		want     bool
	}{
		{"issue task on claude", Task{IssueID: "i"}, "claude", true},
		{"chat task on claude", Task{ChatSessionID: "c"}, "claude", false},
		{"issue task on codex", Task{IssueID: "i"}, "codex", false},
	} {
		if got := agClaudeMayMigrate(&tt.task, tt.provider); got != tt.want {
			t.Errorf("%s: agClaudeMayMigrate = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// perTurnContextBlocksFor renders the per-turn blocks with the options a run
// would carry. promptOpts is built the way BuildPrompt builds it.
func perTurnContextBlocksFor(task Task, opts []PromptOption) string {
	var o promptOpts
	for _, apply := range opts {
		apply(&o)
	}
	return perTurnContextBlocks(task, o)
}

// The directory line is run-scoped text and so belongs after the cached
// prefix, with the other per-turn blocks, not in the runtime brief.
func TestAGPatchResumedWorkDirIsPerTurnText(t *testing.T) {
	t.Parallel()
	task := Task{ID: "t", IssueID: "i", AgentID: "a", PriorSessionID: "s", PriorWorkDir: "/data/task-1/worktree/repo"}
	opts := agWithResumedWorkDir([]PromptOption{WithWorktreeBranch("agent/j/mul-1", "abc123")}, true, "/data/task-2/worktree/repo")
	prompt := BuildPrompt(task, "claude", opts...)
	if !strings.Contains(prompt, "This run's working directory is `/data/task-2/worktree/repo`") {
		t.Fatalf("the prompt should state the directory:\n%s", prompt)
	}
	branch, dir := strings.Index(prompt, "agent/j/mul-1"), strings.Index(prompt, "## Working directory")
	if branch < 0 || dir < branch {
		t.Errorf("the directory line should follow the branch block (branch at %d, directory at %d)", branch, dir)
	}
}

// ---- Through runTask, with real worktrees and a fake claude ----

type agWTCase struct {
	firstCommits      bool                // the prior run commits on its branch
	mergeAfterFirst   bool                // the user merges that branch into HEAD
	holdBranch        bool                // the user's checkout holds the branch, so the next run forks
	holdByWorktree    bool                // a preserved worktree holds the branch while HEAD stays behind it, so the next run forks from unmerged work
	transcriptAge     *time.Duration      // nil: no transcript is written
	priorWorkDir      func(string) string // override the prior directory the server reports
	noPriorSession    bool                // the server reports no prior session
	rewriteBranch     bool                // the user rewrites the branch, so it can no longer be proved the conversation's
	renameAgent       bool                // the agent's name changes before the second run, so its preferred branch name does
	preferredHeld     bool                // the user holds the preferred branch name, so the first run lands on the fingerprint branch
	deletePreferred   bool                // the user then deletes that preferred branch
	otherConversation bool                // another conversation left an unmerged branch and record in the repo
	lostRecord        bool                // ...and that record's object is then lost, so it cannot be read
	rejectResume      bool                // the fake claude refuses a --resume, as it does for a session it cannot load
	chat              bool                // a chat task rather than an issue task
	provider          string              // "" is claude; "grok" is the one non-Claude fake
}

type agWTOutcome struct {
	first, second TaskResult
	repo          string
	configDir     string
	args, stdin   string // the second run's claude invocation
	pwd           string // directory claude ran in on the second run
	log           string
	invocations   int
	provider      string
}

func (o agWTOutcome) resumed() bool {
	if o.provider == "grok" {
		return strings.Contains(o.stdin, `"method":"session/load"`)
	}
	return strings.Contains(o.args, "--resume\n"+o.first.SessionID+"\n")
}
func (o agWTOutcome) notice() bool  { return strings.Contains(o.stdin, "## Session Continuity Notice") }
func (o agWTOutcome) dirLine() bool { return strings.Contains(o.stdin, "## Working directory") }
func (o agWTOutcome) freshLogged() bool {
	return strings.Contains(o.log, "starting fresh Claude session")
}
func (o agWTOutcome) copied() bool {
	_, err := os.Stat(filepath.Join(o.configDir, "projects", encodeClaudeCwd(o.second.WorkDir), o.first.SessionID+".jsonl"))
	return err == nil
}

// agWTRun runs two turns of one issue on a worktree-mode project: the first
// creates the session, then the second carries it and is the run under test.
func agWTRun(t *testing.T, c agWTCase) agWTOutcome {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script agent fixture is POSIX-only")
	}
	t.Setenv("IS_SANDBOX", "1")
	configDir := filepath.Join(t.TempDir(), "claude-config")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	prevNow := agClaudeResumeNow
	agClaudeResumeNow = time.Now
	t.Cleanup(func() { agClaudeResumeNow = prevNow })

	repo := createDaemonTestRepo(t)
	out := t.TempDir()
	argsFile, stdinFile, pwdFile := filepath.Join(out, "args"), filepath.Join(out, "stdin"), filepath.Join(out, "pwd")
	provider := "claude"
	if c.provider != "" {
		provider = c.provider
	}
	fakeBin := filepath.Join(t.TempDir(), provider)
	script := func(commit bool, rejectResume bool) []byte {
		body := ""
		if commit {
			body = "printf 'first turn work\\n' > turn-one.txt\ngit add turn-one.txt\ngit -c user.name=t -c user.email=t@example.com commit -q -m 'turn one'\n"
		}
		if c.provider == "grok" {
			return []byte(`#!/bin/sh
case "$*" in *--version*) echo '1.0.0'; exit 0;; esac
printf '%s\n' "$@" >> '` + argsFile + `'
printf '%s\n' '--invocation-end--' >> '` + argsFile + `'
pwd >> '` + pwdFile + `'
` + body + `while IFS= read -r line; do
  printf '%s\n' "$line" >> '` + stdinFile + `'
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token","name":"Test authentication"}],"agentCapabilities":{"mcpCapabilities":{"http":true,"sse":true}}}}\n' "$id";;
    *'"method":"session/new"'*|*'"method":"session/load"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"session-wt","models":{"currentModelId":"grok-4.6","availableModels":[{"modelId":"grok-4.6","name":"Test model"}]}}}\n' "$id";;
    *'"method":"session/prompt"'*)
      printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-wt","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}}}'
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn","_meta":{"modelId":"grok-4.6"}}}\n' "$id";;
    *) if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi;;
  esac
done
`)
		}
		reject := ""
		if rejectResume {
			reject = `case "$*" in *--resume*)
printf '%s\n' '{"type":"system","session_id":"fresh-new"}'
printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"fresh-new","result":"No conversation found with session ID: session-wt"}'
exit 1;;
esac
`
		}
		return []byte(`#!/bin/sh
case "$*" in *--version*) echo '2.1.299'; exit 0;; esac
printf '%s\n' "$@" >> '` + argsFile + `'
printf '%s\n' '--invocation-end--' >> '` + argsFile + `'
pwd >> '` + pwdFile + `'
` + body + `IFS= read -r line
printf '%s\n--prompt-end--\n' "$line" >> '` + stdinFile + `'
` + reject + `printf '%s\n' '{"type":"system","session_id":"session-wt"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-wt","result":"done"}'
`)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(srv.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{
		client: NewClient(srv.URL), logger: logger, localPathLocks: NewLocalPathLocker(),
		workspaces: make(map[string]*workspaceState), activeEnvRoots: make(map[string]int),
		cfg: Config{
			DaemonID: "daemon-wt", WorkspacesRoot: t.TempDir(), AgentTimeout: 5 * time.Second,
			ServerBaseURL: srv.URL, Agents: map[string]AgentEntry{provider: {Path: fakeBin}},
		},
		runtimeIndex: map[string]Runtime{"runtime-wt": {ID: "runtime-wt", Provider: provider}},
	}
	ref, err := json.Marshal(localDirectoryRef{LocalPath: repo, DaemonID: d.cfg.DaemonID, ExecutionMode: localDirectoryModeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{
		ID: "task-wt-first", WorkspaceID: "ws-wt", IssueID: "issue-wt", IssueIdentifier: "MUL-9001",
		RuntimeID: "runtime-wt", AgentID: "agent-wt", AuthToken: "mat_wt_test",
		Agent:            &AgentData{ID: "agent-wt", Name: "wt-agent"},
		ProjectResources: []ProjectResourceData{{ID: "repo-wt", ResourceType: localDirectoryResourceType, ResourceRef: ref}},
	}

	if c.chat {
		task.IssueID, task.IssueIdentifier = "", ""
		task.ChatSessionID, task.ChatMessage = "chat-wt", "please continue"
	}
	if provider == "grok" {
		task.Agent.Model = "grok-4.6"
	}

	if c.preferredHeld {
		agWTGit(t, repo, "branch", "agent/wt-agent/mul-9001")
	}
	if c.otherConversation {
		agWTLeaveOtherConversation(t, repo, c.lostRecord)
	}
	writeTestExecutable(t, fakeBin, script(c.firstCommits, false))
	first, err := d.runTask(context.Background(), task, provider, 0, logger)
	if err != nil || first.Status != "completed" {
		t.Fatalf("first turn = %+v, %v", first, err)
	}
	if first.SessionID != "session-wt" {
		t.Fatalf("first turn session = %q", first.SessionID)
	}
	if _, err := os.Stat(first.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("prior worktree was not removed: %v", err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		outp, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, outp)
		}
		return strings.TrimSpace(string(outp))
	}
	if c.rewriteBranch {
		git("update-ref", "refs/heads/"+first.BranchName, git("commit-tree", "-m", "rewritten", "HEAD^{tree}"))
	}
	if c.mergeAfterFirst {
		git("merge", "--no-edit", first.BranchName)
	}
	if c.holdBranch {
		git("checkout", first.BranchName)
	}
	if c.holdByWorktree {
		git("worktree", "add", filepath.Join(t.TempDir(), "held"), first.BranchName)
	}
	if c.preferredHeld && first.BranchName == "agent/wt-agent/mul-9001" {
		t.Fatalf("premise: the first run should have landed on the fingerprint branch, got %s", first.BranchName)
	}
	if c.deletePreferred {
		git("branch", "-D", "agent/wt-agent/mul-9001")
	}
	if c.renameAgent {
		task.Agent = &AgentData{ID: "agent-wt", Name: "renamed-agent"}
	}

	priorDir := first.WorkDir
	if c.priorWorkDir != nil {
		priorDir = c.priorWorkDir(first.WorkDir)
	}
	if c.transcriptAge != nil {
		agWTWriteTranscript(t, configDir, priorDir, first.SessionID, *c.transcriptAge)
	}
	task.ID = "task-wt-second"
	if !c.noPriorSession {
		task.PriorSessionID, task.PriorWorkDir = first.SessionID, priorDir
	}
	writeTestExecutable(t, fakeBin, script(false, c.rejectResume))
	// Truncate what the first turn recorded, so only the run under test remains.
	for _, f := range []string{argsFile, stdinFile, pwdFile} {
		if err := os.Truncate(f, 0); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	runLog := slog.New(slog.NewTextHandler(&logs, nil))
	second, err := d.runTask(context.Background(), task, provider, 0, runLog)
	if err != nil || second.Status != "completed" {
		t.Fatalf("second turn = %+v, %v\n%s", second, err, logs.String())
	}
	args, _ := os.ReadFile(argsFile)
	stdin, _ := os.ReadFile(stdinFile)
	pwd, _ := os.ReadFile(pwdFile)
	return agWTOutcome{
		first: first, second: second, repo: repo, configDir: configDir,
		args: string(args), stdin: string(stdin), pwd: strings.TrimSpace(string(pwd)), log: logs.String(),
		invocations: strings.Count(string(args), "--invocation-end--"), provider: provider,
	}
}

func agWTGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// agWTLeaveOtherConversation leaves a branch with unmerged work and its record,
// owned by a different issue in the same repo. With lost set, the record's
// object is then deleted, so reading it fails.
func agWTLeaveOtherConversation(t *testing.T, repo string, lost bool) {
	t.Helper()
	wt, err := execenv.PrepareLocalWorktree(execenv.LocalWorktreeParams{
		LocalPath: repo, EnvRoot: t.TempDir(), AgentName: "wt-agent", TaskID: "task-other",
		ConversationKey: "MUL-9002", WorkspaceID: "ws-wt", AgentID: "agent-wt", ConversationID: "issue-other",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("prepare the other conversation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt.WorkDir, "other.txt"), []byte("other work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Finalize(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("finalize the other conversation: %v", err)
	}
	record := agWTGit(t, repo, "rev-parse", "refs/multica/local-state/agent/wt-agent/mul-9002")
	if lost {
		if err := os.Remove(filepath.Join(repo, ".git", "objects", record[:2], record[2:])); err != nil {
			t.Fatalf("premise: the record should be a loose object: %v", err)
		}
	}
}

func agWTMinutes(m int) *time.Duration { d := time.Duration(m) * time.Minute; return &d }

func TestAGPatchClaudeWorktreeResumeRunTask(t *testing.T) {
	for _, tt := range []struct {
		name       string
		c          agWTCase
		wantResume bool // --resume, the copy and the directory line, together
	}{
		// AC1, AC3: resumes into a new directory.
		{"continued branch", agWTCase{firstCommits: true, transcriptAge: agWTMinutes(10)}, true},
		{"prior run committed nothing", agWTCase{transcriptAge: agWTMinutes(10)}, true},
		{"branch merged into HEAD", agWTCase{firstCommits: true, mergeAfterFirst: true, transcriptAge: agWTMinutes(10)}, true},
		// AC6: a worktree where the prior work may not be present starts fresh.
		{"forked because the branch is held", agWTCase{firstCommits: true, mergeAfterFirst: true, holdBranch: true, transcriptAge: agWTMinutes(10)}, false},
		{"forked off a branch with unmerged work held by a preserved worktree", agWTCase{firstCommits: true, holdByWorktree: true, transcriptAge: agWTMinutes(10)}, false},
		{"prior run was in a managed workdir", agWTCase{transcriptAge: agWTMinutes(10), priorWorkDir: func(string) string { return filepath.Join(t.TempDir(), "task-0", "workdir") }}, false},
		{"prior directory not under an env root", agWTCase{transcriptAge: agWTMinutes(10), priorWorkDir: func(string) string { return filepath.Join(t.TempDir(), "elsewhere") }}, false},
		// A branch that cannot be proved the conversation's moves the run to a
		// fingerprint branch, forked from HEAD: the prior work is on the refused one.
		{"preferred branch rewritten, so refused", agWTCase{firstCommits: true, rewriteBranch: true, transcriptAge: agWTMinutes(10)}, false},
		// A prior run in a managed workdir may have left work this tree lacks,
		// even when the branch continues.
		{"continued branch, prior run was in a managed workdir", agWTCase{firstCommits: true, transcriptAge: agWTMinutes(10), priorWorkDir: func(string) string { return filepath.Join(t.TempDir(), "task-0", "workdir") }}, false},
		// A chat task never crosses directories, which the resume window would
		// otherwise leave unchecked: not even for a transcript three hours old.
		{"chat task, continued branch", agWTCase{chat: true, firstCommits: true, transcriptAge: agWTMinutes(10)}, false},
		{"chat task, continued branch, stale transcript", agWTCase{chat: true, firstCommits: true, transcriptAge: agWTMinutes(180)}, false},
		{"chat task, prior run committed nothing", agWTCase{chat: true, transcriptAge: agWTMinutes(10)}, false},
		// AC6: a missing session file falls back to a fresh start.
		{"missing transcript, nothing committed", agWTCase{}, false},
		{"missing transcript, continued branch", agWTCase{firstCommits: true}, false},
		// The record check: another branch in the repo holds this conversation's
		// work that HEAD lacks.
		{"continued on the fingerprint branch, preferred branch still held", agWTCase{firstCommits: true, preferredHeld: true, transcriptAge: agWTMinutes(10)}, true},
		{"agent renamed after committing", agWTCase{firstCommits: true, renameAgent: true, transcriptAge: agWTMinutes(10)}, false},
		{"work on the fingerprint branch, preferred branch deleted", agWTCase{firstCommits: true, preferredHeld: true, deletePreferred: true, transcriptAge: agWTMinutes(10)}, false},
		{"agent renamed, prior run committed nothing", agWTCase{renameAgent: true, transcriptAge: agWTMinutes(10)}, true},
		{"agent renamed, old branch merged into HEAD", agWTCase{firstCommits: true, mergeAfterFirst: true, renameAgent: true, transcriptAge: agWTMinutes(10)}, true},
		{"another conversation's unmerged record", agWTCase{otherConversation: true, transcriptAge: agWTMinutes(10)}, true},
		{"a record that cannot be read", agWTCase{otherConversation: true, lostRecord: true, transcriptAge: agWTMinutes(10)}, false},
		// AC4: no session from the server, no session in the run.
		{"no prior session id", agWTCase{noPriorSession: true, transcriptAge: agWTMinutes(10)}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := agWTRun(t, tt.c)

			if got.invocations != 1 {
				t.Fatalf("want one claude run, got %d\n%s", got.invocations, got.log)
			}
			if got.resumed() != tt.wantResume {
				t.Errorf("--resume passed = %v, want %v\nargs:\n%s\n%s", got.resumed(), tt.wantResume, got.args, got.log)
			}
			if got.copied() != tt.wantResume {
				t.Errorf("transcript copied into the new directory = %v, want %v", got.copied(), tt.wantResume)
			}
			if got.dirLine() != tt.wantResume {
				t.Errorf("working-directory line = %v, want %v\n%s", got.dirLine(), tt.wantResume, got.stdin)
			}
			// A session lost or left behind carries the notice, except when the
			// server never offered one.
			wantNotice := !tt.wantResume && !tt.c.noPriorSession
			if got.notice() != wantNotice {
				t.Errorf("continuity notice = %v, want %v\n%s", got.notice(), wantNotice, got.stdin)
			}
			if got.freshLogged() {
				t.Errorf("only the resume window logs a deliberate fresh start\n%s", got.log)
			}
		})
	}
}

// AC3: the resumed run works in its own worktree, and the prompt names it.
func TestAGPatchClaudeWorktreeResumeWorksInTheNewDirectory(t *testing.T) {
	got := agWTRun(t, agWTCase{transcriptAge: agWTMinutes(10)})

	if !got.resumed() {
		t.Fatalf("premise: the run should have resumed\n%s", got.args)
	}
	if got.second.WorkDir == got.first.WorkDir {
		t.Fatalf("premise: the second run should have a new directory, both %s", got.first.WorkDir)
	}
	want, _ := filepath.EvalSymlinks(got.second.WorkDir)
	if gotPwd, _ := filepath.EvalSymlinks(got.pwd); gotPwd != want {
		t.Errorf("claude ran in %q, want the new worktree %q", gotPwd, want)
	}
	if !strings.Contains(got.stdin, "`"+got.second.WorkDir+"`") {
		t.Errorf("the prompt should state %q:\n%s", got.second.WorkDir, got.stdin)
	}
	if strings.Contains(got.stdin, got.first.WorkDir) {
		t.Errorf("the prompt should not name the earlier directory %q:\n%s", got.first.WorkDir, got.stdin)
	}
}

// The 60-minute check must read the transcript's mtime before the copy, which
// writes a new one. A stale transcript on an otherwise resumable worktree
// starts fresh; a recent one resumes.
func TestAGPatchClaudeWorktreeResumeWindowReadsMtimeBeforeTheCopy(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		got := agWTRun(t, agWTCase{transcriptAge: agWTMinutes(180)})
		if got.resumed() {
			t.Errorf("a 3 hour old session resumed: the copy's fresh mtime was read instead of the original\n%s", got.args)
		}
		if !got.freshLogged() {
			t.Errorf("the deliberate fresh start should be logged\n%s", got.log)
		}
		if got.notice() || got.dirLine() {
			t.Errorf("a deliberate fresh start carries neither the notice nor the directory line\n%s", got.stdin)
		}
	})
	t.Run("just past the window", func(t *testing.T) {
		got := agWTRun(t, agWTCase{transcriptAge: agWTMinutes(61)})
		if got.resumed() || !got.freshLogged() {
			t.Errorf("a session past the window should start fresh: resumed=%v\n%s", got.resumed(), got.log)
		}
	})
	t.Run("recent", func(t *testing.T) {
		got := agWTRun(t, agWTCase{transcriptAge: agWTMinutes(59)})
		if !got.resumed() || got.freshLogged() {
			t.Errorf("a session inside the window should resume: resumed=%v\n%s", got.resumed(), got.log)
		}
	})
}

// A resume that keeps its directory (same directory, no worktree) is not told
// its directory: only a cross-directory resume gets the line.
func TestAGPatchWorkingDirectoryLineOnlyOnCrossDirectoryResume(t *testing.T) {
	for _, mode := range []agResumeMode{agResumeManaged, agResumeInPlace} {
		t.Run(string(mode), func(t *testing.T) {
			age := 10 * time.Minute
			got := agResumeRunTask(t, mode, &age, false)

			if !got.resumed(0) {
				t.Fatalf("premise: the run should have resumed\n%s", got.invocations[0].args)
			}
			if strings.Contains(got.invocations[0].stdin, "## Working directory") {
				t.Errorf("a same-directory resume should not get the directory line:\n%s", got.invocations[0].stdin)
			}
		})
	}
}

// AC5: another provider, on the setups where Claude resumes, behaves as it did:
// the changed directory drops the session with its notice, and nothing is
// copied or added to the prompt.
func TestAGPatchOtherProviderWorktreeRunUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name string
		c    agWTCase
	}{
		{"continued branch", agWTCase{firstCommits: true}},
		{"prior run committed nothing", agWTCase{}},
		{"branch merged into HEAD", agWTCase{firstCommits: true, mergeAfterFirst: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.provider, tt.c.transcriptAge = "grok", agWTMinutes(10)
			got := agWTRun(t, tt.c)

			if got.invocations != 1 {
				t.Fatalf("want one grok run, got %d\n%s", got.invocations, got.log)
			}
			if got.resumed() {
				t.Errorf("grok should not resume across directories:\n%s", got.stdin)
			}
			if got.copied() {
				t.Error("a Claude transcript was copied for another provider")
			}
			if got.dirLine() {
				t.Errorf("the directory line is Claude's only:\n%s", got.stdin)
			}
			if !got.notice() {
				t.Errorf("the dropped session should carry the continuity notice:\n%s", got.stdin)
			}
		})
	}
}

// A resume that Claude refuses after the transcript was copied takes the
// fresh-session retry. The retry is a cold run: the notice, and no directory
// line, which would claim the earlier turns used another directory.
func TestAGPatchClaudeWorktreeResumeRefusedAfterCopy(t *testing.T) {
	got := agWTRun(t, agWTCase{firstCommits: true, transcriptAge: agWTMinutes(10), rejectResume: true})

	if got.invocations != 2 {
		t.Fatalf("want the refused resume then one fresh retry, got %d runs\n%s", got.invocations, got.log)
	}
	if !got.copied() {
		t.Fatal("premise: the transcript should have been copied before the resume was refused")
	}
	runs := strings.Split(strings.TrimSuffix(got.args, "--invocation-end--\n"), "--invocation-end--\n")
	prompts := strings.Split(strings.TrimSuffix(got.stdin, "--prompt-end--\n"), "--prompt-end--\n")
	if len(runs) != 2 || len(prompts) != 2 {
		t.Fatalf("unexpected runs %q or prompts %q", runs, prompts)
	}
	if !strings.Contains(runs[0], "--resume\n"+got.first.SessionID+"\n") {
		t.Errorf("the first attempt should resume:\n%s", runs[0])
	}
	if !strings.Contains(prompts[0], "## Working directory") || strings.Contains(prompts[0], "## Session Continuity Notice") {
		t.Errorf("the resume attempt should carry the directory line and no notice:\n%s", prompts[0])
	}
	if strings.Contains(runs[1], "--resume") {
		t.Errorf("the retry should start a fresh session:\n%s", runs[1])
	}
	if strings.Contains(prompts[1], "## Working directory") {
		t.Errorf("the retry should not carry the directory line:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], "## Session Continuity Notice") {
		t.Errorf("the retry should carry the continuity notice:\n%s", prompts[1])
	}
}

package daemon

// AG patch: Claude resume window. Resuming a Claude session whose transcript
// was last written an hour or more ago misses the prompt cache and re-reads the
// whole history at the uncached rate, which costs more than a new session. So
// such a run starts fresh on purpose, on the cold prompt path and without the
// "prior session unavailable" notice (that notice is for a session we lost, not
// one we chose to leave).

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// agClaudeResumeWindow is the age at which a resume is no longer worth it.
// Exactly the window counts as over it.
const agClaudeResumeWindow = 60 * time.Minute

// agClaudeResumeNow is swapped in tests.
var agClaudeResumeNow = time.Now

// agClaudeSessionAge is the age of a prior Claude session's transcript, read
// once per run: the stable-path decision before the env root is claimed and the
// resume window after preparation both use this reading, so they cannot
// disagree about the same session.
type agClaudeSessionAge struct {
	age   time.Duration
	known bool
}

// over reports whether the session is at least agClaudeResumeWindow old.
// Exactly the window counts as over it. An unknown age is not over.
func (a agClaudeSessionAge) over() bool {
	return a.known && a.age >= agClaudeResumeWindow
}

// agClaudeSessionApplies reports whether the resume window is about this run:
// an issue run on the claude provider that carries a prior session.
func agClaudeSessionApplies(task *Task, provider string) bool {
	return provider == "claude" && task.PriorSessionID != "" && task.ChatSessionID == ""
}

// agReadClaudeSessionAge reads the transcript's age for a run the window
// applies to. When the age is unknown (no transcript, stat failure) the result
// is the zero value, and the existing gates handle the session.
func agReadClaudeSessionAge(task *Task, provider, workDir string) agClaudeSessionAge {
	if !agClaudeSessionApplies(task, provider) {
		return agClaudeSessionAge{}
	}
	env := os.Environ()
	if task.Agent != nil {
		for k, v := range task.Agent.CustomEnv {
			env = append(env, k+"="+v)
		}
	}
	modTime, err := agent.ClaudeSessionModTime(env, workDir, task.PriorSessionID)
	if err != nil {
		return agClaudeSessionAge{}
	}
	return agClaudeSessionAge{age: agClaudeResumeNow().Sub(modTime), known: true}
}

// agClaudeFreshAfterWindow clears the prior session of an issue run on the
// claude provider when its transcript is at least agClaudeResumeWindow old. It
// must run before the brief and prompt are built. Chat tasks and other
// providers are untouched. When the age is unknown it does nothing.
func agClaudeFreshAfterWindow(task *Task, taskCtx *execenv.TaskContextForEnv, provider, workDir string, taskLog *slog.Logger) {
	agClaudeApplyWindow(task, taskCtx, provider, agReadClaudeSessionAge(task, provider, workDir), taskLog)
}

// agClaudeApplyWindow is agClaudeFreshAfterWindow with the age already read.
func agClaudeApplyWindow(task *Task, taskCtx *execenv.TaskContextForEnv, provider string, age agClaudeSessionAge, taskLog *slog.Logger) {
	if !agClaudeSessionApplies(task, provider) || !age.over() {
		return
	}
	taskLog.Info("starting fresh Claude session: prior session is past the resume window",
		"provider", provider,
		"session_id", task.PriorSessionID,
		"age", age.age.Round(time.Second).String(),
		"window", agClaudeResumeWindow.String(),
	)
	task.PriorSessionID = ""
	taskCtx.PriorSessionResumed = false
}

// agClaudeMayMigrate reports whether a task is one whose Claude session may be
// copied into a new worktree directory. Chat tasks keep the base behaviour:
// agClaudeFreshAfterWindow leaves them alone, so a copy would resume them at
// any age.
func agClaudeMayMigrate(task *Task, provider string) bool {
	return provider == "claude" && task.ChatSessionID == ""
}

// agClaudeWorktreeResumable reports whether a Claude session from priorWorkDir
// may follow this run into its worktree: the worktree stands on the
// conversation's own branch and holds the prior run's work or the prior run
// left none (see agOwnBranchResumable), and the prior run was also in a
// worktree. A prior run in a managed workdir, or anywhere unknown, may have
// left work this tree does not hold. LocalWorktree.Continued is not read: it
// stays set when the branch was forked, which Codex accepts and this does not.
func agClaudeWorktreeResumable(env *execenv.Environment, priorWorkDir string) bool {
	if env == nil || env.LocalWorktree == nil {
		return false
	}
	return env.LocalWorktree.OwnBranchResumable && agIsWorktreePath(priorWorkDir)
}

// agIsWorktreePath reports whether dir is, or is inside, the "worktree"
// directory that worktree mode creates in a task's env root. The nearest of
// "worktree" and "workdir" decides, so a managed workdir under a path that
// happens to contain a "worktree" component is not mistaken for one.
func agIsWorktreePath(dir string) bool {
	for dir = filepath.Clean(dir); dir != "" && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		switch filepath.Base(dir) {
		case "worktree":
			return true
		case "workdir":
			return false
		}
	}
	return false
}

// agWithResumedWorkDir records the directory a resumed run is in, for
// agBuildResumedWorkDirBlock. Claude Code keeps the first directory in the
// resumed history, and a later hop has been seen to report it as the
// environment path.
func agWithResumedWorkDir(opts []PromptOption, resumed bool, workDir string) []PromptOption {
	if !resumed {
		return opts
	}
	return append(opts, func(o *promptOpts) { o.resumedWorkDir = workDir })
}

// agBuildResumedWorkDirBlock states the current working directory. It is
// per-turn text, after the cached prefix. It renders only while the task still
// carries a prior session, so a fresh-session retry drops it.
func agBuildResumedWorkDirBlock(task Task, workDir string) string {
	if task.PriorSessionID == "" || workDir == "" || strings.ContainsAny(workDir, "`\n") {
		return ""
	}
	return fmt.Sprintf("## Working directory\n\nThis run's working directory is `%s`. It differs from the one earlier turns used: do every file and git operation here, and ignore any earlier path.\n\n", workDir)
}

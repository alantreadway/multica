package daemon

// AG patch: Claude resume window. Resuming a Claude session whose transcript
// was last written an hour or more ago misses the prompt cache and re-reads the
// whole history at the uncached rate, which costs more than a new session. So
// such a run starts fresh on purpose, on the cold prompt path and without the
// "prior session unavailable" notice (that notice is for a session we lost, not
// one we chose to leave).

import (
	"log/slog"
	"os"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// agClaudeResumeWindow is the age at which a resume is no longer worth it.
// Exactly the window counts as over it.
const agClaudeResumeWindow = 60 * time.Minute

// Swapped in tests.
var (
	agClaudeResumeNow     = time.Now
	agClaudeSessionMtime  = agent.ClaudeSessionModTime
	agClaudeResumeEnviron = os.Environ
)

// agClaudeFreshAfterWindow clears the prior session of an issue run on the
// claude provider when its transcript is at least agClaudeResumeWindow old. It
// must run before the brief and prompt are built. Chat tasks and other
// providers are untouched. When the age is unknown (no transcript, stat
// failure) it does nothing and leaves the existing gates to handle the session.
func agClaudeFreshAfterWindow(task *Task, taskCtx *execenv.TaskContextForEnv, provider, workDir string, taskLog *slog.Logger) {
	if provider != "claude" || task.PriorSessionID == "" || task.ChatSessionID != "" {
		return
	}
	env := agClaudeResumeEnviron()
	if task.Agent != nil {
		for k, v := range task.Agent.CustomEnv {
			env = append(env, k+"="+v)
		}
	}
	modTime, err := agClaudeSessionMtime(env, workDir, task.PriorSessionID)
	if err != nil {
		return
	}
	age := agClaudeResumeNow().Sub(modTime)
	if age < agClaudeResumeWindow {
		return
	}
	taskLog.Info("starting fresh Claude session: prior session is past the resume window",
		"provider", provider,
		"session_id", task.PriorSessionID,
		"age", age.Round(time.Second).String(),
		"window", agClaudeResumeWindow.String(),
	)
	task.PriorSessionID = ""
	taskCtx.PriorSessionResumed = false
}

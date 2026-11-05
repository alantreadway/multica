package agent

// AG patch: Claude resume window. The daemon decides whether a Claude session
// is too old to resume; the transcript lookup it needs is unexported, so this
// wraps it.

import (
	"fmt"
	"os"
	"time"
)

// ClaudeSessionModTime returns the last write time of the Claude Code
// transcript for sessionID, using the same lookup as the usage snapshot: the
// newest <id>.jsonl under the projects directory of the config dir that env
// and cwd resolve to.
func ClaudeSessionModTime(env []string, cwd, sessionID string) (time.Time, error) {
	configDir, err := claudeConfigDir(env, cwd)
	if err != nil {
		return time.Time{}, err
	}
	path, err := findClaudeSessionFile(configDir, sessionID)
	if err != nil {
		return time.Time{}, err
	}
	// findClaudeSessionFile stats the file, but only returns the path.
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("stat Claude session: %w", err)
	}
	return info.ModTime(), nil
}

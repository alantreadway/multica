package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

// AG patch: the server rejects --until-status on the issue's own done or closed
// status; the CLI must fail and show the server's message, not swallow it.
func TestAGPatchIssueWakeupUntilClosingStatusShowsServerError(t *testing.T) {
	t.Chdir(t.TempDir())
	const issue = "a57c0511-1ebc-471d-a314-438ca16cc75d"
	const message = "a wakeup on this issue is disabled when the issue closes, so --until-status done (done category) can never fire; register it on another open issue (usually the parent) with --until-issue " + issue + " --until-issue-state done|ended"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")

	for _, status := range []string{"done", "cancelled"} {
		cmd := newIssueWakeupCommand()
		cmd.SetArgs([]string{"create", issue, "--instruction", "go", "--until-status", status})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("--until-status %s: no error", status)
		}
		if got := cli.FormatError(err, false); !strings.Contains(got, message) {
			t.Errorf("--until-status %s: user sees %q, want it to contain the server message", status, got)
		}
		if code := cli.ExitCodeFor(err); code == 0 {
			t.Errorf("--until-status %s: exit code 0", status)
		}
	}
}

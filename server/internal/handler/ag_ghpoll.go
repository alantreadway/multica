package handler

// AG patch: token-mode GitHub PR polling. See package ghpoll and PATCHES.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghpoll"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AGStartGitHubPoll starts the PR poller when the environment asks for it (see
// ghpoll.FromEnv). It returns at once; the loop ends with ctx. With the poller
// off it does nothing, so webhook behaviour is untouched either way.
func (h *Handler) AGStartGitHubPoll(ctx context.Context) {
	cfg, mode := ghpoll.FromEnv(os.Getenv)
	if mode == ghpoll.ModeOff {
		return
	}
	slog.Info("github: PR polling enabled", "mode", string(mode), "interval", cfg.Interval.String())
	poller := ghpoll.New(cfg, ghpoll.PGSource{DB: h.DB}, agPollSink{h: h})
	go poller.Run(ctx)
}

// agPollSink feeds polled PRs through the same mirror path a pull_request
// webhook takes. Rows it writes carry installation_id 0.
type agPollSink struct{ h *Handler }

// Mirror implements ghpoll.Sink.
func (s agPollSink) Mirror(ctx context.Context, workspaces []pgtype.UUID, pr ghpoll.PR) error {
	p, err := agPolledPayload(pr)
	if err != nil {
		return err
	}
	// The link policy only reads the workspace ids from the installation rows.
	insts := make([]db.GithubInstallation, len(workspaces))
	for i, ws := range workspaces {
		insts[i].WorkspaceID = ws
	}
	policy := s.h.resolvePRLinkPolicy(ctx, insts, p)

	var errs []error
	for _, ws := range workspaces {
		s.h.mirrorPullRequestForWorkspace(ctx, ws, 0, p, policy)
		// The mirror pass logs and swallows its own failures, so confirm the
		// row landed: it must now be at least as new as the PR we polled.
		stored, err := s.h.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
			WorkspaceID: ws,
			RepoOwner:   pr.Repo.Owner,
			RepoName:    pr.Repo.Name,
			PrNumber:    pr.Number,
		})
		if err != nil || stored.PrUpdatedAt.Time.Before(pr.UpdatedAt) {
			errs = append(errs, fmt.Errorf("workspace %s: pull request not stored", uuidToString(ws)))
		}
	}
	return errors.Join(errs...)
}

// Refresh implements ghpoll.Sink.
func (s agPollSink) Refresh(repo ghpoll.RepoRef, number int32) {
	s.h.PRRefresh.Enqueue(0, repo.Owner, repo.Name, number)
}

// agPolledPayload maps a REST list item onto the webhook payload the mirror
// pass reads. The list endpoint has no `merged`, `mergeable_state` or diff
// stats: merged follows merged_at, the other two stay zero, and the snapshot
// pipeline supplies mergeability.
func agPolledPayload(pr ghpoll.PR) (*ghPullRequestPayload, error) {
	var p ghPullRequestPayload
	if err := json.Unmarshal(pr.Raw, &p.PullRequest); err != nil {
		return nil, fmt.Errorf("decode polled pull request: %w", err)
	}
	p.PullRequest.Merged = strings.TrimSpace(p.PullRequest.MergedAt) != ""
	p.Repository.Name = pr.Repo.Name
	p.Repository.Owner.Login = pr.Repo.Owner
	return &p, nil
}

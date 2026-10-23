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
	poller := ghpoll.New(cfg, ghpoll.PGSource{DB: h.DB}, agPollSink{h: h, appOptIn: mode == ghpoll.ModeAppOptIn})
	go poller.Run(ctx)
}

// agPollSink feeds polled PRs through the same mirror path a pull_request
// webhook takes. Rows it writes carry installation_id 0. appOptIn is true when
// the App is configured too: its webhooks own rows with a real installation id.
type agPollSink struct {
	h        *Handler
	appOptIn bool
}

// Mirror implements ghpoll.Sink.
func (s agPollSink) Mirror(ctx context.Context, workspaces []pgtype.UUID, pr ghpoll.PR) error {
	p, err := agPolledPayload(pr)
	if err != nil {
		return err
	}
	// With the App beside the poller, a row a webhook wrote carries a real
	// installation id; a poll would rewrite it to 0 and strand its refreshes, so
	// leave it to the webhook. In token mode no webhook arrives: take it over.
	owned := workspaces
	if s.appOptIn {
		owned = nil
		for _, ws := range workspaces {
			if !s.webhookOwned(ctx, ws, pr) {
				owned = append(owned, ws)
			}
		}
	}
	if len(owned) == 0 {
		return nil
	}
	// The link policy only reads the workspace ids from the installation rows.
	insts := make([]db.GithubInstallation, len(owned))
	for i, ws := range owned {
		insts[i].WorkspaceID = ws
	}
	policy := s.h.resolvePRLinkPolicy(ctx, insts, p)

	var errs []error
	for _, ws := range owned {
		s.h.mirrorPullRequestForWorkspace(ctx, ws, 0, p, policy)
		// The mirror pass logs and swallows its own failures, so confirm the
		// row landed: it must now be at least as new as the PR we polled.
		stored, err := s.getRow(ctx, ws, pr)
		if err != nil || stored.PrUpdatedAt.Time.Before(pr.UpdatedAt) {
			errs = append(errs, fmt.Errorf("workspace %s: pull request not stored", uuidToString(ws)))
		}
	}
	return errors.Join(errs...)
}

func (s agPollSink) getRow(ctx context.Context, ws pgtype.UUID, pr ghpoll.PR) (db.GithubPullRequest, error) {
	return s.h.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: ws,
		RepoOwner:   pr.Repo.Owner,
		RepoName:    pr.Repo.Name,
		PrNumber:    pr.Number,
	})
}

// webhookOwned reports whether ws already stores pr under a real installation.
func (s agPollSink) webhookOwned(ctx context.Context, ws pgtype.UUID, pr ghpoll.PR) bool {
	row, err := s.getRow(ctx, ws, pr)
	return err == nil && row.InstallationID != 0
}

// Refresh implements ghpoll.Sink.
func (s agPollSink) Refresh(repo ghpoll.RepoRef, number int32) {
	// Only rows the poller owns (installation 0) route here; a PR whose rows are
	// all webhook-owned is refreshed by those webhooks.
	rows, err := s.h.Queries.ListGitHubPRRowsByAddress(context.Background(), db.ListGitHubPRRowsByAddressParams{
		InstallationID: 0,
		RepoOwner:      repo.Owner,
		RepoName:       repo.Name,
		PrNumber:       number,
	})
	if err != nil || len(rows) == 0 {
		return
	}
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

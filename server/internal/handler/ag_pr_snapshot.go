package handler

// AG patch: extra GitHub PR snapshot fields on GET /api/issues/{id}/pull-requests
// — per-check required-ness and run fields, a required-checks rollup, review
// decision, latest reviews, base ref and the mirrored head SHA — and the
// no-refresh-on-view rule for finished PRs. Kept in this file behind small
// hooks in github.go (the embedded AGPRSnapshotFields, agSetPRHeadSHA,
// agSkipViewRefresh and the agEnrichPullRequests call). See
// ag-patches/PATCHES.md.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AGPRSnapshotFields is embedded in GitHubPullRequestResponse, so its fields sit
// at the top level of each pull request. All are null when no current snapshot
// carries them (and for non-GitHub providers).
type AGPRSnapshotFields struct {
	// Checks is the PR's current snapshot check list, in GitHub's order.
	Checks []AGPRCheck `json:"checks"`
	// RequiredChecksState rolls up only the checks GitHub marks required for the
	// PR's base branch: "success" when every one passed, "failure" when any
	// failed, "pending" otherwise. null when the snapshot has no checks, or any
	// check lacks required-ness (the snapshot could not fetch it), so a missing
	// answer is never reported as success. A snapshot where no check is required
	// is "success": nothing blocks the merge on CI.
	RequiredChecksState *string `json:"required_checks_state"`
	// ReviewDecision is GitHub's reviewDecision, lowercased: "approved" |
	// "changes_requested" | "review_required" | null (no review requirement, or
	// no snapshot).
	ReviewDecision *string `json:"review_decision"`
	// LatestReviews is one entry per reviewer's latest review, or null when the
	// snapshot did not carry them.
	LatestReviews []AGPRReview `json:"latest_reviews"`
	// BaseRefName is the branch the PR targets, or null.
	BaseRefName *string `json:"base_ref_name"`
	// HeadSHA is the head the mirrored PR row holds (the value the snapshot's
	// head is compared with), or null when the row has none. Present whether or
	// not a current snapshot exists.
	HeadSHA *string `json:"head_sha"`
}

// AGPRCheck is one check of the current snapshot.
type AGPRCheck struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	DetailsURL *string `json:"details_url"`
	// Required is null when the snapshot did not say.
	Required *bool `json:"required"`
	// The run fields belong to this row's own check run, not to its name. All
	// are null when the snapshot did not carry them: older snapshot, commit
	// status, a check run outside Actions, or the extras query not answering.
	// WorkflowName and RunID are the Actions workflow run; StartedAt is RFC3339
	// UTC; SuiteStatus and SuiteConclusion are the check suite's, lowercased.
	WorkflowName    *string `json:"workflow_name"`
	RunID           *int64  `json:"run_id"`
	StartedAt       *string `json:"started_at"`
	SuiteStatus     *string `json:"suite_status"`
	SuiteConclusion *string `json:"suite_conclusion"`
}

// AGPRReview is a reviewer's latest review.
type AGPRReview struct {
	Author      string `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// agEnrichPullRequests fills the AG fields of the GitHub PRs in out, which were
// built for issueID by ListPullRequestsForIssue. Only PRs with a current
// snapshot are filled; the rest keep null fields.
func (h *Handler) agEnrichPullRequests(ctx context.Context, issueID pgtype.UUID, out []GitHubPullRequestResponse) error {
	byID := make(map[string]*GitHubPullRequestResponse, len(out))
	for i := range out {
		if out[i].Provider == "github" && out[i].SnapshotAvailable != nil && *out[i].SnapshotAvailable {
			byID[out[i].ID] = &out[i]
		}
	}
	if len(byID) == 0 {
		return nil
	}
	extras, err := h.Queries.AGListPullRequestSnapshotExtras(ctx, issueID)
	if err != nil {
		return err
	}
	for _, row := range extras {
		pr := byID[uuidToString(row.ID)]
		if pr == nil {
			continue
		}
		pr.ReviewDecision = lowerTextPtr(row.ReviewDecision)
		pr.LatestReviews = agParseLatestReviews(row.LatestReviews)
		pr.BaseRefName = textToPtr(row.BaseRefName)
		pr.Checks = []AGPRCheck{}
	}
	checks, err := h.Queries.AGListPullRequestChecksForIssue(ctx, issueID)
	if err != nil {
		return err
	}
	for _, row := range checks {
		pr := byID[uuidToString(row.PrID)]
		if pr == nil {
			continue
		}
		check := AGPRCheck{
			Name:       row.Name,
			Status:     row.Status,
			Conclusion: textToPtr(row.Conclusion),
			DetailsURL: textToPtr(row.DetailsUrl),
		}
		if row.IsRequired.Valid {
			required := row.IsRequired.Bool
			check.Required = &required
		}
		check.WorkflowName = textToPtr(row.WorkflowName)
		if row.RunID.Valid {
			runID := row.RunID.Int64
			check.RunID = &runID
		}
		if row.StartedAt.Valid {
			startedAt := row.StartedAt.Time.UTC().Format(time.RFC3339)
			check.StartedAt = &startedAt
		}
		check.SuiteStatus = lowerTextPtr(row.SuiteStatus)
		check.SuiteConclusion = lowerTextPtr(row.SuiteConclusion)
		pr.Checks = append(pr.Checks, check)
	}
	for _, pr := range byID {
		pr.RequiredChecksState = agChecksRollup(pr.Checks)
	}
	return nil
}

// agSetPRHeadSHA sets HeadSHA from the mirrored row.
func agSetPRHeadSHA(resp *GitHubPullRequestResponse, headSHA string) {
	if headSHA != "" {
		resp.HeadSHA = &headSHA
	}
}

// agSkipViewRefresh reports whether opening the PR's card must not re-fetch its
// snapshot: a merged or closed PR that already has a current-head snapshot is
// final, and a 60s on-view refresh of it would only spend GitHub quota on every
// read of the list. Open and draft PRs, and finished ones with no snapshot,
// keep upstream's behaviour.
func agSkipViewRefresh(row db.ListPullRequestsByIssueRow) bool {
	return (row.State == "merged" || row.State == "closed") &&
		row.SnapshotFetchedAt.Valid &&
		row.SnapshotHeadSha != "" &&
		row.SnapshotHeadSha == row.HeadSha
}

// agChecksRollup is AGPRSnapshotFields.RequiredChecksState for a snapshot's
// checks.
func agChecksRollup(checks []AGPRCheck) *string {
	states := make([]ghsnapshot.AGCheckState, 0, len(checks))
	for _, c := range checks {
		state := ghsnapshot.AGCheckState{Status: c.Status, Required: c.Required}
		if c.Conclusion != nil {
			state.Conclusion = *c.Conclusion
		}
		states = append(states, state)
	}
	return ghsnapshot.AGRequiredChecksState(states)
}

// agParseLatestReviews decodes the latest_reviews JSONB column. A null or
// malformed column is "not carried", not an empty list.
func agParseLatestReviews(raw []byte) []AGPRReview {
	if len(raw) == 0 {
		return nil
	}
	var stored []struct {
		Author      string `json:"author"`
		State       string `json:"state"`
		SubmittedAt string `json:"submitted_at"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return nil
	}
	out := make([]AGPRReview, 0, len(stored))
	for _, r := range stored {
		out = append(out, AGPRReview{Author: r.Author, State: strings.ToLower(r.State), SubmittedAt: r.SubmittedAt})
	}
	return out
}

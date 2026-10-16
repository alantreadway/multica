package ghsnapshot

// AG patch: extra PR snapshot fields — per-check required-ness (isRequired),
// reviewDecision, latestReviews and baseRefName. Kept in this file behind small
// hooks in snapshot.go, refresh.go and client.go so the patch rebases cleanly.
// See ag-patches/PATCHES.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxAGLatestReviews caps latestReviews. GitHub keeps one entry per reviewer,
// so 20 covers any realistic PR.
const maxAGLatestReviews = 20

// GraphQL fragments spliced into prSnapshotQueryTemplate. The base query (no
// fragments) is the fallback when GitHub rejects the extended one.
const (
	agPRFields            = ` baseRefName reviewDecision latestReviews(first:20){nodes{author{login} state submittedAt}}`
	agCheckRunFields      = ` isRequired(pullRequestNumber:$number)`
	agStatusContextFields = ` isRequired(pullRequestNumber:$number)`
)

// prSnapshotQuery returns the snapshot query, with the AG fields when extras is
// true.
func prSnapshotQuery(extras bool) string {
	if !extras {
		return fmt.Sprintf(prSnapshotQueryTemplate, "", "", "")
	}
	return fmt.Sprintf(prSnapshotQueryTemplate, agPRFields, agCheckRunFields, agStatusContextFields)
}

// GraphQLError is a query-level error in a GitHub GraphQL response (HTTP 200
// with an `errors` array), as opposed to a transport, status or rate-limit
// failure.
type GraphQLError struct{ Message string }

func (e *GraphQLError) Error() string { return "github graphql error: " + e.Message }

// AGReview is one reviewer's latest review.
type AGReview struct {
	Author      string `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// AGSnapshotExtras carries the AG fields of a PRSnapshot. Available is false
// when the extended query was not answered (GitHub or the token rejected it, so
// the base query ran instead): everything below is then unknown, which is not
// the same as "no reviews".
type AGSnapshotExtras struct {
	Available bool
	// ReviewDecision is GitHub's raw enum (APPROVED / CHANGES_REQUESTED /
	// REVIEW_REQUIRED), empty when GitHub returned null.
	ReviewDecision string
	BaseRefName    string
	LatestReviews  []AGReview
}

// agPRExtras is embedded in graphqlPullRequest.
type agPRExtras struct {
	BaseRefName    *string `json:"baseRefName"`
	ReviewDecision *string `json:"reviewDecision"`
	LatestReviews  *struct {
		Nodes []struct {
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
			State       string `json:"state"`
			SubmittedAt string `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"latestReviews"`
}

func (x agPRExtras) snapshotExtras(extras bool) AGSnapshotExtras {
	if !extras {
		return AGSnapshotExtras{}
	}
	out := AGSnapshotExtras{Available: true}
	if x.BaseRefName != nil {
		out.BaseRefName = *x.BaseRefName
	}
	if x.ReviewDecision != nil {
		out.ReviewDecision = *x.ReviewDecision
	}
	if x.LatestReviews != nil {
		for _, n := range x.LatestReviews.Nodes {
			r := AGReview{State: n.State, SubmittedAt: n.SubmittedAt}
			if n.Author != nil {
				r.Author = n.Author.Login
			}
			out.LatestReviews = append(out.LatestReviews, r)
		}
	}
	return out
}

// agGraphQL runs one page of the snapshot query. On the first page, a
// query-level GraphQL error from the extended query (a token that cannot see
// isRequired, a field GitHub does not serve) drops *extras and retries with
// the base query, so the new fields can never fail the snapshot. Rate limits,
// transport errors and later-page errors are returned as they are.
func (c *Client) agGraphQL(ctx context.Context, installationID int64, extras *bool, page int, vars map[string]any) (json.RawMessage, error) {
	data, err := c.graphQL(ctx, installationID, prSnapshotQuery(*extras), vars)
	var gqlErr *GraphQLError
	if err != nil && *extras && page == 0 && errors.As(err, &gqlErr) {
		*extras = false
		return c.graphQL(ctx, installationID, prSnapshotQuery(false), vars)
	}
	return data, err
}

// agApplySnapshotExtras writes the AG fields of snap for prID, inside the
// snapshot transaction and after the check rows are inserted. Unavailable
// extras write NULLs, so a value from an earlier snapshot never outlives it.
func agApplySnapshotExtras(ctx context.Context, q *db.Queries, prID pgtype.UUID, snap *PRSnapshot) error {
	params := db.AGUpdateGitHubPRSnapshotExtrasParams{PrID: prID}
	if snap.AG.Available {
		params.ReviewDecision = textOrNull(snap.AG.ReviewDecision)
		params.BaseRefName = textOrNull(snap.AG.BaseRefName)
		reviews := snap.AG.LatestReviews
		if len(reviews) > maxAGLatestReviews {
			reviews = reviews[:maxAGLatestReviews]
		}
		if reviews == nil {
			reviews = []AGReview{}
		}
		raw, err := json.Marshal(reviews)
		if err != nil {
			return err
		}
		params.LatestReviews = raw
	}
	if err := q.AGUpdateGitHubPRSnapshotExtras(ctx, params); err != nil {
		return err
	}

	var required, notRequired []int32
	for i, c := range snap.Contexts {
		switch {
		case c.Required == nil:
		case *c.Required:
			required = append(required, int32(i))
		default:
			notRequired = append(notRequired, int32(i))
		}
	}
	for _, set := range []struct {
		ordinals []int32
		required bool
	}{{required, true}, {notRequired, false}} {
		if len(set.ordinals) == 0 {
			continue
		}
		if err := q.AGSetGitHubPRCheckRunRequired(ctx, db.AGSetGitHubPRCheckRunRequiredParams{
			PrID: prID, Required: set.required, Ordinals: set.ordinals,
		}); err != nil {
			return err
		}
	}
	return nil
}

// AGCheckState is the part of a check that decides the required-checks rollup.
type AGCheckState struct {
	Status     string // "queued" / "in_progress" / "completed"
	Conclusion string // empty while running
	// Required is nil when the snapshot did not say.
	Required *bool
}

// AGRequiredChecksState rolls up the required checks of a snapshot: "success"
// when every required check passed, "failure" when any failed, "pending"
// otherwise. nil when there are no checks or any check lacks required-ness (the
// snapshot could not fetch it), so a missing answer is never reported as
// success. A snapshot where no check is required is "success": nothing blocks
// the merge on CI.
func AGRequiredChecksState(checks []AGCheckState) *string {
	if len(checks) == 0 {
		return nil
	}
	state := "success"
	for _, c := range checks {
		if c.Required == nil {
			return nil
		}
		if !*c.Required {
			continue
		}
		switch {
		case c.Status != "completed" || c.Conclusion == "":
			if state == "success" {
				state = "pending"
			}
		case agCheckFailed(c.Conclusion):
			state = "failure"
		case !agCheckPassed(c.Conclusion):
			if state == "success" {
				state = "pending"
			}
		}
	}
	return &state
}

// The conclusion sets match ListPullRequestsByIssue's failed / passed counts.
func agCheckFailed(conclusion string) bool {
	switch conclusion {
	case "failure", "cancelled", "timed_out", "action_required", "startup_failure", "stale", "error":
		return true
	}
	return false
}

func agCheckPassed(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

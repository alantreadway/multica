package ghsnapshot

// AG patch: extra PR snapshot fields — per-check required-ness (isRequired),
// reviewDecision, latestReviews, baseRefName and each check run's own workflow
// run (workflow name, run id, start time, suite status). Kept in this file behind small
// hooks in snapshot.go and refresh.go so the patch rebases cleanly.
// See ag-patches/PATCHES.md.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxAGLatestReviews caps latestReviews. GitHub keeps one entry per reviewer,
// so 20 covers any realistic PR.
const maxAGLatestReviews = 20

// agExtrasQuery reads the AG fields for the PR head. Upstream's snapshot is
// REST-only since v0.6.4, and REST has no reviewDecision or per-check
// isRequired, so the extras come from this one GraphQL query, paginated over the
// head's check contexts like the pre-v0.6.4 snapshot query was.
const agExtrasQuery = `query($owner:String!,$repo:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      headRefOid
      baseRefName
      reviewDecision
      latestReviews(first:20){nodes{author{login} state submittedAt}}
      commits(last:1){nodes{commit{
        statusCheckRollup{
          contexts(first:100,after:$cursor){
            pageInfo{hasNextPage endCursor}
            nodes{
              __typename
              ... on CheckRun{
                databaseId name startedAt isRequired(pullRequestNumber:$number)
                checkSuite{status conclusion workflowRun{databaseId workflow{name}}}
              }
              ... on StatusContext{context isRequired(pullRequestNumber:$number)}
            }
          }
        }
      }}}
    }
  }
}`

// AGCheckRun is the part of a check run that belongs to the run itself, not to
// its name: the same name can appear on several rows, one per superseded run.
// It is set on a CheckContext only when the extras query answered for that
// exact check run, so nil always means unknown. Within it, an empty string, a
// zero RunID and a zero StartedAt are null (GitHub omitted the field, as for a
// check run that is not an Actions job).
type AGCheckRun struct {
	WorkflowName    string
	RunID           int64
	StartedAt       time.Time
	SuiteStatus     string
	SuiteConclusion string
}

// AGReview is one reviewer's latest review.
type AGReview struct {
	Author      string `json:"author"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// AGSnapshotExtras carries the AG fields of a PRSnapshot. Available is false
// when the extras query was not answered (GitHub or the credentials rejected
// it): everything below is then unknown, which is not the same as "no
// reviews".
type AGSnapshotExtras struct {
	Available bool
	// ReviewDecision is GitHub's raw enum (APPROVED / CHANGES_REQUESTED /
	// REVIEW_REQUIRED), empty when GitHub returned null.
	ReviewDecision string
	BaseRefName    string
	LatestReviews  []AGReview
}

type agExtrasPullRequest struct {
	HeadRefOid     string  `json:"headRefOid"`
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
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []agExtrasContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type agExtrasContext struct {
	Typename   string `json:"__typename"`
	DatabaseID int64  `json:"databaseId"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	IsRequired *bool  `json:"isRequired"`
	StartedAt  string `json:"startedAt"`
	CheckSuite *struct {
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
			Workflow   *struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

func (x *agExtrasPullRequest) snapshotExtras() AGSnapshotExtras {
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

// agCheckKey matches a REST check to its GraphQL context: check runs by name,
// commit statuses by context.
func agCheckKey(isStatusContext bool, name string) string {
	if isStatusContext {
		return "status\x00" + name
	}
	return "run\x00" + name
}

// agCheckRunFields is the AGCheckRun of one GraphQL CheckRun node.
func agCheckRunFields(n *agExtrasContext) *AGCheckRun {
	out := &AGCheckRun{}
	if t, err := time.Parse(time.RFC3339, n.StartedAt); err == nil {
		out.StartedAt = t.UTC()
	}
	if suite := n.CheckSuite; suite != nil {
		out.SuiteStatus = strings.ToLower(suite.Status)
		out.SuiteConclusion = strings.ToLower(suite.Conclusion)
		if run := suite.WorkflowRun; run != nil {
			out.RunID = run.DatabaseID
			if run.Workflow != nil {
				out.WorkflowName = run.Workflow.Name
			}
		}
	}
	return out
}

// agMergeRequired combines two answers for checks sharing a key: required wins,
// then unknown, so a duplicate name can only make the rollup stricter.
func agMergeRequired(a, b *bool) *bool {
	switch {
	case a != nil && *a:
		return a
	case b != nil && *b:
		return b
	case a == nil || b == nil:
		return nil
	}
	return a
}

// agFetchSnapshotExtras fills snap.AG and each context's Required from the
// extras query, for the head the REST snapshot read. The new fields never fail
// a snapshot: any GitHub answer other than a rate limit (a GraphQL error, a
// permission or status failure, a malformed page) leaves the extras unavailable
// and every Required nil. A rate limit, a cancelled context and a head that moved
// fail the snapshot as upstream's own fetch does, so the refresh queue retries.
// A REST check with no GraphQL counterpart keeps Required nil.
func (c *Client) agFetchSnapshotExtras(ctx context.Context, installationID int64, owner, repo string, number int32, snap *PRSnapshot) error {
	vars := map[string]any{"owner": owner, "repo": repo, "number": number}
	required := map[string]*bool{}
	runs := map[int64]*agExtrasContext{}
	var extras AGSnapshotExtras
	unavailable := func(err error) error {
		var rl *RateLimitError
		if errors.As(err, &rl) || ctx.Err() != nil {
			return err
		}
		snap.AG = AGSnapshotExtras{}
		for i := range snap.Contexts {
			snap.Contexts[i].Required = nil
			snap.Contexts[i].AGRun = nil
		}
		return nil
	}
	cursor := ""
	for page := 0; ; page++ {
		if page == maxSnapshotContextPages {
			return unavailable(errors.New("ghsnapshot: extras pagination exceeds page limit"))
		}
		vars["cursor"] = nil
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := c.graphQL(ctx, installationID, agExtrasQuery, vars)
		if err != nil {
			return unavailable(err)
		}
		var resp struct {
			Repository *struct {
				PullRequest *agExtrasPullRequest `json:"pullRequest"`
			} `json:"repository"`
		}
		if json.Unmarshal(data, &resp) != nil || resp.Repository == nil || resp.Repository.PullRequest == nil {
			return unavailable(errors.New("ghsnapshot: malformed extras data"))
		}
		pr := resp.Repository.PullRequest
		if pr.HeadRefOid != snap.HeadSHA {
			return errors.New("ghsnapshot: pull request head changed during fetch")
		}
		if page == 0 {
			extras = pr.snapshotExtras()
		}
		if len(pr.Commits.Nodes) == 0 || pr.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
			break
		}
		contexts := pr.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts
		for i, n := range contexts.Nodes {
			var key string
			switch n.Typename {
			case "CheckRun":
				key = agCheckKey(false, n.Name)
				if n.DatabaseID > 0 {
					if _, dup := runs[n.DatabaseID]; dup {
						// One id twice is not a row we can trust.
						runs[n.DatabaseID] = nil
					} else {
						runs[n.DatabaseID] = &contexts.Nodes[i]
					}
				}
			case "StatusContext":
				key = agCheckKey(true, n.Context)
			default:
				continue
			}
			if prev, seen := required[key]; seen {
				required[key] = agMergeRequired(prev, n.IsRequired)
			} else {
				required[key] = n.IsRequired
			}
		}
		if !contexts.PageInfo.HasNextPage {
			break
		}
		if contexts.PageInfo.EndCursor == "" || contexts.PageInfo.EndCursor == cursor {
			return unavailable(errors.New("ghsnapshot: extras pagination cursor did not advance"))
		}
		cursor = contexts.PageInfo.EndCursor
	}
	snap.AG = extras
	for i := range snap.Contexts {
		snap.Contexts[i].Required = required[agCheckKey(snap.Contexts[i].IsStatusContext, snap.Contexts[i].Name)]
		// Run fields are per row, matched on the check run's own id: names repeat
		// across superseded runs, so a name match could give a row another run's.
		if c := &snap.Contexts[i]; !c.IsStatusContext && c.AGCheckRunID > 0 {
			if n := runs[c.AGCheckRunID]; n != nil && n.Name == c.Name {
				c.AGRun = agCheckRunFields(n)
			}
		}
	}
	return nil
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
		if c.AGRun != nil {
			if err := q.AGSetGitHubPRCheckRunRunFields(ctx, db.AGSetGitHubPRCheckRunRunFieldsParams{
				PrID:            prID,
				Ordinal:         int32(i),
				WorkflowName:    textOrNull(c.AGRun.WorkflowName),
				RunID:           pgtype.Int8{Int64: c.AGRun.RunID, Valid: c.AGRun.RunID > 0},
				StartedAt:       pgtype.Timestamptz{Time: c.AGRun.StartedAt, Valid: !c.AGRun.StartedAt.IsZero()},
				SuiteStatus:     textOrNull(c.AGRun.SuiteStatus),
				SuiteConclusion: textOrNull(c.AGRun.SuiteConclusion),
			}); err != nil {
				return err
			}
		}
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

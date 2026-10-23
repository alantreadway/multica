package main

// AG patch: `multica issue pull-requests link|unlink`. Attaches a PR the
// workspace already mirrors to an issue (or removes it) through the existing
// POST/DELETE /api/issues/{id}/pull-requests endpoints. The listing
// (`pull-requests <id>`) is unchanged. See ag-patches/PATCHES.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issuePullRequestsLinkCmd = &cobra.Command{
	Use:   "link <issue> <pr-url>",
	Short: "Link a pull request to an issue by URL",
	Long: "Link a pull request to an issue by its URL, then print the issue's pull requests.\n" +
		"The pull request must already be mirrored by the workspace (its repository is connected\n" +
		"and the PR has been synced); the server does not fetch it on demand.",
	Args: exactArgs(2),
	RunE: runIssuePullRequestsLink,
}

var issuePullRequestsUnlinkCmd = &cobra.Command{
	Use:   "unlink <issue> <pr-url|pr-id>",
	Short: "Unlink a pull request from an issue",
	Long: "Remove a pull request from an issue, then print the issue's remaining pull requests.\n" +
		"The pull request is given by URL (matched against the issue's linked pull requests) or by\n" +
		"the id shown in `pull-requests --output json`.",
	Args: exactArgs(2),
	RunE: runIssuePullRequestsUnlink,
}

func init() {
	for _, c := range []*cobra.Command{issuePullRequestsLinkCmd, issuePullRequestsUnlinkCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
		issuePullRequestsCmd.AddCommand(c)
	}
}

func runIssuePullRequestsLink(cmd *cobra.Command, args []string) error {
	prURL := strings.TrimSpace(args[1])
	if prURL == "" {
		return errors.New("pull request URL is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	err = client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/pull-requests", map[string]string{"url": prURL}, &result)
	if err != nil {
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("pull request %s has not been mirrored yet: %s", prURL, agPRServerMessage(httpErr))
		}
		return fmt.Errorf("link pull request: %w", agPRServerError(err))
	}
	return agPrintIssuePullRequests(cmd, result)
}

func runIssuePullRequestsUnlink(cmd *cobra.Command, args []string) error {
	ref := strings.TrimSpace(args[1])
	if ref == "" {
		return errors.New("pull request URL or id is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	base := "/api/issues/" + url.PathEscape(issueRef.ID) + "/pull-requests"

	prID := ref
	if _, err := uuid.Parse(ref); err != nil {
		var listed map[string]any
		if err := client.GetJSON(ctx, base, &listed); err != nil {
			return fmt.Errorf("list issue pull requests: %w", err)
		}
		prs, _ := listed["pull_requests"].([]any)
		prID = agFindLinkedPullRequest(normalizePullRequestList(prs), ref)
		if prID == "" {
			return fmt.Errorf("no pull request matching %q is linked to %s", ref, args[0])
		}
	}

	var result map[string]any
	if err := client.DeleteJSONResponse(ctx, base+"/"+url.PathEscape(prID), &result); err != nil {
		return fmt.Errorf("unlink pull request: %w", agPRServerError(err))
	}
	return agPrintIssuePullRequests(cmd, result)
}

func agPrintIssuePullRequests(cmd *cobra.Command, result map[string]any) error {
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	prs, _ := result["pull_requests"].([]any)
	printIssuePullRequestsTable(normalizePullRequestList(prs))
	return nil
}

// agFindLinkedPullRequest returns the id of the listed pull request whose URL
// matches ref, ignoring case, a trailing slash, a query or fragment and a
// sub-page such as /files. "" when none matches.
func agFindLinkedPullRequest(prs []map[string]any, ref string) string {
	want := agNormalizePRURL(ref)
	if want == "" {
		return ""
	}
	for _, pr := range prs {
		if agNormalizePRURL(pullRequestURL(pr)) == want {
			return strVal(pr, "id")
		}
	}
	return ""
}

var agPRURLPattern = regexp.MustCompile(`^(https?://[^/?#]+/.+?/(?:pull|pulls|merge_requests)/\d+)(?:[/?#].*)?$`)

func agNormalizePRURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	m := agPRURLPattern.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return strings.ToLower(strings.TrimRight(m[1], "/"))
}

// agPRServerMessage is the server's own message for an error response: the
// `error` field of its JSON body, else the raw body.
func agPRServerMessage(e *cli.HTTPError) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &body) == nil && body.Error != "" {
		return body.Error
	}
	return e.Body
}

// agPRServerError reduces an HTTP error to the server's message, so the CLI
// prints what the server said rather than the request line.
func agPRServerError(err error) error {
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) {
		return errors.New(agPRServerMessage(httpErr))
	}
	return err
}

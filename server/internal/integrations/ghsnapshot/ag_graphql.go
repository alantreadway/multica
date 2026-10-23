package ghsnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AG patch: GitHub GraphQL client (see ag-patches/PATCHES.md).

// GraphQLError is a query-level error in a GitHub GraphQL response (HTTP 200
// with an `errors` array), as opposed to a transport, status or rate-limit
// failure.
type GraphQLError struct{ Message string }

func (e *GraphQLError) Error() string { return "github graphql error: " + e.Message }

// graphQL runs one GraphQL query as the given installation and returns the raw
// `data` object. Upstream dropped its GraphQL client in v0.6.4 (REST only);
// this is that client, used by the PR extras query and the token-mode tests.
// GitHub answers HTTP 200 for query-level errors, so the `errors` array is
// checked too: RATE_LIMITED maps to a RateLimitError, anything else to a
// GraphQLError.
func (c *Client) graphQL(ctx context.Context, installationID int64, query string, variables map[string]any) (json.RawMessage, error) {
	token, err := c.installationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.apiBase, "/") + "/graphql"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if isRateLimited(resp, body) {
		return nil, rateLimitFromResponse(resp, c.now())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql: unexpected status %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("github graphql: malformed response")
	}
	if len(envelope.Errors) > 0 {
		for _, e := range envelope.Errors {
			if e.Type == "RATE_LIMITED" {
				return nil, &RateLimitError{RetryAfter: time.Minute}
			}
		}
		// GraphQL error messages do not contain credentials.
		return nil, &GraphQLError{Message: envelope.Errors[0].Message}
	}
	if len(envelope.Data) == 0 {
		return nil, errors.New("github graphql: empty data")
	}
	return envelope.Data, nil
}

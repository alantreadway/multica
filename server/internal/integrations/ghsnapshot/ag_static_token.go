package ghsnapshot

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// AG patch: token-mode GitHub PR polling (see ag-patches/PATCHES.md).
//
// A deployment without a GitHub App can still use the snapshot pipeline when
// GITHUB_POLL_TOKEN holds a personal access token (e.g. `gh auth token`). The
// token authenticates every GraphQL and REST call as installation 0, which is
// the installation id the poller writes on the PR rows it mirrors. The token is
// an opaque secret: it is never logged and never put in an error.

// agPollTokenFromEnv returns the trimmed GITHUB_POLL_TOKEN. Empty or
// whitespace-only counts as unset.
func agPollTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("GITHUB_POLL_TOKEN"))
}

// agStaticTokenClient builds the token-only client, or nil when no token is
// configured.
func agStaticTokenClient() *Client {
	tok := agPollTokenFromEnv()
	if tok == "" {
		return nil
	}
	return &Client{
		staticToken: tok,
		apiBase:     defaultAPIBase,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
		now:         time.Now,
		tokens:      map[int64]cachedToken{},
	}
}

// agStaticToken returns the static token for installationID when it applies: a
// client without an App key uses it for every installation (there is nothing
// else to mint with), and an App client uses it only for installation 0, the id
// the poller writes when GITHUB_POLL_ENABLED opts in alongside an App. Webhook
// deliveries never carry installation 0, so App behaviour is unchanged.
func (c *Client) agStaticToken(installationID int64) (string, bool) {
	if c.staticToken == "" {
		return "", false
	}
	if c.privateKey == nil || installationID == 0 {
		return c.staticToken, true
	}
	return "", false
}

// agEnabled reports whether the client has any credential to call GitHub with.
func (c *Client) agEnabled() bool {
	return c != nil && (c.privateKey != nil || c.staticToken != "")
}

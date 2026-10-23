package handler

// AG patch: urgent-only claim filter. See package agclaim.

import (
	"context"
	"net/http"

	"github.com/multica-ai/multica/server/internal/agclaim"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agClaimContext validates the batch claim's ag_min_issue_priority map and
// returns the request context carrying it for the claim path. An invalid map is
// a 400, never an unfiltered claim; an absent map returns the request context
// unchanged.
func agClaimContext(w http.ResponseWriter, r *http.Request, minPriority map[string]string) (context.Context, bool) {
	filter, err := agclaim.Parse(minPriority)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return agclaim.With(r.Context(), filter), true
}

// agAddCapabilitiesToHeartbeatResp advertises the server's AG capabilities in
// an HTTP heartbeat response; the WebSocket ack carries them in its payload.
func agAddCapabilitiesToHeartbeatResp(resp map[string]any, ack *protocol.DaemonHeartbeatAckPayload) {
	if len(ack.AGCapabilities) > 0 {
		resp["ag_capabilities"] = ack.AGCapabilities
	}
}

// Package agclaim is the server side of the urgent-only claim filter: a batch
// claim may name runtimes that only take tasks whose issue is at or above a
// priority. It is separate from the handler and service packages so the parsing
// and wire rules are testable without Postgres.
package agclaim

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// CapabilityClaimFilter is advertised on the heartbeat ack as ag_capabilities
// so a daemon can fail closed against a server that would ignore the filter.
const CapabilityClaimFilter = "claim_filter"

// Capabilities is what this server advertises as ag_capabilities.
func Capabilities() []string { return []string{CapabilityClaimFilter} }

// priorities lists issue priorities from most to least urgent. It mirrors the
// CHECK constraint on issue.priority.
var priorities = []string{"urgent", "high", "medium", "low", "none"}

// Filter maps a runtime ID (canonical lowercase UUID string) to the issue
// priorities its claims may take.
type Filter map[string][]string

// Parse turns the request's runtime ID -> minimum priority map into a Filter.
// An unknown priority or a malformed runtime ID is an error: the filter fails
// closed rather than silently leaving a runtime unfiltered. A nil or empty map
// yields a nil Filter, which changes nothing.
func Parse(minPriority map[string]string) (Filter, error) {
	if len(minPriority) == 0 {
		return nil, nil
	}
	f := make(Filter, len(minPriority))
	for rid, min := range minPriority {
		id, err := util.ParseUUID(rid)
		if err != nil {
			return nil, fmt.Errorf("ag_min_issue_priority: invalid runtime id %q", rid)
		}
		allowed := allowedFrom(min)
		if allowed == nil {
			return nil, fmt.Errorf("ag_min_issue_priority: unknown priority %q", min)
		}
		f[util.UUIDToString(id)] = allowed
	}
	return f, nil
}

// allowedFrom returns min and every priority above it, or nil for an unknown min.
func allowedFrom(min string) []string {
	for i, p := range priorities {
		if p == min {
			return append([]string(nil), priorities[:i+1]...)
		}
	}
	return nil
}

// Filtered reports whether the runtime is subject to the filter.
func (f Filter) Filtered(runtimeID pgtype.UUID) bool {
	_, ok := f[util.UUIDToString(runtimeID)]
	return ok
}

// JSON encodes the filter for the claim queries' ag_claim_filter parameter: an
// object of runtime ID -> allowed priorities. A nil or empty filter encodes as
// nil (SQL NULL), which the queries read as "no filter".
func (f Filter) JSON() []byte {
	if len(f) == 0 {
		return nil
	}
	b, err := json.Marshal(map[string][]string(f))
	if err != nil {
		// A map of string slices always marshals.
		panic(err)
	}
	return b
}

type ctxKey struct{}

// With returns ctx carrying f, so the claim path can apply it without a
// signature change. A nil or empty filter returns ctx unchanged.
func With(ctx context.Context, f Filter) context.Context {
	if len(f) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, f)
}

// From returns the filter carried by ctx, or nil.
func From(ctx context.Context) Filter {
	f, _ := ctx.Value(ctxKey{}).(Filter)
	return f
}

// JSONFor encodes only runtimeID's entry of the filter carried by ctx, for the
// single-runtime claim query. It returns nil when the runtime is unfiltered.
func JSONFor(ctx context.Context, runtimeID pgtype.UUID) []byte {
	f := From(ctx)
	key := util.UUIDToString(runtimeID)
	allowed, ok := f[key]
	if !ok {
		return nil
	}
	return Filter{key: allowed}.JSON()
}

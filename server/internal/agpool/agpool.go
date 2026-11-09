// Package agpool holds the rules of the AG runtime pool shared by the daemon,
// the handler and the claim path: the pool name a daemon declares, how it is
// stored in agent_runtime.metadata, and the per-poll set of pooled runtimes the
// claim path carries in its context. Membership itself (same pool, workspace,
// owner, provider and profile) is decided in SQL, in ag_runtime_pool.sql.
package agpool

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// MetadataKey is the agent_runtime.metadata key holding the pool name.
const MetadataKey = "ag_pool"

// EnvVar is the daemon environment variable that declares the pool.
const EnvVar = "MULTICA_AG_POOL"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Normalize trims name and reports whether it is a usable pool name. An empty
// name is valid and means "no pool".
func Normalize(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", true
	}
	return name, namePattern.MatchString(name)
}

// WithMetadata returns metadata with the pool name added. With no pool it
// returns metadata unchanged, byte for byte, so a daemon without a pool
// registers exactly as before.
func WithMetadata(metadata []byte, pool string) []byte {
	if pool == "" {
		return metadata
	}
	m := map[string]any{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &m); err != nil {
			return metadata
		}
	}
	m[MetadataKey] = pool
	out, err := json.Marshal(m)
	if err != nil {
		return metadata
	}
	return out
}

// InMetadata reports whether runtime metadata names a pool.
func InMetadata(metadata []byte) bool {
	var m struct {
		Pool string `json:"ag_pool"`
	}
	if len(metadata) == 0 || json.Unmarshal(metadata, &m) != nil {
		return false
	}
	return m.Pool != ""
}

// Set is the runtimes of one claim poll that are in a pool.
type Set map[string]struct{}

// NewSet builds a Set from runtime IDs.
func NewSet(ids []pgtype.UUID) Set {
	if len(ids) == 0 {
		return nil
	}
	s := make(Set, len(ids))
	for _, id := range ids {
		s[util.UUIDToString(id)] = struct{}{}
	}
	return s
}

// Has reports whether runtime id is pooled. A nil Set has nothing.
func (s Set) Has(id pgtype.UUID) bool {
	if s == nil || !id.Valid {
		return false
	}
	_, ok := s[util.UUIDToString(id)]
	return ok
}

type ctxKey struct{}

// WithSet returns ctx carrying s.
func WithSet(ctx context.Context, s Set) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From returns the Set in ctx, or nil.
func From(ctx context.Context) Set {
	s, _ := ctx.Value(ctxKey{}).(Set)
	return s
}

// PinnedKey is the agent.runtime_config key that keeps an agent out of the
// pool: only the agent's own runtime may claim its tasks, as upstream. Its
// runtime stays pooled for every other agent. For an agent that must run on one
// machine (a macOS sysadmin on the laptop, a Linux one on the server).
const PinnedKey = "ag_pool_pinned"

// Pinned reports whether runtimeConfig pins the agent to its runtime: PinnedKey
// holds JSON true. Anything else (absent, false, "true", malformed JSON) is
// unpinned. The SQL in ag_runtime_pool.sql tests the same thing with
// runtime_config->'ag_pool_pinned' = 'true'::jsonb.
func Pinned(runtimeConfig []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(runtimeConfig, &m); err != nil {
		return false
	}
	return string(bytes.TrimSpace(m[PinnedKey])) == "true"
}

// SetPinned sets or clears PinnedKey in a decoded runtime_config, keeping its
// other keys. A nil map (no runtime_config yet) is allocated. Unpinning
// removes the key rather than storing false.
func SetPinned(runtimeConfig map[string]any, pinned bool) map[string]any {
	if runtimeConfig == nil {
		runtimeConfig = map[string]any{}
	}
	if pinned {
		runtimeConfig[PinnedKey] = true
	} else {
		delete(runtimeConfig, PinnedKey)
	}
	return runtimeConfig
}

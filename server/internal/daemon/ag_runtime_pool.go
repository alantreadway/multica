package daemon

// AG patch: runtime pool. MULTICA_AG_POOL names the pool this daemon's runtimes
// join. It is sent as ag_pool on every register, and the server lets runtimes
// with the same pool, workspace, owner and provider claim each other's agents'
// queued tasks. Unset, the register request is exactly upstream's.

import (
	"fmt"

	"github.com/multica-ai/multica/server/internal/agpool"
)

// agPoolFromEnv reads MULTICA_AG_POOL. An invalid name fails daemon start, so
// a typo cannot silently leave the machine out of its pool.
func agPoolFromEnv(lookup func(string) (string, bool)) (string, error) {
	raw, _ := lookup(agpool.EnvVar)
	name, ok := agpool.Normalize(raw)
	if !ok {
		return "", fmt.Errorf("%s: invalid pool name %q (letters, digits, '.', '_' or '-', up to 64, starting with a letter or digit)", agpool.EnvVar, raw)
	}
	return name, nil
}

// agAddPoolToRegister adds ag_pool to a register request when a pool is set.
func agAddPoolToRegister(req map[string]any, pool string) {
	if pool != "" {
		req["ag_pool"] = pool
	}
}

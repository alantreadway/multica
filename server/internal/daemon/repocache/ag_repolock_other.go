//go:build windows

package repocache

import "os"

// Without flock the per-repo lock stays in-process only, as it was upstream.

const agRepoFlockSupported = false

func agRepoTryFlock(*os.File) (busy bool, err error) { return false, nil }

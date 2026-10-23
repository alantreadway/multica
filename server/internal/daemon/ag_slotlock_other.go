//go:build windows

package daemon

import "os"

// AG patch: machine-wide task slot cap (see ag_slotlock.go).
//
// Without flock the slot set stays in-process only, as it was upstream.

const agSlotFlockSupported = false

func agSlotTryFlock(*os.File) (busy bool, err error) { return false, nil }

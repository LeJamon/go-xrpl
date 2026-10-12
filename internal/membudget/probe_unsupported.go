//go:build !linux && !darwin

package membudget

import (
	"fmt"
	"runtime"
)

func probePlatform(ProbeOptions) (Headroom, error) {
	return Headroom{}, fmt.Errorf("automatic memory detection is unsupported on %s; configure resources.memory_mb", runtime.GOOS)
}

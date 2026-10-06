//go:build darwin

package membudget

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

func probePlatform(options ProbeOptions) (Headroom, error) {
	pageSize := uint64(unix.Getpagesize())
	if pageSize == 0 {
		return Headroom{}, fmt.Errorf("Darwin page size is unavailable")
	}
	var pages uint64
	for _, name := range []string{
		"vm.stats.vm.free_count",
		"vm.stats.vm.inactive_count",
		"vm.stats.vm.speculative_count",
	} {
		value, err := unix.SysctlUint64(name)
		if err != nil {
			return Headroom{}, fmt.Errorf("read Darwin %s: %w", name, err)
		}
		if pages > math.MaxUint64-value {
			return Headroom{}, fmt.Errorf("Darwin available page count overflows")
		}
		pages += value
	}
	if pages > math.MaxUint64/pageSize {
		return Headroom{}, fmt.Errorf("Darwin available memory overflows")
	}
	return applyRuntimeCap(options, pages*pageSize, "darwin:vm.stats.vm")
}

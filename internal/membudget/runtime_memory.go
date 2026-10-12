package membudget

import (
	"fmt"
	"runtime/debug"
	"runtime/metrics"
)

func runtimeMemoryLimit() (uint64, bool) {
	limit := debug.SetMemoryLimit(-1)
	if limit < 0 {
		return 0, false
	}
	value := uint64(limit)
	return value, finiteLimit(value)
}

func runtimeManagedBytes() (uint64, error) {
	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindUint64 || samples[1].Value.Kind() != metrics.KindUint64 {
		return 0, fmt.Errorf("runtime memory metrics are unavailable")
	}
	total := samples[0].Value.Uint64()
	released := samples[1].Value.Uint64()
	if released > total {
		return 0, fmt.Errorf("runtime released memory %d exceeds total %d", released, total)
	}
	return total - released, nil
}

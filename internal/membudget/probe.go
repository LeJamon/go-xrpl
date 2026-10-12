package membudget

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Headroom is the usable byte allowance observed by the platform probe.
// Source identifies the primary source and any applied caps for diagnostics.
type Headroom struct {
	Available uint64
	Source    string
}

// ProbeOptions makes Probe deterministic in tests and permits callers to
// provide an alternate proc/cgroup root. A nil ReadFile uses os.ReadFile.
// Runtime callbacks override the corresponding runtime values when supplied.
type ProbeOptions struct {
	Root string

	MemInfoPath    string
	SelfCgroupPath string
	MountInfoPath  string
	ReadFile       func(string) ([]byte, error)
	ReadVMStat     func() ([]byte, error)
	DisableCgroup  bool
	DisableRuntime bool

	RuntimeMemoryLimit  func() (limit uint64, finite bool)
	RuntimeManagedBytes func() (uint64, error)
}

// Probe returns startup available memory using the host's native source.
func Probe() (Headroom, error) {
	return probePlatform(ProbeOptions{})
}

// ProbeWithOptions runs the probe with injectable file and runtime sources.
func ProbeWithOptions(options ProbeOptions) (Headroom, error) {
	return probePlatform(options)
}

func (o ProbeOptions) readFile(path string) ([]byte, error) {
	if o.ReadFile != nil {
		return o.ReadFile(path)
	}
	if o.Root != "" && filepath.IsAbs(path) {
		path = filepath.Join(o.Root, filepath.Clean(path))
	}
	return os.ReadFile(path)
}

func (o ProbeOptions) path(defaultPath, override string) string {
	if override != "" {
		return override
	}
	return defaultPath
}

func applyRuntimeCap(options ProbeOptions, headroom uint64, source string) (Headroom, error) {
	if options.DisableRuntime {
		return Headroom{Available: headroom, Source: source}, nil
	}
	limit, finite := runtimeLimit(options)
	if !finite {
		return Headroom{Available: headroom, Source: source}, nil
	}
	managed, err := runtimeManaged(options)
	if err != nil {
		return Headroom{}, fmt.Errorf("read Go runtime managed memory: %w", err)
	}
	remaining := uint64(0)
	if managed < limit {
		remaining = limit - managed
	}
	if remaining < headroom {
		headroom = remaining
	}
	return Headroom{Available: headroom, Source: source + "+GOMEMLIMIT"}, nil
}

func runtimeLimit(options ProbeOptions) (uint64, bool) {
	if options.RuntimeMemoryLimit != nil {
		return options.RuntimeMemoryLimit()
	}
	return runtimeMemoryLimit()
}

func runtimeManaged(options ProbeOptions) (uint64, error) {
	if options.RuntimeManagedBytes != nil {
		return options.RuntimeManagedBytes()
	}
	return runtimeManagedBytes()
}

func finiteLimit(value uint64) bool {
	return value < math.MaxInt64
}

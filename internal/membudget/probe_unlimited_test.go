package membudget

import (
	"io/fs"
	"testing"
)

func TestProbeReadableUnlimitedCgroupsUseHostHeadroom(t *testing.T) {
	for _, v2 := range []bool{true, false} {
		t.Run(map[bool]string{true: "v2", false: "v1"}[v2], func(t *testing.T) {
			files := map[string]string{"/proc/meminfo": "MemAvailable: 8192 kB\n"}
			if v2 {
				files["/proc/self/cgroup"] = "0::/\n"
				files["/proc/self/mountinfo"] = "36 29 0:32 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n"
				files["/sys/fs/cgroup/memory.max"] = "max\n"
				files["/sys/fs/cgroup/memory.high"] = "max\n"
				files["/sys/fs/cgroup/memory.current"] = "1000000\n"
			} else {
				files["/proc/self/cgroup"] = "5:memory:/\n"
				files["/proc/self/mountinfo"] = "36 29 0:32 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n"
				files["/sys/fs/cgroup/memory/memory.limit_in_bytes"] = "9223372036854771712\n"
				files["/sys/fs/cgroup/memory/memory.soft_limit_in_bytes"] = "0\n"
				files["/sys/fs/cgroup/memory/memory.usage_in_bytes"] = "1000000\n"
			}
			result, err := probeLinux(ProbeOptions{DisableRuntime: true, ReadFile: func(path string) ([]byte, error) {
				value, ok := files[path]
				if !ok {
					return nil, fs.ErrNotExist
				}
				return []byte(value), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Available != 8<<20 {
				t.Fatalf("unlimited cgroup reduced host headroom: %+v", result)
			}
		})
	}
}

func TestMemAvailableRejectsMalformedUnitsAndOverflow(t *testing.T) {
	for _, input := range []string{"MemAvailable: 10", "MemAvailable: 10 MB", "MemAvailable: 18446744073709551615 kB", "MemAvailable: 10 kB unexpected"} {
		if _, err := parseMemAvailable([]byte(input)); err == nil {
			t.Errorf("accepted malformed available memory %q", input)
		}
	}
}

package membudget

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

func probeLinux(options ProbeOptions) (Headroom, error) {
	meminfoPath := options.path("/proc/meminfo", options.MemInfoPath)
	meminfo, err := options.readFile(meminfoPath)
	if err != nil {
		return Headroom{}, fmt.Errorf("read %s: %w", meminfoPath, err)
	}
	available, err := parseMemAvailable(meminfo)
	if err != nil {
		return Headroom{}, err
	}

	source := "linux:MemAvailable"
	if !options.DisableCgroup {
		cgroupAvailable, applied, err := cgroupHeadroom(options)
		if err != nil {
			return Headroom{}, err
		}
		if applied {
			if cgroupAvailable < available {
				available = cgroupAvailable
			}
			source += "+cgroup"
		}
	}
	return applyRuntimeCap(options, available, source)
}

func parseMemAvailable(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimSuffix(fields[0], ":") != "MemAvailable" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("MemAvailable must contain a count in kB")
		}
		value, err := parseUint(fields[1], "MemAvailable")
		if err != nil {
			return 0, err
		}
		if value > math.MaxUint64/1024 {
			return 0, fmt.Errorf("MemAvailable overflows bytes: %s", fields[1])
		}
		return value * 1024, nil
	}
	return 0, errors.New("MemAvailable is missing from /proc/meminfo")
}

type cgroupMembership struct {
	v2          bool
	controllers map[string]bool
	path        string
}

type cgroupMount struct {
	v2          bool
	root        string
	mountPoint  string
	controllers map[string]bool
}

func cgroupHeadroom(options ProbeOptions) (uint64, bool, error) {
	membershipPath := options.path("/proc/self/cgroup", options.SelfCgroupPath)
	membershipData, err := options.readFile(membershipPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %s: %w", membershipPath, err)
	}
	memberships, err := parseCgroupMembership(membershipData)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", membershipPath, err)
	}
	if len(memberships) == 0 {
		return 0, false, nil
	}

	mountInfoPath := options.path("/proc/self/mountinfo", options.MountInfoPath)
	mountInfo, err := options.readFile(mountInfoPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, fmt.Errorf("cgroup membership exists but %s is missing", mountInfoPath)
		}
		return 0, false, fmt.Errorf("read %s: %w", mountInfoPath, err)
	}
	mounts, err := parseCgroupMounts(mountInfo)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", mountInfoPath, err)
	}

	var minimum uint64
	applied := false
	for _, membership := range memberships {
		matched := false
		matchedPath := false
		for _, mount := range mounts {
			if !membershipMatchesMount(membership, mount) {
				continue
			}
			matched = true
			directory, ok := cgroupDirectory(mount, membership.path)
			if !ok {
				continue
			}
			matchedPath = true
			value, found, err := cgroupMountHeadroom(options, mount, directory)
			if err != nil {
				return 0, false, err
			}
			if !found {
				return 0, false, fmt.Errorf("cgroup membership %s has no readable memory limits", membership.path)
			}
			if !applied || value < minimum {
				minimum = value
			}
			applied = true
		}
		if (!matched || !matchedPath) && (membership.v2 || membership.controllers["memory"]) {
			return 0, false, fmt.Errorf("cgroup membership %s has no matching memory mount", membership.path)
		}
	}
	return minimum, applied, nil
}

func parseCgroupMembership(data []byte) ([]cgroupMembership, error) {
	var memberships []cgroupMembership
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" || !strings.HasPrefix(parts[2], "/") {
			return nil, fmt.Errorf("line %d has invalid cgroup membership", lineNumber+1)
		}
		membership := cgroupMembership{path: unescapeProcField(parts[2])}
		if parts[0] == "0" {
			if parts[1] != "" {
				return nil, fmt.Errorf("line %d has invalid v2 controllers", lineNumber+1)
			}
			membership.v2 = true
		} else {
			membership.controllers = splitControllers(parts[1])
		}
		memberships = append(memberships, membership)
	}
	return memberships, nil
}

func parseCgroupMounts(data []byte) ([]cgroupMount, error) {
	var mounts []cgroupMount
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) < separator+4 {
			return nil, fmt.Errorf("line %d has invalid mountinfo", lineNumber+1)
		}
		filesystem := fields[separator+1]
		if filesystem != "cgroup" && filesystem != "cgroup2" {
			continue
		}
		mount := cgroupMount{
			v2:         filesystem == "cgroup2",
			root:       unescapeProcField(fields[3]),
			mountPoint: unescapeProcField(fields[4]),
		}
		if mount.root == "" || mount.mountPoint == "" || !filepath.IsAbs(mount.root) || !filepath.IsAbs(mount.mountPoint) {
			return nil, fmt.Errorf("line %d has invalid cgroup mount paths", lineNumber+1)
		}
		if !mount.v2 {
			mount.controllers = splitControllers(fields[5])
			for option := range splitControllers(fields[separator+3]) {
				mount.controllers[option] = true
			}
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

func membershipMatchesMount(membership cgroupMembership, mount cgroupMount) bool {
	if membership.v2 {
		return mount.v2
	}
	if mount.v2 || !membership.controllers["memory"] {
		return false
	}
	return mount.controllers["memory"] || filepath.Base(mount.mountPoint) == "memory"
}

func cgroupDirectory(mount cgroupMount, membershipPath string) (string, bool) {
	root := filepath.Clean(mount.root)
	path := filepath.Clean(membershipPath)
	if root != "/" {
		if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
			return "", false
		}
		path = strings.TrimPrefix(path, root)
	}
	path = strings.TrimPrefix(path, string(filepath.Separator))
	return filepath.Join(mount.mountPoint, path), true
}

func cgroupMountHeadroom(options ProbeOptions, mount cgroupMount, directory string) (uint64, bool, error) {
	minimum := uint64(0)
	found := false
	for current := directory; ; current = filepath.Dir(current) {
		value, present, err := cgroupLevelHeadroom(options, mount.v2, current)
		if err != nil {
			return 0, false, err
		}
		if present && (!found || value < minimum) {
			minimum = value
			found = true
		}
		if current == mount.mountPoint {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !strings.HasPrefix(parent, mount.mountPoint) {
			break
		}
	}
	return minimum, found, nil
}

func cgroupLevelHeadroom(options ProbeOptions, v2 bool, directory string) (uint64, bool, error) {
	if v2 {
		return cgroupLevelV2(options, directory)
	}
	return cgroupLevelV1(options, directory)
}

func cgroupLevelV2(options ProbeOptions, directory string) (uint64, bool, error) {
	max, maxPresent, err := readCgroupLimit(options, filepath.Join(directory, "memory.max"), false)
	if err != nil {
		return 0, false, err
	}
	high, highPresent, err := readCgroupLimit(options, filepath.Join(directory, "memory.high"), false)
	if err != nil {
		return 0, false, err
	}
	current, currentPresent, err := readCgroupCurrent(options, filepath.Join(directory, "memory.current"))
	if err != nil {
		return 0, false, err
	}
	if !maxPresent && !highPresent && !currentPresent {
		return 0, false, nil
	}
	if !currentPresent {
		return 0, false, fmt.Errorf("cgroup %s is missing memory.current", directory)
	}
	remaining := uint64(math.MaxUint64)
	if maxPresent && max < remaining {
		remaining = max
	}
	if highPresent && high < remaining {
		remaining = high
	}
	if remaining == math.MaxUint64 {
		return remaining, true, nil
	}
	if current >= remaining {
		return 0, true, nil
	}
	return remaining - current, true, nil
}

func cgroupLevelV1(options ProbeOptions, directory string) (uint64, bool, error) {
	limit, limitPresent, err := readCgroupLimit(options, filepath.Join(directory, "memory.limit_in_bytes"), true)
	if err != nil {
		return 0, false, err
	}
	soft, softPresent, err := readCgroupLimit(options, filepath.Join(directory, "memory.soft_limit_in_bytes"), true)
	if err != nil {
		return 0, false, err
	}
	current, currentPresent, err := readCgroupCurrent(options, filepath.Join(directory, "memory.usage_in_bytes"))
	if err != nil {
		return 0, false, err
	}
	if !limitPresent && !softPresent && !currentPresent {
		return 0, false, nil
	}
	if !currentPresent {
		return 0, false, fmt.Errorf("cgroup %s is missing memory.usage_in_bytes", directory)
	}
	remaining := uint64(math.MaxUint64)
	if limitPresent && limit < remaining {
		remaining = limit
	}
	if softPresent && soft < remaining {
		remaining = soft
	}
	if remaining == math.MaxUint64 {
		return remaining, true, nil
	}
	if current >= remaining {
		return 0, true, nil
	}
	return remaining - current, true, nil
}

func readCgroupLimit(options ProbeOptions, path string, legacy bool) (uint64, bool, error) {
	data, err := options.readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return 0, false, nil
	}
	parsed, err := parseUint(value, path)
	if err != nil {
		return 0, false, err
	}
	if legacy && parsed == 0 {
		return 0, false, nil
	}
	if parsed >= uint64(math.MaxInt64)-1<<20 {
		return 0, false, nil
	}
	return parsed, true, nil
}

func readCgroupCurrent(options ProbeOptions, path string) (uint64, bool, error) {
	data, err := options.readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	value, err := parseUint(strings.TrimSpace(string(data)), path)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func parseUint(value, name string) (uint64, error) {
	if strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("%s has a negative value %q", name, value)
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s has malformed value %q: %w", name, value, err)
	}
	return parsed, nil
}

func splitControllers(value string) map[string]bool {
	controllers := make(map[string]bool)
	for _, controller := range strings.Split(value, ",") {
		controller = strings.TrimSpace(controller)
		if controller != "" {
			controllers[controller] = true
		}
	}
	return controllers
}

func unescapeProcField(value string) string {
	value = strings.ReplaceAll(value, `\040`, " ")
	value = strings.ReplaceAll(value, `\011`, "\t")
	return strings.ReplaceAll(value, `\134`, `\`)
}

//go:build darwin

package membudget

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func probePlatform(options ProbeOptions) (Headroom, error) {
	data, err := readVMStat(options)
	if err != nil {
		return Headroom{}, err
	}
	available, err := parseVMStatAvailable(data)
	if err != nil {
		return Headroom{}, err
	}
	return applyRuntimeCap(options, available, "darwin:vm_stat")
}

func readVMStat(options ProbeOptions) ([]byte, error) {
	if options.ReadVMStat != nil {
		return options.ReadVMStat()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return nil, fmt.Errorf("run /usr/bin/vm_stat: %w", err)
	}
	return data, nil
}

func parseVMStatAvailable(data []byte) (uint64, error) {
	pageSize := uint64(0)
	pages := uint64(0)
	found := uint8(0)
	for lineNumber, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
			open := strings.LastIndexByte(line, '(')
			close := strings.LastIndex(line, " bytes)")
			if open < 0 || close <= open {
				return 0, fmt.Errorf("vm_stat line %d has malformed page size", lineNumber+1)
			}
			pageFields := strings.Fields(line[open+1 : close])
			if len(pageFields) == 0 {
				return 0, fmt.Errorf("vm_stat line %d has malformed page size", lineNumber+1)
			}
			value, err := strconv.ParseUint(pageFields[len(pageFields)-1], 10, 64)
			if err != nil || value == 0 {
				return 0, fmt.Errorf("vm_stat line %d has malformed page size", lineNumber+1)
			}
			pageSize = value
			continue
		}
		name, valueText, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		bit := uint8(0)
		switch name {
		case "Pages free":
			bit = 1
		case "Pages inactive":
			bit = 2
		case "Pages speculative":
			bit = 4
		default:
			continue
		}
		if found&bit != 0 {
			return 0, fmt.Errorf("vm_stat line %d repeats %s", lineNumber+1, name)
		}
		valueText = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(valueText), "."))
		value, err := strconv.ParseUint(valueText, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("vm_stat line %d has malformed %s count", lineNumber+1, name)
		}
		if pages > math.MaxUint64-value {
			return 0, fmt.Errorf("vm_stat available page count overflows")
		}
		pages += value
		found |= bit
	}
	if pageSize == 0 {
		return 0, fmt.Errorf("vm_stat did not report page size")
	}
	if found != 7 {
		return 0, fmt.Errorf("vm_stat did not report free, inactive, and speculative pages")
	}
	if pages > math.MaxUint64/pageSize {
		return 0, fmt.Errorf("vm_stat available memory overflows")
	}
	return pages * pageSize, nil
}

package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// portSectionPrefix marks a top-level table as a port section even when
// server.ports does not list it.
const portSectionPrefix = "port_"

// UnknownKeysError reports keys in a configuration file that no setting
// consumes. Loading fails on them so a mistyped key cannot silently fall
// back to its default.
type UnknownKeysError struct {
	// File is the path of the configuration file holding the keys.
	File string
	// Keys lists the unknown keys as sorted, lower-case, dotted paths.
	Keys []string
}

// Error implements the error interface.
func (e *UnknownKeysError) Error() string {
	noun := "key"
	if len(e.Keys) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf("unknown configuration %s in %s: %s", noun, e.File, strings.Join(e.Keys, ", "))
}

func newUnknownKeysError(file string, keys []string) *UnknownKeysError {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return &UnknownKeysError{File: file, Keys: sorted}
}

// unmarshalTrackingUnused decodes v into target and returns the dotted paths
// of the keys the target does not define.
func unmarshalTrackingUnused(v *viper.Viper, target any, opts ...viper.DecoderConfigOption) ([]string, error) {
	var metadata mapstructure.Metadata
	opts = append(opts, func(dc *mapstructure.DecoderConfig) { dc.Metadata = &metadata })
	if err := v.Unmarshal(target, opts...); err != nil {
		return nil, err
	}
	return metadata.Unused, nil
}

// dropPortSections removes the top-level port tables from the unused keys of
// the main configuration. Port tables are decoded separately: the ones named
// by server.ports under any name, and by convention every port_* table.
func dropPortSections(unused, serverPorts []string) []string {
	listed := make(map[string]struct{}, len(serverPorts))
	for _, name := range serverPorts {
		listed[strings.ToLower(name)] = struct{}{}
	}
	var kept []string
	for _, key := range unused {
		if _, ok := listed[key]; ok || strings.HasPrefix(key, portSectionPrefix) {
			continue
		}
		kept = append(kept, key)
	}
	return kept
}

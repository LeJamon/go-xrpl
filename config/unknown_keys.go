package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"
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
	for i, key := range keys {
		keys[i] = strings.ToLower(key)
	}
	slices.Sort(keys)
	return &UnknownKeysError{File: file, Keys: slices.Compact(keys)}
}

func decodeSettings(settings map[string]any, target any) (mapstructure.Metadata, error) {
	var metadata mapstructure.Metadata
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: target, Metadata: &metadata, WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			configDecodeHook(),
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return metadata, err
	}
	if err := decoder.Decode(settings); err != nil {
		return metadata, err
	}
	return metadata, nil
}

// Match Viper's longest-key lookup for both ["admin.rpc"] and [admin.rpc].
func removePortSection(extra map[string]any, name string) bool {
	for prefix := name; ; {
		if value, ok := extra[prefix]; ok {
			if prefix == name {
				delete(extra, prefix)
				return true
			}
			table, ok := value.(map[string]any)
			if ok && removePortSection(table, name[len(prefix)+1:]) {
				if len(table) == 0 {
					delete(extra, prefix)
				}
				return true
			}
		}
		cut := strings.LastIndexByte(prefix, '.')
		if cut < 0 {
			return false
		}
		prefix = prefix[:cut]
	}
}

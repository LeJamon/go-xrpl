package replaytool

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/LeJamon/go-xrpl/shamap"
)

type stateDiffCounts struct {
	Added    int
	Modified int
	Removed  int
}

func writeStateArtifact(ctx context.Context, path string, stateMap *shamap.SHAMap) (int, error) {
	count := 0
	err := writeAtomicArtifact(path, 0o644, func(w io.Writer) error {
		if err := writeAll(w, []byte("[")); err != nil {
			return err
		}
		first := true
		var callbackErr error
		walkErr := stateMap.ForEachCtxReleasing(ctx, func(item *shamap.Item) bool {
			key := item.Key()
			entry := map[string]any{
				"index":    hex.EncodeToString(key[:]),
				"data_hex": hex.EncodeToString(item.Data()),
			}
			if decoded := decodeEntryBytes(item.Data()); decoded != nil {
				entry["decoded"] = decoded
			}
			callbackErr = writeStreamValue(w, entry, &first)
			if callbackErr == nil {
				count++
			}
			return callbackErr == nil
		})
		if walkErr != nil {
			return walkErr
		}
		if callbackErr != nil {
			return callbackErr
		}
		return writeAll(w, []byte("]\n"))
	})
	return count, err
}

func writeStateDiffArtifact(ctx context.Context, path string, pre, post *shamap.SHAMap) (stateDiffCounts, error) {
	var counts stateDiffCounts
	err := writeAtomicArtifact(path, 0o644, func(w io.Writer) error {
		if err := writeAll(w, []byte("{\"added\":[")); err != nil {
			return err
		}
		added, err := streamStateItems(ctx, w, post, func(item *shamap.Item) (any, bool, error) {
			key := item.Key()
			_, found, err := pre.Get(key)
			if err != nil {
				return nil, false, fmt.Errorf("reading pre-state %x: %w", key, err)
			}
			if found {
				return nil, false, nil
			}
			entry := map[string]any{
				"index":    hex.EncodeToString(key[:]),
				"data_hex": hex.EncodeToString(item.Data()),
			}
			if decoded := decodeEntryBytes(item.Data()); decoded != nil {
				entry["decoded"] = decoded
			}
			return entry, true, nil
		})
		if err != nil {
			return err
		}
		counts.Added = added

		if err := writeAll(w, []byte("],\"modified\":[")); err != nil {
			return err
		}
		modified, err := streamStateItems(ctx, w, pre, func(item *shamap.Item) (any, bool, error) {
			key := item.Key()
			postItem, found, err := post.Get(key)
			if err != nil {
				return nil, false, fmt.Errorf("reading post-state %x: %w", key, err)
			}
			if !found || bytes.Equal(item.Data(), postItem.Data()) {
				return nil, false, nil
			}
			entry := map[string]any{
				"index":         hex.EncodeToString(key[:]),
				"pre_data_hex":  hex.EncodeToString(item.Data()),
				"post_data_hex": hex.EncodeToString(postItem.Data()),
			}
			if decoded := decodeEntryBytes(item.Data()); decoded != nil {
				entry["pre_decoded"] = decoded
			}
			if decoded := decodeEntryBytes(postItem.Data()); decoded != nil {
				entry["post_decoded"] = decoded
			}
			return entry, true, nil
		})
		if err != nil {
			return err
		}
		counts.Modified = modified

		if err := writeAll(w, []byte("],\"removed\":[")); err != nil {
			return err
		}
		removed, err := streamStateItems(ctx, w, pre, func(item *shamap.Item) (any, bool, error) {
			key := item.Key()
			_, found, err := post.Get(key)
			if err != nil {
				return nil, false, fmt.Errorf("reading post-state %x: %w", key, err)
			}
			if found {
				return nil, false, nil
			}
			return hex.EncodeToString(key[:]), true, nil
		})
		if err != nil {
			return err
		}
		counts.Removed = removed
		return writeAll(w, []byte("]}\n"))
	})
	return counts, err
}

func streamStateItems(ctx context.Context, w io.Writer, stateMap *shamap.SHAMap, value func(*shamap.Item) (any, bool, error)) (int, error) {
	count := 0
	first := true
	var callbackErr error
	walkErr := stateMap.ForEachCtxReleasing(ctx, func(item *shamap.Item) bool {
		var output any
		var include bool
		output, include, callbackErr = value(item)
		if callbackErr != nil || !include {
			return callbackErr == nil
		}
		callbackErr = writeStreamValue(w, output, &first)
		if callbackErr == nil {
			count++
		}
		return callbackErr == nil
	})
	if walkErr != nil {
		return 0, walkErr
	}
	return count, callbackErr
}

func writeStreamValue(w io.Writer, value any, first *bool) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !*first {
		if err := writeAll(w, []byte(",")); err != nil {
			return err
		}
	}
	*first = false
	return writeAll(w, encoded)
}

func writeAll(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

// writeJSONFile marshals v as indented JSON to path, returning any error so a
// failed debug-dump write is surfaced rather than silently dropped.
func writeJSONFile(path string, v any) error {
	return writeAtomicJSON(path, v)
}

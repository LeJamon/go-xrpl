package nodestore

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// FormatVersion is the on-disk node record format written by this build:
// a 32-byte node hash keying [type:1][ledger sequence:4][payload]. It must be
// raised whenever that encoding changes incompatibly.
//
// The version is persisted once per store in a marker record rather than in
// every value, so stores written before the marker existed stay readable: a
// store without a marker is by definition version 1 and is stamped on its
// first writable open. A store whose marker names any other version is refused
// with ErrUnsupportedFormat instead of being decoded as garbage.
//
// The marker is independent of the Pebble generation manifest. The manifest
// versions how a rotating store's generation directories are laid out and
// owned; the marker versions the records inside them, for every backend. It
// lives in the key space so it follows the data: a plain database adopted as
// the first generation of a rotating store keeps its marker, and each rotation
// rewrites it into the generation about to become the archive.
const FormatVersion uint32 = 1

// ErrUnsupportedFormat indicates that a store was written in a node record
// format this build cannot read.
var ErrUnsupportedFormat = errors.New("unsupported nodestore format version")

// formatMarkerKey is not 32 bytes long, so it cannot collide with a node hash.
var formatMarkerKey = []byte("goxrpl:nodestore:format")

// checkFormat verifies the store's format marker, stamping an unmarked store
// unless it is read-only. The stamp needs no explicit sync: it precedes every
// later write in the backend's log, and losing it leaves an unmarked store
// that is stamped again on the next open.
func checkFormat(store kvstore.KeyValueStore) error {
	value, err := store.Get(formatMarkerKey)
	if errors.Is(err, kvstore.ErrNotFound) {
		if readOnly, ok := store.(kvstore.ReadOnlyStore); ok && readOnly.ReadOnly() {
			return nil
		}
		return writeFormatMarker(store)
	}
	if err != nil {
		return fmt.Errorf("read nodestore format marker: %w", err)
	}
	if len(value) != 4 {
		return fmt.Errorf("%w: nodestore format marker has length %d", ErrDataCorrupt, len(value))
	}
	if version := binary.BigEndian.Uint32(value); version != FormatVersion {
		return fmt.Errorf(
			"%w: store is version %d, this build reads version %d; open it with a build that supports version %d",
			ErrUnsupportedFormat, version, FormatVersion, version,
		)
	}
	return nil
}

func writeFormatMarker(store kvstore.KeyValueStore) error {
	if err := store.Put(formatMarkerKey, binary.BigEndian.AppendUint32(nil, FormatVersion)); err != nil {
		return fmt.Errorf("write nodestore format marker: %w", err)
	}
	return nil
}

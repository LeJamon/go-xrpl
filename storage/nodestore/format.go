package nodestore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// FormatVersion is the on-disk node record format written by this build:
// a 32-byte node hash keying [type:1][ledger sequence:4][payload]. It must be
// raised whenever that encoding changes incompatibly.
//
// The marker is independent of the Pebble generation manifest. The manifest
// versions how a rotating store's generation directories are laid out and
// owned. The marker versions the records inside them.
const FormatVersion uint32 = 1

// ErrUnsupportedFormat indicates that a store was written in a node record
// format this build cannot read.
var ErrUnsupportedFormat = errors.New("unsupported nodestore format version")

// formatMarkerKey is not 32 bytes long, so it cannot collide with a node hash.
var formatMarkerKey = []byte("goxrpl:nodestore:format")

// VerifyFormat checks a generation before it is adopted into a rotating store.
// An unmarked generation is readable as version 1.
func VerifyFormat(store kvstore.KeyValueStore) error {
	return checkFormat(store, false)
}

func checkFormat(store kvstore.KeyValueStore, stampMissing bool) error {
	var value []byte
	var err error
	if reader, ok := store.(kvstore.BoundedReadingStore); ok {
		value, err = reader.GetBounded(context.Background(), formatMarkerKey, 4)
		if errors.Is(err, kvstore.ErrValueTooLarge) {
			return fmt.Errorf("%w: nodestore format marker exceeds 4 bytes", ErrDataCorrupt)
		}
	} else {
		value, err = store.Get(formatMarkerKey)
	}
	if errors.Is(err, kvstore.ErrNotFound) {
		if !stampMissing {
			return nil
		}
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

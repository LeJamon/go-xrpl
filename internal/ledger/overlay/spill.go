package overlay

import (
	"context"
	"fmt"
)

func (o *Overlay) spillRootLocked(ctx context.Context) error {
	if o.root == nil {
		return nil
	}
	if o.cfg.MaxDiskBytes == 0 || o.cfg.SpillDir == "" {
		return ErrSpillDisabled
	}
	segment, err := writeTreeSegment(ctx, o.budget, o.cfg.SpillDir, o.root)
	if err != nil {
		return err
	}
	if err := o.addSegmentLocked(ctx, segment); err != nil {
		return err
	}
	root := o.root
	o.root = nil
	releaseTreeNode(root)
	return nil
}

// addSegmentLocked consumes segment's one reference. On success it becomes an
// overlay-owned run. On failure it releases that reference and leaves the
// prior overlay segments unchanged.
func (o *Overlay) addSegmentLocked(ctx context.Context, segment *spillSegment) error {
	if segment == nil {
		return nil
	}
	if len(o.segments)+1 <= o.cfg.MaxSegments {
		o.segments = append([]*spillSegment{segment}, o.segments...)
		return nil
	}

	// Temporary references let compaction proceed without changing the live
	// overlay until all bounded fan-in stages have succeeded.
	working := make([]*spillSegment, 0, len(o.segments)+1)
	working = append(working, segment)
	for _, existing := range o.segments {
		working = append(working, retainSegment(existing))
	}
	for len(working) > o.cfg.MaxSegments {
		fanIn := o.cfg.MaxMergeFanIn
		if fanIn > len(working) {
			fanIn = len(working)
		}
		merged, err := compactSegments(ctx, o.budget, o.cfg.SpillDir, working[:fanIn])
		if err != nil {
			for _, held := range working {
				releaseSegment(held)
			}
			return err
		}
		for _, held := range working[:fanIn] {
			releaseSegment(held)
		}
		working = append([]*spillSegment{merged}, working[fanIn:]...)
	}
	for _, existing := range o.segments {
		releaseSegment(existing)
	}
	o.segments = working
	return nil
}

func compactSegments(ctx context.Context, owner *budget, dir string, inputs []*spillSegment) (*spillSegment, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: empty compaction", ErrCorruptSegment)
	}
	reserved := segmentHeaderSize
	count := uint64(0)
	for _, input := range inputs {
		if input == nil || input.size < segmentHeaderSize || input.size-segmentHeaderSize > ^uint64(0)-reserved {
			return nil, fmt.Errorf("%w: invalid compaction input", ErrCorruptSegment)
		}
		reserved += input.size - segmentHeaderSize
		if input.count > ^uint64(0)-count {
			return nil, fmt.Errorf("%w: record count overflow", ErrCorruptSegment)
		}
		count += input.count
	}
	f, path, err := createSegmentFile(ctx, owner, dir, reserved, false)
	if err != nil {
		return nil, err
	}
	cursors := make([]changeCursor, 0, len(inputs))
	for _, input := range inputs {
		cursor, openErr := input.cursor(ctx, owner, nil)
		if openErr != nil {
			for _, held := range cursors {
				_ = held.close()
			}
			discardSegmentFile(f, path, owner, reserved)
			return nil, openErr
		}
		cursors = append(cursors, cursor)
	}
	var index [sparseIndexMax]sparseIndexEntry
	var bloom segmentBloom
	indexCount := 0
	stride := count / sparseIndexMax
	if stride == 0 {
		stride = 1
	}
	position := uint64(0)
	offset := int64(segmentHeaderSize)
	var writeErr error
	mergeErr := mergeCursors(ctx, cursors, func(key [32]byte, value []byte, deleted bool) bool {
		writeErr = writeRecord(f, key, value, deleted)
		if writeErr == nil {
			bloom.add(key)
			if position%stride == 0 && indexCount < sparseIndexMax {
				index[indexCount] = sparseIndexEntry{key: key, offset: offset}
				indexCount++
			}
			recordBytes, valid := recordSize(value)
			if !valid || recordBytes > (^uint64(0)>>1)-uint64(offset) {
				writeErr = ErrDiskBudget
			} else {
				offset += int64(recordBytes)
				position++
			}
		}
		return writeErr == nil
	})
	if mergeErr == nil && writeErr != nil {
		mergeErr = fmt.Errorf("write overlay compacted segment: %w", writeErr)
	}
	closeErr := closeCursors(cursors, 0, owner)
	if mergeErr != nil {
		discardSegmentFile(f, path, owner, reserved)
		return nil, mergeErr
	}
	if closeErr != nil {
		discardSegmentFile(f, path, owner, reserved)
		return nil, closeErr
	}
	return finishSegmentFile(ctx, f, path, owner, reserved, position, &index, indexCount, bloom)
}

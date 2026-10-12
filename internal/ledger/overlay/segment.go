package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sync/atomic"
)

var segmentMagic = [8]byte{'G', 'X', 'O', 'V', 1, 0, 0, 0}

const (
	segmentHeaderSize = uint64(len(segmentMagic))
	recordHeaderSize  = uint64(32 + 1 + 8)
	sparseIndexMax    = 8
)

type sparseIndexEntry struct {
	key    [32]byte
	offset int64
}

type spillSegment struct {
	refs atomic.Int64

	path       string
	size       uint64
	count      uint64
	index      [sparseIndexMax]sparseIndexEntry
	indexCount uint8
	owner      *budget
}

func retainSegment(s *spillSegment) *spillSegment {
	if s != nil {
		s.refs.Add(1)
	}
	return s
}

func releaseSegment(s *spillSegment) {
	if s == nil || s.refs.Add(-1) != 0 {
		return
	}
	_ = os.Remove(s.path)
	if s.owner != nil {
		s.owner.releaseDisk(s.size)
		s.owner.releaseMemory(segmentOverhead)
	}
}

func recordSize(value []byte) (uint64, bool) {
	if uint64(len(value)) > math.MaxUint64-recordHeaderSize {
		return 0, false
	}
	return recordHeaderSize + uint64(len(value)), true
}

func addRecordSize(total uint64, value []byte) (uint64, bool) {
	size, ok := recordSize(value)
	if !ok || size > math.MaxUint64-total {
		return 0, false
	}
	return total + size, true
}

func reserveSegment(owner *budget, size uint64) error {
	if !owner.reserveMemory(segmentOverhead) {
		return ErrMemoryBudget
	}
	if !owner.reserveDisk(size) {
		owner.releaseMemory(segmentOverhead)
		return ErrDiskBudget
	}
	return nil
}

func createSegmentFile(ctx context.Context, owner *budget, dir string, size uint64) (*os.File, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := reserveSegment(owner, size); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		owner.releaseDisk(size)
		owner.releaseMemory(segmentOverhead)
		return nil, "", fmt.Errorf("create overlay spill directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".goxrpl-overlay-*.run")
	if err != nil {
		owner.releaseDisk(size)
		owner.releaseMemory(segmentOverhead)
		return nil, "", fmt.Errorf("create overlay spill segment: %w", err)
	}
	cleanup := func(cause error) (*os.File, string, error) {
		_ = f.Close()
		_ = os.Remove(f.Name())
		owner.releaseDisk(size)
		owner.releaseMemory(segmentOverhead)
		return nil, "", cause
	}
	if _, err := f.Write(segmentMagic[:]); err != nil {
		return cleanup(fmt.Errorf("write overlay spill header: %w", err))
	}
	return f, f.Name(), nil
}

func finishSegmentFile(ctx context.Context, f *os.File, path string, owner *budget, reserved, count uint64, index *[sparseIndexMax]sparseIndexEntry, indexCount int) (*spillSegment, error) {
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		owner.releaseDisk(reserved)
		owner.releaseMemory(segmentOverhead)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		owner.releaseDisk(reserved)
		owner.releaseMemory(segmentOverhead)
		return nil, fmt.Errorf("sync overlay spill segment: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		owner.releaseDisk(reserved)
		owner.releaseMemory(segmentOverhead)
		return nil, fmt.Errorf("close overlay spill segment: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		owner.releaseDisk(reserved)
		owner.releaseMemory(segmentOverhead)
		return nil, fmt.Errorf("stat overlay spill segment: %w", err)
	}
	actual := uint64(info.Size())
	if actual > reserved {
		_ = os.Remove(path)
		owner.releaseDisk(reserved)
		owner.releaseMemory(segmentOverhead)
		return nil, fmt.Errorf("%w: segment grew beyond admission", ErrDiskBudget)
	}
	if actual < reserved {
		owner.releaseDisk(reserved - actual)
	}
	s := &spillSegment{path: path, size: actual, count: count, owner: owner}
	if index != nil {
		if indexCount > sparseIndexMax {
			indexCount = sparseIndexMax
		}
		copy(s.index[:], index[:indexCount])
		s.indexCount = uint8(indexCount)
	}
	s.refs.Store(1)
	return s, nil
}

func writeRecord(w io.Writer, key [32]byte, value []byte, deleted bool) error {
	if _, err := w.Write(key[:]); err != nil {
		return err
	}
	flag := byte(0)
	if deleted {
		flag = 1
	}
	if _, err := w.Write([]byte{flag}); err != nil {
		return err
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	if _, err := w.Write(length[:]); err != nil {
		return err
	}
	if len(value) != 0 {
		_, err := w.Write(value)
		return err
	}
	return nil
}

func estimateTreeSegment(ctx context.Context, root *treeNode) (uint64, uint64, error) {
	total := segmentHeaderSize
	count := uint64(0)
	cursor := newTreeCursor(root, nil)
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		_, value, _, ok := cursor.next()
		if !ok {
			return total, count, nil
		}
		var valid bool
		total, valid = addRecordSize(total, value)
		if !valid {
			return 0, 0, ErrDiskBudget
		}
		count++
	}
}

func writeTreeSegment(ctx context.Context, owner *budget, dir string, root *treeNode) (*spillSegment, error) {
	if root == nil {
		return nil, nil
	}
	size, count, err := estimateTreeSegment(ctx, root)
	if err != nil {
		return nil, err
	}
	f, path, err := createSegmentFile(ctx, owner, dir, size)
	if err != nil {
		return nil, err
	}
	cursor := newTreeCursor(root, nil)
	var previous *[32]byte
	var index [sparseIndexMax]sparseIndexEntry
	indexCount := 0
	stride := count / sparseIndexMax
	if stride == 0 {
		stride = 1
	}
	position := uint64(0)
	offset := int64(segmentHeaderSize)
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			owner.releaseDisk(size)
			owner.releaseMemory(segmentOverhead)
			return nil, err
		}
		key, value, deleted, ok := cursor.next()
		if !ok {
			break
		}
		if previous != nil && bytes.Compare(key[:], previous[:]) <= 0 {
			_ = f.Close()
			_ = os.Remove(path)
			owner.releaseDisk(size)
			owner.releaseMemory(segmentOverhead)
			return nil, ErrOrdering
		}
		previous = &key
		if position%stride == 0 && indexCount < sparseIndexMax {
			index[indexCount] = sparseIndexEntry{key: key, offset: offset}
			indexCount++
		}
		position++
		recordBytes, valid := recordSize(value)
		if !valid || recordBytes > uint64(math.MaxInt64-offset) {
			_ = f.Close()
			_ = os.Remove(path)
			owner.releaseDisk(size)
			owner.releaseMemory(segmentOverhead)
			return nil, ErrDiskBudget
		}
		offset += int64(recordBytes)
		if err := writeRecord(f, key, value, deleted); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			owner.releaseDisk(size)
			owner.releaseMemory(segmentOverhead)
			return nil, fmt.Errorf("write overlay spill segment: %w", err)
		}
	}
	return finishSegmentFile(ctx, f, path, owner, size, count, &index, indexCount)
}

func writeOneSegment(ctx context.Context, owner *budget, dir string, key [32]byte, value []byte, deleted bool) (*spillSegment, error) {
	recordBytes, ok := recordSize(value)
	if !ok || segmentHeaderSize > math.MaxUint64-recordBytes {
		return nil, ErrDiskBudget
	}
	size := segmentHeaderSize + recordBytes
	f, path, err := createSegmentFile(ctx, owner, dir, size)
	if err != nil {
		return nil, err
	}
	if err := writeRecord(f, key, value, deleted); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		owner.releaseDisk(size)
		owner.releaseMemory(segmentOverhead)
		return nil, fmt.Errorf("write overlay spill record: %w", err)
	}
	index := [sparseIndexMax]sparseIndexEntry{{key: key, offset: int64(segmentHeaderSize)}}
	return finishSegmentFile(ctx, f, path, owner, size, 1, &index, 1)
}

type segmentCursor struct {
	segment  *spillSegment
	owner    *budget
	file     *os.File
	after    [32]byte
	hasAfter bool
	last     [32]byte
	hasLast  bool
	current  uint64
	closed   bool
}

func openSegmentCursor(ctx context.Context, owner *budget, segment *spillSegment, after *[32]byte) (*segmentCursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(segment.path)
	if err != nil {
		return nil, fmt.Errorf("open overlay spill segment: %w", err)
	}
	var header [len(segmentMagic)]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: read header: %v", ErrCorruptSegment, err)
	}
	if header != segmentMagic {
		_ = f.Close()
		return nil, fmt.Errorf("%w: bad header", ErrCorruptSegment)
	}
	if after != nil && segment.indexCount != 0 {
		best := int64(segmentHeaderSize)
		for i := 0; i < int(segment.indexCount); i++ {
			if bytes.Compare(segment.index[i].key[:], after[:]) > 0 {
				break
			}
			best = segment.index[i].offset
		}
		if _, err := f.Seek(best, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("seek overlay spill segment: %w", err)
		}
	}
	cursor := &segmentCursor{segment: segment, owner: owner, file: f}
	if after != nil {
		cursor.after = *after
		cursor.hasAfter = true
	}
	return cursor, nil
}

func (c *segmentCursor) seekKey(key [32]byte) error {
	if c == nil || c.closed {
		return ErrClosed
	}
	if c.segment.indexCount == 0 {
		return nil
	}
	best := int64(segmentHeaderSize)
	for i := 0; i < int(c.segment.indexCount); i++ {
		if bytes.Compare(c.segment.index[i].key[:], key[:]) > 0 {
			break
		}
		best = c.segment.index[i].offset
	}
	if _, err := c.file.Seek(best, io.SeekStart); err != nil {
		return fmt.Errorf("seek overlay spill segment: %w", err)
	}
	c.hasLast = false
	c.releaseCurrent()
	return nil
}

func (c *segmentCursor) releaseCurrent() {
	if c.current != 0 {
		c.owner.releaseMemory(c.current)
		c.current = 0
	}
}

func (c *segmentCursor) close() error {
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	c.releaseCurrent()
	if c.file == nil {
		return nil
	}
	return c.file.Close()
}

func (c *segmentCursor) next(ctx context.Context) ([32]byte, []byte, bool, bool, error) {
	var key [32]byte
	if c == nil || c.closed {
		return key, nil, false, false, ErrClosed
	}
	c.releaseCurrent()
	for {
		if err := ctx.Err(); err != nil {
			return key, nil, false, false, err
		}
		var keyBytes [32]byte
		n, err := io.ReadFull(c.file, keyBytes[:])
		if err == io.EOF && n == 0 {
			return key, nil, false, false, nil
		}
		if err != nil {
			return key, nil, false, false, fmt.Errorf("%w: key: %v", ErrCorruptSegment, err)
		}
		var flag [1]byte
		if _, err := io.ReadFull(c.file, flag[:]); err != nil || flag[0] > 1 {
			if err == nil {
				err = fmt.Errorf("invalid tombstone flag %d", flag[0])
			}
			return key, nil, false, false, fmt.Errorf("%w: flag: %v", ErrCorruptSegment, err)
		}
		var length [8]byte
		if _, err := io.ReadFull(c.file, length[:]); err != nil {
			return key, nil, false, false, fmt.Errorf("%w: length: %v", ErrCorruptSegment, err)
		}
		valueLen := binary.BigEndian.Uint64(length[:])
		if valueLen > uint64(maxInt()) {
			return key, nil, false, false, fmt.Errorf("%w: value length overflows int", ErrCorruptSegment)
		}
		if c.hasLast && bytes.Compare(keyBytes[:], c.last[:]) <= 0 {
			return key, nil, false, false, ErrOrdering
		}
		c.last = keyBytes
		c.hasLast = true
		if c.hasAfter && bytes.Compare(keyBytes[:], c.after[:]) <= 0 {
			if _, err := io.CopyN(io.Discard, c.file, int64(valueLen)); err != nil {
				return key, nil, false, false, fmt.Errorf("%w: skipped value: %v", ErrCorruptSegment, err)
			}
			continue
		}
		charge, ok := addCharge(workspaceStream, valueLen)
		if !ok || !c.owner.reserveMemory(charge) {
			return key, nil, false, false, ErrMemoryBudget
		}
		value := make([]byte, int(valueLen))
		if _, err := io.ReadFull(c.file, value); err != nil {
			c.owner.releaseMemory(charge)
			return key, nil, false, false, fmt.Errorf("%w: value: %v", ErrCorruptSegment, err)
		}
		c.current = charge
		return keyBytes, value, flag[0] == 1, true, nil
	}
}

func (c *segmentCursor) get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	if err := c.seekKey(key); err != nil {
		return nil, false, err
	}
	for {
		candidate, value, deleted, ok, err := c.next(ctx)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		cmp := bytes.Compare(candidate[:], key[:])
		if cmp > 0 {
			return nil, false, nil
		}
		if cmp == 0 {
			if deleted {
				return nil, true, nil
			}
			copyValue := make([]byte, len(value))
			copy(copyValue, value)
			return copyValue, true, nil
		}
	}
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func (s *spillSegment) cursor(ctx context.Context, owner *budget, after *[32]byte) (*segmentCursor, error) {
	return openSegmentCursor(ctx, owner, s, after)
}

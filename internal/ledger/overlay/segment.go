package overlay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"sync"
	"sync/atomic"
)

var segmentMagic = [8]byte{'G', 'X', 'O', 'V', 1, 0, 0, 0}

const (
	segmentHeaderSize          = uint64(len(segmentMagic))
	recordHeaderSize           = uint64(32 + 1 + 8)
	sparseIndexMax             = 8
	maxSegmentReaders          = 64
	maxCachedReaders           = 48
	maxFallbackReaders         = 1
	maxEphemeralReaders        = maxSegmentReaders - maxCachedReaders - maxFallbackReaders
	segmentWriteBufferBytes    = 32 << 10
	minSegmentWriteBufferBytes = 1 << 10
	bloomWordCount             = 32
	bloomBitCount              = bloomWordCount * 64
	bloomProbeCount            = 3
)

var (
	cachedReaderSlots    = make(chan struct{}, maxCachedReaders)
	ephemeralReaderSlots = make(chan struct{}, maxEphemeralReaders)
	fallbackReaderMu     sync.Mutex
)

func tryReserveCachedReaderSlot() bool {
	select {
	case cachedReaderSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func tryReserveEphemeralReaderSlot() bool {
	select {
	case ephemeralReaderSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseCachedReaderSlot() {
	<-cachedReaderSlots
}

func releaseEphemeralReaderSlot() {
	<-ephemeralReaderSlots
}

type sparseIndexEntry struct {
	key    [32]byte
	offset int64
}

type segmentBloom [bloomWordCount]uint64

func bloomMix(value uint64) uint64 {
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func bloomHashes(key [32]byte) (uint64, uint64) {
	hash1 := uint64(0x9e3779b97f4a7c15)
	hash2 := uint64(0xc2b2ae3d27d4eb4f)
	for offset := 0; offset < len(key); offset += 8 {
		word := binary.LittleEndian.Uint64(key[offset : offset+8])
		hash1 ^= word + 0x9e3779b97f4a7c15
		hash1 = bits.RotateLeft64(hash1, 27) * 0x3c79ac492ba7b653
		hash2 += word ^ hash1
		hash2 = bits.RotateLeft64(hash2, 31) * 0x1c69b3f74ac4ae35
	}
	return bloomMix(hash1), bloomMix(hash2) | 1
}

func (b *segmentBloom) add(key [32]byte) {
	hash1, hash2 := bloomHashes(key)
	for i := uint64(0); i < bloomProbeCount; i++ {
		bit := (hash1 + i*hash2) & (bloomBitCount - 1)
		b[bit>>6] |= uint64(1) << (bit & 63)
	}
}

func (b *segmentBloom) mayContain(key [32]byte) bool {
	hash1, hash2 := bloomHashes(key)
	for i := uint64(0); i < bloomProbeCount; i++ {
		bit := (hash1 + i*hash2) & (bloomBitCount - 1)
		if b[bit>>6]&(uint64(1)<<(bit&63)) == 0 {
			return false
		}
	}
	return true
}

type spillSegment struct {
	refs       atomic.Int64
	fileMu     sync.Mutex
	file       *os.File
	readerSlot bool

	path       string
	size       uint64
	count      uint64
	index      [sparseIndexMax]sparseIndexEntry
	indexCount uint8
	bloom      segmentBloom
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
	s.fileMu.Lock()
	file := s.file
	s.file = nil
	cached := s.readerSlot
	s.readerSlot = false
	s.fileMu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if cached {
		releaseCachedReaderSlot()
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

type segmentWriter struct {
	file         *os.File
	buffer       *bufio.Writer
	owner        *budget
	bufferCharge uint64
}

func newSegmentWriter(file *os.File, owner *budget, buffered bool) *segmentWriter {
	writer := &segmentWriter{file: file, owner: owner}
	if buffered && owner != nil {
		for size := uint64(segmentWriteBufferBytes); size >= minSegmentWriteBufferBytes; size >>= 1 {
			if owner.reserveMemory(size) {
				writer.buffer = bufio.NewWriterSize(file, int(size))
				writer.bufferCharge = size
				break
			}
		}
	}
	return writer
}

func (w *segmentWriter) Write(data []byte) (int, error) {
	if w == nil || w.file == nil {
		return 0, ErrClosed
	}
	if w.buffer != nil {
		return w.buffer.Write(data)
	}
	return w.file.Write(data)
}

func (w *segmentWriter) close() error {
	return w.finish(true)
}

func (w *segmentWriter) abort() error {
	return w.finish(false)
}

func (w *segmentWriter) finish(flush bool) error {
	if w == nil || w.file == nil {
		return nil
	}
	var first error
	if flush && w.buffer != nil {
		if err := w.buffer.Flush(); err != nil {
			first = err
		}
	}
	if err := w.file.Close(); err != nil && first == nil {
		first = err
	}
	w.file = nil
	w.buffer = nil
	if w.owner != nil {
		w.owner.releaseMemory(w.bufferCharge)
	}
	w.bufferCharge = 0
	return first
}

func discardSegmentFile(w *segmentWriter, path string, owner *budget, reserved uint64) {
	_ = w.abort()
	_ = os.Remove(path)
	owner.releaseDisk(reserved)
	owner.releaseMemory(segmentOverhead)
}

func createSegmentFile(ctx context.Context, owner *budget, dir string, size uint64, buffered bool) (*segmentWriter, string, error) {
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
	writer := newSegmentWriter(f, owner, buffered)
	cleanup := func(cause error) (*segmentWriter, string, error) {
		_ = writer.abort()
		_ = os.Remove(f.Name())
		owner.releaseDisk(size)
		owner.releaseMemory(segmentOverhead)
		return nil, "", cause
	}
	if err := writeAll(writer, segmentMagic[:]); err != nil {
		return cleanup(fmt.Errorf("write overlay spill header: %w", err))
	}
	return writer, f.Name(), nil
}

func finishSegmentFile(ctx context.Context, f *segmentWriter, path string, owner *budget, reserved, count uint64, index *[sparseIndexMax]sparseIndexEntry, indexCount int, bloom segmentBloom) (*spillSegment, error) {
	if err := ctx.Err(); err != nil {
		discardSegmentFile(f, path, owner, reserved)
		return nil, err
	}
	if err := f.close(); err != nil {
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
	var reader *os.File
	readerSlot := tryReserveCachedReaderSlot()
	if readerSlot {
		reader, err = os.Open(path)
		if err != nil {
			releaseCachedReaderSlot()
			reader = nil
			readerSlot = false
		}
	}
	s := &spillSegment{file: reader, readerSlot: readerSlot, path: path, size: actual, count: count, owner: owner}
	if index != nil {
		if indexCount > sparseIndexMax {
			indexCount = sparseIndexMax
		}
		copy(s.index[:], index[:indexCount])
		s.indexCount = uint8(indexCount)
	}
	s.bloom = bloom
	s.refs.Store(1)
	return s, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := w.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func writeRecord(w io.Writer, key [32]byte, value []byte, deleted bool) error {
	var header [32 + 1 + 8]byte
	copy(header[:32], key[:])
	flag := byte(0)
	if deleted {
		flag = 1
	}
	header[32] = flag
	binary.BigEndian.PutUint64(header[33:], uint64(len(value)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	if len(value) != 0 {
		return writeAll(w, value)
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
	f, path, err := createSegmentFile(ctx, owner, dir, size, true)
	if err != nil {
		return nil, err
	}
	cursor := newTreeCursor(root, nil)
	var previous *[32]byte
	var index [sparseIndexMax]sparseIndexEntry
	var bloom segmentBloom
	indexCount := 0
	stride := count / sparseIndexMax
	if stride == 0 {
		stride = 1
	}
	position := uint64(0)
	offset := int64(segmentHeaderSize)
	for {
		if err := ctx.Err(); err != nil {
			discardSegmentFile(f, path, owner, size)
			return nil, err
		}
		key, value, deleted, ok := cursor.next()
		if !ok {
			break
		}
		if previous != nil && bytes.Compare(key[:], previous[:]) <= 0 {
			discardSegmentFile(f, path, owner, size)
			return nil, ErrOrdering
		}
		previous = &key
		if position%stride == 0 && indexCount < sparseIndexMax {
			index[indexCount] = sparseIndexEntry{key: key, offset: offset}
			indexCount++
		}
		bloom.add(key)
		position++
		recordBytes, valid := recordSize(value)
		if !valid || recordBytes > uint64(math.MaxInt64-offset) {
			discardSegmentFile(f, path, owner, size)
			return nil, ErrDiskBudget
		}
		offset += int64(recordBytes)
		if err := writeRecord(f, key, value, deleted); err != nil {
			discardSegmentFile(f, path, owner, size)
			return nil, fmt.Errorf("write overlay spill segment: %w", err)
		}
	}
	return finishSegmentFile(ctx, f, path, owner, size, count, &index, indexCount, bloom)
}

func writeOneSegment(ctx context.Context, owner *budget, dir string, key [32]byte, value []byte, deleted bool) (*spillSegment, error) {
	recordBytes, ok := recordSize(value)
	if !ok || segmentHeaderSize > math.MaxUint64-recordBytes {
		return nil, ErrDiskBudget
	}
	size := segmentHeaderSize + recordBytes
	f, path, err := createSegmentFile(ctx, owner, dir, size, true)
	if err != nil {
		return nil, err
	}
	if err := writeRecord(f, key, value, deleted); err != nil {
		discardSegmentFile(f, path, owner, size)
		return nil, fmt.Errorf("write overlay spill record: %w", err)
	}
	index := [sparseIndexMax]sparseIndexEntry{{key: key, offset: int64(segmentHeaderSize)}}
	var bloom segmentBloom
	bloom.add(key)
	return finishSegmentFile(ctx, f, path, owner, size, 1, &index, 1, bloom)
}

func readAtFull(file *os.File, data []byte, offset int64) (int, error) {
	read := 0
	for read < len(data) {
		n, err := file.ReadAt(data[read:], offset+int64(read))
		read += n
		if read == len(data) {
			return read, nil
		}
		if err != nil {
			if err == io.EOF && read != 0 {
				return read, io.ErrUnexpectedEOF
			}
			return read, err
		}
		if n == 0 {
			return read, io.ErrUnexpectedEOF
		}
	}
	return read, nil
}

func readPathAt(path string, data []byte, offset int64) (int, error) {
	fallbackReaderMu.Lock()
	defer fallbackReaderMu.Unlock()
	file, err := os.Open(path)
	if err != nil {
		return 0, segmentOpenError{err: err}
	}
	read, readErr := readAtFull(file, data, offset)
	closeErr := file.Close()
	if readErr != nil {
		return read, readErr
	}
	return read, closeErr
}

type segmentOpenError struct {
	err error
}

func (e segmentOpenError) Error() string {
	return e.err.Error()
}

func (e segmentOpenError) Unwrap() error {
	return e.err
}

type segmentCursor struct {
	segment     *spillSegment
	owner       *budget
	file        *os.File
	closeFile   bool
	readerSlot  bool
	heldSegment bool
	offset      int64
	after       [32]byte
	hasAfter    bool
	last        [32]byte
	hasLast     bool
	current     uint64
	closed      bool
}

func openSegmentCursor(ctx context.Context, owner *budget, segment *spillSegment, after *[32]byte) (*segmentCursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if segment == nil {
		return nil, fmt.Errorf("%w: nil segment", ErrCorruptSegment)
	}
	retainSegment(segment)
	fail := func(err error, file *os.File, closeFile, readerSlot bool) (*segmentCursor, error) {
		if closeFile && file != nil {
			_ = file.Close()
		}
		if readerSlot {
			releaseEphemeralReaderSlot()
		}
		releaseSegment(segment)
		return nil, err
	}
	segment.fileMu.Lock()
	f := segment.file
	segment.fileMu.Unlock()
	ephemeral := false
	if f == nil {
		if tryReserveEphemeralReaderSlot() {
			var openErr error
			f, openErr = os.Open(segment.path)
			if openErr != nil {
				return fail(fmt.Errorf("open overlay spill segment: %w", openErr), nil, false, true)
			}
			ephemeral = true
		}
	}
	var header [len(segmentMagic)]byte
	var headerErr error
	if f != nil {
		_, headerErr = readAtFull(f, header[:], 0)
	} else {
		_, headerErr = readPathAt(segment.path, header[:], 0)
	}
	if headerErr != nil {
		var openErr segmentOpenError
		if errors.As(headerErr, &openErr) {
			return fail(fmt.Errorf("open overlay spill segment: %w", openErr.err), f, ephemeral, ephemeral)
		}
		return fail(fmt.Errorf("%w: read header: %v", ErrCorruptSegment, headerErr), f, ephemeral, ephemeral)
	}
	if header != segmentMagic {
		return fail(fmt.Errorf("%w: bad header", ErrCorruptSegment), f, ephemeral, ephemeral)
	}
	offset := int64(segmentHeaderSize)
	if after != nil && segment.indexCount != 0 {
		best := offset
		for i := 0; i < int(segment.indexCount); i++ {
			if bytes.Compare(segment.index[i].key[:], after[:]) > 0 {
				break
			}
			best = segment.index[i].offset
		}
		offset = best
	}
	cursor := &segmentCursor{
		segment: segment, owner: owner, file: f,
		closeFile: ephemeral, readerSlot: ephemeral,
		heldSegment: true, offset: offset,
	}
	if after != nil {
		cursor.after = *after
		cursor.hasAfter = true
	}
	return cursor, nil
}

func (c *segmentCursor) readAt(data []byte, offset int64) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if c.file != nil {
		return readAtFull(c.file, data, offset)
	}
	return readPathAt(c.segment.path, data, offset)
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
	c.offset = best
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

func (c *segmentCursor) advanceValue(valueLen uint64) error {
	if c.offset < 0 || uint64(c.offset) > c.segment.size || valueLen > c.segment.size-uint64(c.offset) {
		return fmt.Errorf("%w: value exceeds segment", ErrCorruptSegment)
	}
	c.offset += int64(valueLen)
	return nil
}

func (c *segmentCursor) skipValue(valueOffset int64, valueLen uint64) error {
	if err := c.advanceValue(valueLen); err != nil {
		return err
	}
	if valueLen == 0 {
		return nil
	}
	var lastByte [1]byte
	lastOffset := valueOffset + int64(valueLen) - 1
	if _, err := c.readAt(lastByte[:], lastOffset); err != nil {
		return fmt.Errorf("%w: skipped value: %v", ErrCorruptSegment, err)
	}
	return nil
}

func (c *segmentCursor) readRecordHeader() (key [32]byte, flag byte, valueLen uint64, valueOffset int64, err error) {
	var header [32 + 1 + 8]byte
	n, err := c.readAt(header[:], c.offset)
	if err == io.EOF && n == 0 {
		return key, 0, 0, c.offset, io.EOF
	}
	if err != nil {
		return key, 0, 0, c.offset, fmt.Errorf("%w: record header: %v", ErrCorruptSegment, err)
	}
	copy(key[:], header[:32])
	flag = header[32]
	if flag > 1 {
		return key, 0, 0, c.offset, fmt.Errorf("%w: invalid tombstone flag %d", ErrCorruptSegment, flag)
	}
	valueLen = binary.BigEndian.Uint64(header[33:])
	if valueLen > uint64(maxInt()) {
		return key, 0, 0, c.offset, fmt.Errorf("%w: value length overflows int", ErrCorruptSegment)
	}
	c.offset += int64(len(header))
	valueOffset = c.offset
	if valueLen > uint64(math.MaxInt64-c.offset) {
		return key, 0, 0, valueOffset, fmt.Errorf("%w: value offset overflows int64", ErrCorruptSegment)
	}
	return key, flag, valueLen, valueOffset, nil
}

func (c *segmentCursor) close() error {
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	c.releaseCurrent()
	var first error
	if c.closeFile && c.file != nil {
		if err := c.file.Close(); err != nil {
			first = err
		}
		c.file = nil
	}
	if c.readerSlot {
		releaseEphemeralReaderSlot()
		c.readerSlot = false
	}
	if c.heldSegment {
		segment := c.segment
		c.heldSegment = false
		c.segment = nil
		releaseSegment(segment)
	}
	return first
}

func (c *segmentCursor) next(ctx context.Context) ([32]byte, []byte, bool, bool, error) {
	var key [32]byte
	if c == nil || c.closed {
		return key, nil, false, false, ErrClosed
	}
	c.releaseCurrent()
	if err := ctx.Err(); err != nil {
		return key, nil, false, false, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return key, nil, false, false, err
		}
		keyBytes, flag, valueLen, valueOffset, err := c.readRecordHeader()
		if err == io.EOF {
			return key, nil, false, false, nil
		}
		if err != nil {
			return key, nil, false, false, err
		}
		if c.hasLast && bytes.Compare(keyBytes[:], c.last[:]) <= 0 {
			return key, nil, false, false, ErrOrdering
		}
		c.last = keyBytes
		c.hasLast = true
		if c.hasAfter && bytes.Compare(keyBytes[:], c.after[:]) <= 0 {
			if err := c.skipValue(valueOffset, valueLen); err != nil {
				return key, nil, false, false, err
			}
			continue
		}
		charge, ok := addCharge(workspaceStream, valueLen)
		if !ok || !c.owner.reserveMemory(charge) {
			return key, nil, false, false, ErrMemoryBudget
		}
		if err := c.advanceValue(valueLen); err != nil {
			c.owner.releaseMemory(charge)
			return key, nil, false, false, err
		}
		value := make([]byte, int(valueLen))
		if _, err := c.readAt(value, valueOffset); err != nil {
			c.owner.releaseMemory(charge)
			return key, nil, false, false, fmt.Errorf("%w: value: %v", ErrCorruptSegment, err)
		}
		c.current = charge
		return keyBytes, value, flag == 1, true, nil
	}
}

func (c *segmentCursor) get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	if err := c.seekKey(key); err != nil {
		return nil, false, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		candidate, flag, valueLen, valueOffset, err := c.readRecordHeader()
		if err == io.EOF {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if c.hasLast && bytes.Compare(candidate[:], c.last[:]) <= 0 {
			return nil, false, ErrOrdering
		}
		c.last = candidate
		c.hasLast = true
		cmp := bytes.Compare(candidate[:], key[:])
		if cmp > 0 {
			if err := c.skipValue(valueOffset, valueLen); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
		if cmp < 0 {
			if err := c.skipValue(valueOffset, valueLen); err != nil {
				return nil, false, err
			}
			continue
		}
		if flag == 1 {
			if err := c.skipValue(valueOffset, valueLen); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
		charge, ok := addCharge(workspaceStream, valueLen)
		if !ok || !c.owner.reserveMemory(charge) {
			return nil, false, ErrMemoryBudget
		}
		if err := c.advanceValue(valueLen); err != nil {
			c.owner.releaseMemory(charge)
			return nil, false, err
		}
		value := make([]byte, int(valueLen))
		if _, err := c.readAt(value, valueOffset); err != nil {
			c.owner.releaseMemory(charge)
			return nil, false, fmt.Errorf("%w: value: %v", ErrCorruptSegment, err)
		}
		// Return the temporary value directly. The caller owns the bytes, so
		// release the cursor's workspace charge before it closes.
		c.current = 0
		c.owner.releaseMemory(charge)
		return value, true, nil
	}
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func (s *spillSegment) cursor(ctx context.Context, owner *budget, after *[32]byte) (*segmentCursor, error) {
	return openSegmentCursor(ctx, owner, s, after)
}

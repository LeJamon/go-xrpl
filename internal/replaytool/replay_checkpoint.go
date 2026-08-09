package replaytool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	checkpointMagic            = "XRPLCKPT"
	checkpointVersion          = uint32(2)
	checkpointHeaderSize       = int64(len(checkpointMagic) + 4 + 4)
	checkpointFooterSize       = int64(8 + sha256.Size)
	checkpointRecordHeaderSize = int64(32 + 4)
	checkpointMinEntrySize     = uint32(12)
	checkpointMaxEntrySize     = uint32(16 << 20)
)

func checkpointPath(dir string, seq uint32) string {
	return filepath.Join(dir, fmt.Sprintf("checkpoint_%d.dat", seq))
}

// writeCheckpoint writes a checksummed checkpoint through one ordered state
// traversal and publishes it durably with a same-directory atomic rename.
func writeCheckpoint(ctx context.Context, dir string, seq uint32, stateMap *shamap.SHAMap) (err error) {
	if stateMap == nil {
		return errors.New("checkpoint state map is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating checkpoint directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, fmt.Sprintf("checkpoint_%d_*.tmp", seq))
	if err != nil {
		return fmt.Errorf("creating temporary checkpoint: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			err = errors.Join(err, tmp.Close())
		}
		if !committed {
			if removeErr := os.Remove(tmpName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("removing temporary checkpoint: %w", removeErr))
			}
		}
	}()

	buffered := bufio.NewWriterSize(tmp, 256<<10)
	digest := sha256.New()
	stream := io.MultiWriter(buffered, digest)
	if _, err := io.WriteString(stream, checkpointMagic); err != nil {
		return fmt.Errorf("writing checkpoint magic: %w", err)
	}
	if err := binary.Write(stream, binary.BigEndian, checkpointVersion); err != nil {
		return fmt.Errorf("writing checkpoint version: %w", err)
	}
	if err := binary.Write(stream, binary.BigEndian, seq); err != nil {
		return fmt.Errorf("writing checkpoint sequence: %w", err)
	}

	var count uint64
	var previous [32]byte
	havePrevious := false
	var callbackErr error
	if err := stateMap.ForEachCtxReleasing(ctx, func(item *shamap.Item) bool {
		key := item.Key()
		data := item.Data()
		switch {
		case havePrevious && bytes.Compare(previous[:], key[:]) >= 0:
			callbackErr = errors.New("checkpoint traversal returned non-increasing keys")
		case uint64(len(data)) < uint64(checkpointMinEntrySize):
			callbackErr = fmt.Errorf("checkpoint entry %x is shorter than %d bytes", key, checkpointMinEntrySize)
		case uint64(len(data)) > uint64(checkpointMaxEntrySize):
			callbackErr = fmt.Errorf("checkpoint entry %x exceeds %d bytes", key, checkpointMaxEntrySize)
		}
		if callbackErr != nil {
			return false
		}
		if _, callbackErr = stream.Write(key[:]); callbackErr != nil {
			return false
		}
		if callbackErr = binary.Write(stream, binary.BigEndian, uint32(len(data))); callbackErr != nil {
			return false
		}
		if _, callbackErr = stream.Write(data); callbackErr != nil {
			return false
		}
		previous = key
		havePrevious = true
		count++
		return true
	}); err != nil {
		return fmt.Errorf("traversing checkpoint state: %w", err)
	}
	if callbackErr != nil {
		return fmt.Errorf("writing checkpoint entries: %w", callbackErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := binary.Write(stream, binary.BigEndian, count); err != nil {
		return fmt.Errorf("writing checkpoint count: %w", err)
	}
	if _, err := buffered.Write(digest.Sum(nil)); err != nil {
		return fmt.Errorf("writing checkpoint checksum: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flushing checkpoint: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		return fmt.Errorf("closing checkpoint: %w", err)
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, checkpointPath(dir, seq)); err != nil {
		return fmt.Errorf("publishing checkpoint: %w", err)
	}
	committed = true
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("syncing checkpoint directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) (err error) {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	return dir.Sync()
}

// loadCheckpoint validates a complete v2 checkpoint before allocating any
// entry payload and reconstructs its state map.
func loadCheckpoint(ctx context.Context, path string) (_ *shamap.SHAMap, _ uint32, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("opening checkpoint %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stating checkpoint: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("checkpoint %s is not a regular file", path)
	}
	if info.Size() < checkpointHeaderSize+checkpointFooterSize {
		return nil, 0, fmt.Errorf("checkpoint %s is too short", path)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	footerOffset := info.Size() - checkpointFooterSize
	footer := make([]byte, checkpointFooterSize)
	if _, err := f.ReadAt(footer, footerOffset); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint footer: %w", err)
	}
	count := binary.BigEndian.Uint64(footer[:8])
	expectedDigest := footer[8:]

	digest := sha256.New()
	if err := copyCheckpointHash(ctx, digest, io.NewSectionReader(f, 0, info.Size()-sha256.Size)); err != nil {
		return nil, 0, fmt.Errorf("hashing checkpoint: %w", err)
	}
	if !hmac.Equal(digest.Sum(nil), expectedDigest) {
		return nil, 0, fmt.Errorf("checkpoint %s checksum mismatch", path)
	}

	reader := bufio.NewReader(io.NewSectionReader(f, 0, footerOffset))
	magic := make([]byte, len(checkpointMagic))
	if _, err := io.ReadFull(reader, magic); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint magic: %w", err)
	}
	if string(magic) != checkpointMagic {
		return nil, 0, fmt.Errorf("invalid checkpoint magic in %s", path)
	}
	var version, seq uint32
	if err := binary.Read(reader, binary.BigEndian, &version); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint version: %w", err)
	}
	if version != checkpointVersion {
		if version == 1 {
			return nil, 0, fmt.Errorf("checkpoint version 1 is not integrity protected; regenerate %s", path)
		}
		return nil, 0, fmt.Errorf("unsupported checkpoint version %d in %s", version, path)
	}
	if err := binary.Read(reader, binary.BigEndian, &seq); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint sequence: %w", err)
	}

	recordBytes := footerOffset - checkpointHeaderSize
	if count > uint64(recordBytes/(checkpointRecordHeaderSize+int64(checkpointMinEntrySize))) {
		return nil, 0, fmt.Errorf("checkpoint count %d cannot fit in %d record bytes", count, recordBytes)
	}
	limited := &io.LimitedReader{R: reader, N: recordBytes}
	stateMap := shamap.New(shamap.TypeState)
	var previous [32]byte
	havePrevious := false
	for i := uint64(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if limited.N < checkpointRecordHeaderSize {
			return nil, 0, fmt.Errorf("checkpoint entry %d header exceeds remaining data", i)
		}
		var key [32]byte
		if _, err := io.ReadFull(limited, key[:]); err != nil {
			return nil, 0, fmt.Errorf("reading checkpoint entry %d key: %w", i, err)
		}
		if havePrevious && bytes.Compare(previous[:], key[:]) >= 0 {
			return nil, 0, fmt.Errorf("checkpoint entry %d key is duplicate or out of order", i)
		}
		var dataLen uint32
		if err := binary.Read(limited, binary.BigEndian, &dataLen); err != nil {
			return nil, 0, fmt.Errorf("reading checkpoint entry %d length: %w", i, err)
		}
		if dataLen < checkpointMinEntrySize || dataLen > checkpointMaxEntrySize {
			return nil, 0, fmt.Errorf("checkpoint entry %d length %d is outside %d..%d", i, dataLen, checkpointMinEntrySize, checkpointMaxEntrySize)
		}
		if int64(dataLen) > limited.N {
			return nil, 0, fmt.Errorf("checkpoint entry %d length %d exceeds %d remaining bytes", i, dataLen, limited.N)
		}
		data := make([]byte, int(dataLen))
		if _, err := io.ReadFull(limited, data); err != nil {
			return nil, 0, fmt.Errorf("reading checkpoint entry %d data: %w", i, err)
		}
		if err := stateMap.Put(key, data); err != nil {
			return nil, 0, fmt.Errorf("injecting checkpoint entry %d: %w", i, err)
		}
		previous = key
		havePrevious = true
	}
	if limited.N != 0 {
		return nil, 0, fmt.Errorf("checkpoint contains %d trailing record bytes", limited.N)
	}
	return stateMap, seq, nil
}

func copyCheckpointHash(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if _, err := dst.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

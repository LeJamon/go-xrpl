package replaytool

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	checkpointMagic   = "XRPLCKPT"
	checkpointVersion = uint32(1)
)

func checkpointPath(dir string, seq uint32) string {
	return filepath.Join(dir, fmt.Sprintf("checkpoint_%d.dat", seq))
}

// writeCheckpoint persists the full state map and its ledger sequence to disk.
// The write is atomic: it serializes to a temp file in the same directory and
// renames it into place so a crash mid-write never leaves a corrupt checkpoint.
//
// Format: magic | version(u32) | seq(u32) | count(u64) | count*(key[32] | len(u32) | data).
func writeCheckpoint(dir string, seq uint32, stateMap *shamap.SHAMap) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating checkpoint dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, fmt.Sprintf("checkpoint_%d_*.tmp", seq))
	if err != nil {
		return fmt.Errorf("creating temp checkpoint: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	w := bufio.NewWriter(tmp)

	if _, err := w.WriteString(checkpointMagic); err != nil {
		tmp.Close()
		return err
	}
	for _, v := range []any{checkpointVersion, seq, uint64(stateMap.Size())} {
		if err := binary.Write(w, binary.BigEndian, v); err != nil {
			tmp.Close()
			return err
		}
	}

	var writeErr error
	_ = stateMap.ForEach(func(item *shamap.Item) bool {
		key := item.Key()
		data := item.Data()
		if _, writeErr = w.Write(key[:]); writeErr != nil {
			return false
		}
		if writeErr = binary.Write(w, binary.BigEndian, uint32(len(data))); writeErr != nil {
			return false
		}
		if _, writeErr = w.Write(data); writeErr != nil {
			return false
		}
		return true
	})
	if writeErr != nil {
		tmp.Close()
		return fmt.Errorf("writing checkpoint entries: %w", writeErr)
	}

	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpName, checkpointPath(dir, seq))
}

// loadCheckpoint reconstructs the state map and ledger sequence from a
// checkpoint written by writeCheckpoint.
func loadCheckpoint(path string) (*shamap.SHAMap, uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("opening checkpoint %s: %w", path, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)

	magic := make([]byte, len(checkpointMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint magic: %w", err)
	}
	if string(magic) != checkpointMagic {
		return nil, 0, fmt.Errorf("invalid checkpoint magic in %s", path)
	}

	var version, seq uint32
	var count uint64
	if err := binary.Read(r, binary.BigEndian, &version); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint version: %w", err)
	}
	if version != checkpointVersion {
		return nil, 0, fmt.Errorf("unsupported checkpoint version %d in %s", version, path)
	}
	if err := binary.Read(r, binary.BigEndian, &seq); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint seq: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, 0, fmt.Errorf("reading checkpoint count: %w", err)
	}

	stateMap, err := shamap.New(shamap.TypeState)
	if err != nil {
		return nil, 0, fmt.Errorf("creating state map: %w", err)
	}

	for i := uint64(0); i < count; i++ {
		var key [32]byte
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return nil, 0, fmt.Errorf("reading entry %d key: %w", i, err)
		}
		var dataLen uint32
		if err := binary.Read(r, binary.BigEndian, &dataLen); err != nil {
			return nil, 0, fmt.Errorf("reading entry %d length: %w", i, err)
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, 0, fmt.Errorf("reading entry %d data: %w", i, err)
		}
		if err := stateMap.Put(key, data); err != nil {
			return nil, 0, fmt.Errorf("injecting entry %d: %w", i, err)
		}
	}

	return stateMap, seq, nil
}

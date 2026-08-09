package replaytool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func writeAtomicArtifact(path string, mode os.FileMode, write func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, tmp.Close())
		}
		if !committed {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting artifact mode: %w", err)
	}
	buffered := bufio.NewWriter(tmp)
	if err := write(buffered); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flushing artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		return fmt.Errorf("closing artifact: %w", err)
	}
	closed = true
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publishing artifact: %w", err)
	}
	committed = true
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("syncing artifact directory: %w", err)
	}
	return nil
}

func writeAtomicJSON(path string, value any) error {
	return writeAtomicArtifact(path, 0o644, func(w io.Writer) error {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	})
}

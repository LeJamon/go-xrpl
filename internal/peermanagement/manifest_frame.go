package peermanagement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	manifestSpoolThreshold  = 1 * 1024 * 1024
	manifestSpoolBufferSize = 64 * 1024
)

var (
	ErrManifestFrameClosed       = errors.New("manifest frame closed")
	ErrManifestFrameMaterialized = errors.New("manifest frame already materialized")
)

// ManifestFrame is a disk-backed inbound manifest payload.
type ManifestFrame struct {
	mu sync.Mutex

	path   string
	header MessageHeader
	budget *readBudget
	done   chan struct{}

	materializing bool
	materialized  bool
	closed        bool
	reservation   int64
}

func newManifestFrame(path string, header MessageHeader, budget *readBudget) *ManifestFrame {
	return &ManifestFrame{
		path:   path,
		header: header,
		budget: budget,
		done:   make(chan struct{}),
	}
}

// Header returns the validated wire header for this payload.
func (f *ManifestFrame) Header() MessageHeader {
	return f.header
}

// WireSize returns the on-wire payload size, excluding the message header.
func (f *ManifestFrame) WireSize() uint64 {
	return uint64(f.header.PayloadSize)
}

// Materialize reads the exact wire payload into memory while holding its share
// of the overlay manifest budget. The caller must Close the frame after use.
func (f *ManifestFrame) Materialize(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	f.mu.Lock()
	switch {
	case f.closed:
		f.mu.Unlock()
		return nil, ErrManifestFrameClosed
	case f.materializing || f.materialized:
		f.mu.Unlock()
		return nil, ErrManifestFrameMaterialized
	default:
		f.materializing = true
	}
	f.mu.Unlock()

	reservation := manifestReadReservation(f.header)
	if f.budget != nil {
		if err := f.budget.acquire(ctx, f.done, reservation); err != nil {
			f.mu.Lock()
			f.materializing = false
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return nil, ErrManifestFrameClosed
			}
			return nil, err
		}
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		if f.budget != nil {
			f.budget.release(reservation)
		}
		return nil, ErrManifestFrameClosed
	}
	f.reservation = reservation
	payload, err := os.ReadFile(f.path)
	if err == nil && uint32(len(payload)) != f.header.PayloadSize {
		err = fmt.Errorf("manifest payload size: got %d, want %d", len(payload), f.header.PayloadSize)
	}
	if err == nil {
		f.materialized = true
	}
	f.materializing = false
	f.mu.Unlock()

	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return payload, nil
}

// Close releases the memory reservation, removes the spool file, and permits
// this peer to accept another oversized manifest frame.
func (f *ManifestFrame) Close() error {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	path := f.path
	reservation := f.reservation
	f.reservation = 0
	close(f.done)
	f.mu.Unlock()

	if reservation > 0 && f.budget != nil {
		f.budget.release(reservation)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *ManifestFrame) completion() <-chan struct{} {
	return f.done
}

func spoolManifestFrame(
	r io.Reader,
	header MessageHeader,
	budget *readBudget,
	dir string,
) (*ManifestFrame, error) {
	file, err := os.CreateTemp(dir, "goxrpl-manifests-*")
	if err != nil {
		return nil, fmt.Errorf("create manifest spool: %w", err)
	}
	path := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}

	n, copyErr := io.CopyBuffer(
		file,
		io.LimitReader(r, int64(header.PayloadSize)),
		make([]byte, manifestSpoolBufferSize),
	)
	closeErr := file.Close()
	if copyErr != nil {
		cleanup()
		return nil, fmt.Errorf("spool manifest payload: %w", copyErr)
	}
	if n != int64(header.PayloadSize) {
		cleanup()
		return nil, fmt.Errorf("spool manifest payload: copied %d of %d bytes: %w",
			n, header.PayloadSize, io.ErrUnexpectedEOF)
	}
	if closeErr != nil {
		cleanup()
		return nil, fmt.Errorf("close manifest spool: %w", closeErr)
	}
	return newManifestFrame(path, header, budget), nil
}

func prepareManifestSpoolDir(dataDir string) (string, error) {
	if dataDir == "" {
		return "", nil
	}
	dir := filepath.Join(dataDir, "manifest-spool")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create manifest spool directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure manifest spool directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read manifest spool directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "goxrpl-manifests-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return "", fmt.Errorf("remove stale manifest spool %q: %w", entry.Name(), err)
		}
	}
	return dir, nil
}

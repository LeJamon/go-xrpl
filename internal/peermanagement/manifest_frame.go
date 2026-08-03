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

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

const (
	manifestSpoolThreshold  = 1 * 1024 * 1024
	manifestSpoolBufferSize = 64 * 1024
)

var (
	ErrManifestFrameClosed       = errors.New("manifest frame closed")
	ErrManifestFrameMaterialized = errors.New("manifest frame already materialized")
)

type manifestSpoolLocalError struct {
	operation string
	err       error
}

func (e *manifestSpoolLocalError) Error() string {
	return fmt.Sprintf("%s: %v", e.operation, e.err)
}

func (e *manifestSpoolLocalError) Unwrap() error {
	return e.err
}

type manifestSpoolWriter struct {
	writer io.Writer
	err    error
}

func (w *manifestSpoolWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && w.err == nil {
		w.err = err
	}
	return n, err
}

// ManifestFrame is a disk-backed inbound manifest payload.
type ManifestFrame struct {
	mu sync.Mutex

	path   string
	header message.Header
	budget *readBudget
	done   chan struct{}

	materializing bool
	materialized  bool
	closed        bool
	reservation   int64
}

func newManifestFrame(path string, header message.Header, budget *readBudget, reservation int64) *ManifestFrame {
	return &ManifestFrame{
		path:        path,
		header:      header,
		budget:      budget,
		done:        make(chan struct{}),
		reservation: reservation,
	}
}

// Header returns the validated wire header for this payload.
func (f *ManifestFrame) Header() message.Header {
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

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, ErrManifestFrameClosed
	}
	err := ctx.Err()
	var payload []byte
	if err == nil {
		payload, err = os.ReadFile(f.path)
	}
	if err == nil && uint32(len(payload)) != f.header.PayloadSize {
		err = fmt.Errorf("manifest payload size: got %d, want %d", len(payload), f.header.PayloadSize)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && f.header.Compressed {
		payload, err = message.DecompressLZ4(payload, int(f.header.UncompressedSize))
	}
	if err == nil {
		err = ctx.Err()
	}
	var releaseBytes int64
	if err == nil {
		f.materialized = true
		if f.header.Compressed {
			releaseBytes = int64(f.header.PayloadSize)
			f.reservation -= releaseBytes
		}
	}
	f.materializing = false
	f.mu.Unlock()

	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if releaseBytes > 0 && f.budget != nil {
		f.budget.release(releaseBytes)
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
	ctx context.Context,
	closeCh <-chan struct{},
	r io.Reader,
	header message.Header,
	budget *readBudget,
	dir string,
) (*ManifestFrame, error) {
	reservation := manifestFrameReservation(header)
	if budget != nil {
		if err := budget.acquire(ctx, closeCh, reservation); err != nil {
			return nil, err
		}
	}
	releaseBudget := true
	defer func() {
		if releaseBudget && budget != nil {
			budget.release(reservation)
		}
	}()

	file, err := os.CreateTemp(dir, "goxrpl-manifests-*")
	if err != nil {
		return nil, &manifestSpoolLocalError{operation: "create manifest spool", err: err}
	}
	path := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}

	copyErr := copyManifestPayload(file, r, header.PayloadSize)
	closeErr := file.Close()
	if copyErr != nil {
		cleanup()
		return nil, copyErr
	}
	if closeErr != nil {
		cleanup()
		return nil, &manifestSpoolLocalError{operation: "close manifest spool", err: closeErr}
	}
	releaseBudget = false
	return newManifestFrame(path, header, budget, reservation), nil
}

func manifestFrameReservation(header message.Header) int64 {
	wireBytes := int64(header.PayloadSize)
	decodedBytes := wireBytes
	if header.Compressed {
		decodedBytes = int64(header.UncompressedSize)
		return 2*wireBytes + decodedBytes
	}
	return wireBytes + decodedBytes
}

func copyManifestPayload(dst io.Writer, src io.Reader, size uint32) error {
	writer := &manifestSpoolWriter{writer: dst}
	n, err := io.CopyBuffer(
		writer,
		io.LimitReader(src, int64(size)),
		make([]byte, manifestSpoolBufferSize),
	)
	if writer.err != nil {
		return &manifestSpoolLocalError{operation: "spool manifest payload", err: writer.err}
	}
	if err != nil {
		return fmt.Errorf("spool manifest payload: %w", err)
	}
	if n != int64(size) {
		return fmt.Errorf("spool manifest payload: copied %d of %d bytes: %w",
			n, size, io.ErrUnexpectedEOF)
	}
	return nil
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

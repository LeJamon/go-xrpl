package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func manifestAndLedgerFrames(t *testing.T, manifest, ledger []byte) []byte {
	t.Helper()
	var wire bytes.Buffer
	require.NoError(t, message.WriteMessage(&wire, message.TypeManifests, manifest))
	require.NoError(t, message.WriteMessage(&wire, message.TypeLedgerData, ledger))
	return wire.Bytes()
}

func TestOversizedManifestPeersDoNotBlockLedgerFrames(t *testing.T) {
	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)

	manifestPayload := bytes.Repeat([]byte{0x4d}, manifestSpoolThreshold+1)
	manifestMessages := make(chan *InboundMessage, 2)
	acquisitionEvents := make(chan Event, 2)
	budget := newReadBudget(int64(2 * len(manifestPayload)))
	done := make(chan error, 2)

	for i := 1; i <= 2; i++ {
		peer := newLatencyTestPeer(t)
		peer.id = PeerID(i)
		peer.bufReader = bufio.NewReader(bytes.NewReader(
			manifestAndLedgerFrames(t, manifestPayload, []byte{byte(i)}),
		))
		peer.SetManifestMessages(manifestMessages)
		peer.SetManifestReadBudget(budget)
		peer.SetManifestSpoolDir(spoolDir)
		peer.SetAcquisitionEvents(acquisitionEvents)
		go func() {
			done <- peer.readLoop(context.Background())
		}()
	}

	descriptors := make(map[PeerID]*ManifestFrame, 2)
	for range 2 {
		inbound := <-manifestMessages
		require.Nil(t, inbound.Payload)
		require.NotNil(t, inbound.ManifestFrame)
		descriptors[inbound.PeerID] = inbound.ManifestFrame
	}
	require.Len(t, descriptors, 2)

	ledgers := make(map[PeerID][]byte, 2)
	for range 2 {
		event := <-acquisitionEvents
		ledgers[event.PeerID] = event.Payload
	}
	require.Equal(t, []byte{1}, ledgers[1])
	require.Equal(t, []byte{2}, ledgers[2])
	budget.mu.Lock()
	require.Zero(t, budget.used)
	budget.mu.Unlock()

	for range 2 {
		require.ErrorIs(t, <-done, io.EOF)
	}
	for _, frame := range descriptors {
		payload, err := frame.Materialize(context.Background())
		require.NoError(t, err)
		require.Equal(t, manifestPayload, payload)
		require.NoError(t, frame.Close())
	}
}

func TestSecondOversizedManifestBlocksOnlyItsPeer(t *testing.T) {
	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)

	manifestPayload := bytes.Repeat([]byte{0x4d}, manifestSpoolThreshold+1)
	var wire bytes.Buffer
	require.NoError(t, message.WriteMessage(&wire, message.TypeManifests, manifestPayload))
	require.NoError(t, message.WriteMessage(&wire, message.TypeLedgerData, []byte("first")))
	require.NoError(t, message.WriteMessage(&wire, message.TypeManifests, manifestPayload))
	require.NoError(t, message.WriteMessage(&wire, message.TypeLedgerData, []byte("second")))

	peer := newLatencyTestPeer(t)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire.Bytes()))
	manifestMessages := make(chan *InboundMessage, 2)
	acquisitionEvents := make(chan Event, 2)
	peer.SetManifestMessages(manifestMessages)
	peer.SetManifestReadBudget(newReadBudget(int64(2 * len(manifestPayload))))
	peer.SetManifestSpoolDir(spoolDir)
	peer.SetAcquisitionEvents(acquisitionEvents)
	var bootstrapReady atomic.Int32
	peer.onBootstrapReady = func() {
		bootstrapReady.Add(1)
	}

	done := make(chan error, 1)
	go func() {
		done <- peer.readLoop(context.Background())
	}()

	first := <-manifestMessages
	require.NotNil(t, first.ManifestFrame)
	require.Equal(t, []byte("first"), (<-acquisitionEvents).Payload)
	require.True(t, peer.bootstrapManifestPending.Load())
	require.Zero(t, bootstrapReady.Load())

	select {
	case second := <-manifestMessages:
		_ = second.ManifestFrame.Close()
		t.Fatal("second oversized manifest arrived before the first completed")
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, first.ManifestFrame.Close())
	second := <-manifestMessages
	require.NotNil(t, second.ManifestFrame)
	require.Equal(t, []byte("second"), (<-acquisitionEvents).Payload)
	require.ErrorIs(t, <-done, io.EOF)
	require.NoError(t, second.ManifestFrame.Close())
	require.Zero(t, bootstrapReady.Load())
}

func TestManifestFrameCleanupIsPrivateExactAndIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	spoolDir, err := prepareManifestSpoolDir(dataDir)
	require.NoError(t, err)
	info, err := os.Stat(spoolDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	stalePath := filepath.Join(spoolDir, "goxrpl-manifests-stale")
	keepPath := filepath.Join(spoolDir, "keep")
	require.NoError(t, os.WriteFile(stalePath, []byte("stale"), 0o600))
	require.NoError(t, os.WriteFile(keepPath, []byte("keep"), 0o600))
	_, err = prepareManifestSpoolDir(dataDir)
	require.NoError(t, err)
	_, err = os.Stat(stalePath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(keepPath)
	require.NoError(t, err)

	header := MessageHeader{
		PayloadSize:      8,
		MessageType:      TypeManifests,
		UncompressedSize: 8,
	}
	_, err = spoolManifestFrame(bytes.NewReader([]byte("short")), header, nil, spoolDir)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "keep", entries[0].Name())

	payload := []byte("manifest")
	header.PayloadSize = uint32(len(payload))
	header.UncompressedSize = uint32(len(payload))
	budget := newReadBudget(int64(len(payload)))
	frame, err := spoolManifestFrame(bytes.NewReader(payload), header, budget, spoolDir)
	require.NoError(t, err)
	fileInfo, err := os.Stat(frame.path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())

	got, err := frame.Materialize(context.Background())
	require.NoError(t, err)
	require.Equal(t, payload, got)
	budget.mu.Lock()
	require.Equal(t, int64(len(payload)), budget.used)
	budget.mu.Unlock()

	require.NoError(t, frame.Close())
	require.NoError(t, frame.Close())
	require.ErrorIs(t, func() error {
		_, err := frame.Materialize(context.Background())
		return err
	}(), ErrManifestFrameClosed)
	require.ErrorIs(t, func() error {
		_, err := os.Stat(frame.path)
		return err
	}(), os.ErrNotExist)
	select {
	case <-frame.completion():
	default:
		t.Fatal("Close did not notify peer completion")
	}
	budget.mu.Lock()
	require.Zero(t, budget.used)
	budget.mu.Unlock()
}

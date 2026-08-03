package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func readBudgetUsed(budget *readBudget) int64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

func TestInboundBulkBudgetFollowsMessageThroughConsumer(t *testing.T) {
	payload := protowire.AppendTag(nil, 100, protowire.BytesType)
	payload = protowire.AppendBytes(payload, bytes.Repeat([]byte{0x4c}, 32*1024))
	frame, err := message.BuildWireMessage(message.TypeLedgerData, payload)
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	events := make(chan Event, 2)
	first := newLatencyTestPeer(t)
	first.bufReader = bufio.NewReader(bytes.NewReader(frame))
	first.SetAcquisitionEvents(events)
	first.SetInboundReadBudget(budget)
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.readLoop(context.Background()) }()

	firstEvent := <-events
	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))

	secondReader := &countingReader{reader: bytes.NewReader(frame)}
	second := newLatencyTestPeer(t)
	second.bufReader = bufio.NewReader(secondReader)
	second.SetAcquisitionEvents(events)
	second.SetInboundReadBudget(budget)
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.readLoop(context.Background()) }()

	require.Eventually(t, func() bool { return secondReader.read.Load() > 0 }, time.Second, time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	require.Less(t, secondReader.read.Load(), int64(len(frame)))

	overlay := &Overlay{
		cfg:        DefaultConfig(),
		ledgerData: make(chan *InboundMessage, 1),
		stopCh:     make(chan struct{}),
	}
	overlay.onMessageReceived(firstEvent)
	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))
	firstMessage := <-overlay.ledgerData
	require.NoError(t, firstMessage.Close())

	require.Eventually(t, func() bool { return secondReader.read.Load() == int64(len(frame)) }, time.Second, time.Millisecond)
	secondEvent := <-events
	secondEvent.release()
	require.ErrorIs(t, <-firstDone, io.EOF)
	require.ErrorIs(t, <-secondDone, io.EOF)
	require.Zero(t, readBudgetUsed(budget))
}

func TestCompressedManifestSpoolSharesInboundBudget(t *testing.T) {
	payload := bytes.Repeat([]byte("manifest"), manifestSpoolThreshold/len("manifest")+1)
	frame, err := message.BuildWireMessage(message.TypeManifests, payload)
	require.NoError(t, err)
	wire, compressed := message.CompressFrameIfWorthwhile(frame)
	require.True(t, compressed)
	header, err := message.DecodeHeader(wire)
	require.NoError(t, err)

	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)
	budget := newReadBudget(int64(3 * len(payload)))
	manifests := make(chan *InboundMessage, 1)
	peer := newLatencyTestPeer(t)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire))
	peer.handshakeCfg.EnableCompression = true
	peer.capabilities = NewPeerCapabilities()
	peer.capabilities.Features.Enable(FeatureCompression)
	peer.SetManifestMessages(manifests)
	peer.SetInboundReadBudget(budget)
	peer.SetManifestSpoolDir(spoolDir)
	done := make(chan error, 1)
	go func() { done <- peer.readLoop(context.Background()) }()

	inbound := <-manifests
	require.NotNil(t, inbound.ManifestFrame)
	require.Equal(t, 2*int64(header.PayloadSize)+int64(len(payload)), readBudgetUsed(budget))
	spoolPath := inbound.ManifestFrame.path
	materialized, err := inbound.ManifestFrame.Materialize(context.Background())
	require.NoError(t, err)
	require.Equal(t, payload, materialized)
	require.Equal(t, int64(header.PayloadSize)+int64(len(payload)), readBudgetUsed(budget))
	require.NoError(t, inbound.Close())
	require.ErrorIs(t, func() error {
		_, err := os.Stat(spoolPath)
		return err
	}(), os.ErrNotExist)
	require.Zero(t, readBudgetUsed(budget))
	require.ErrorIs(t, <-done, io.EOF)
}

func TestInboundRetainedBytesValidation(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, DefaultInboundRetainedBytes, cfg.InboundRetainedBytes)

	cfg.InboundRetainedBytes = 0
	require.NoError(t, cfg.Validate())
	require.Equal(t, DefaultInboundRetainedBytes, cfg.InboundRetainedBytes)

	cfg = DefaultConfig()
	cfg.InboundRetainedBytes = 3*int64(message.MaxMessageSize) - 1
	err := cfg.Validate()
	require.EqualError(t, err, "InboundRetainedBytes must be at least 201326592")
}

func TestRetainedEventReservationReleasesAfterLastOwner(t *testing.T) {
	budget := newReadBudget(1024)
	require.NoError(t, budget.acquire(context.Background(), nil, 512))
	event := Event{reservation: newInboundReservation(budget, 512)}
	retained := event.retainedInboundMessage()

	event.release()
	require.Equal(t, int64(512), readBudgetUsed(budget))

	require.NoError(t, retained.Close())
	require.Zero(t, readBudgetUsed(budget))
}

func TestGetObjectsReplyRetainsBudgetUntilRouterConsumption(t *testing.T) {
	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType: message.ObjectTypeFetchPack,
		Query:   false,
		Objects: []message.IndexedObject{{Hash: bytes.Repeat([]byte{0x01}, 32), Data: []byte{0x09}}},
	})
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	overlay := &Overlay{
		cfg:        DefaultConfig(),
		ledgerData: make(chan *InboundMessage, 1),
		stopCh:     make(chan struct{}),
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      1,
		MessageType: message.TypeGetObjects,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})

	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))
	inbound := <-overlay.ledgerData
	require.NoError(t, inbound.Close())
	require.Zero(t, readBudgetUsed(budget))
}

func TestGetObjectsQueryRetainsBudgetUntilServeCompletion(t *testing.T) {
	payload, err := message.Encode(&message.GetObjectByHash{
		Query:   true,
		Objects: []message.IndexedObject{{Hash: bytes.Repeat([]byte{0x02}, 32)}},
	})
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := newServeScheduler(ctx, 1, 1, 1, 1)
	peer := newLatencyTestPeer(t)
	overlay := &Overlay{
		cfg:            DefaultConfig(),
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: scheduler,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: message.TypeGetObjects,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})

	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))
	task, ok := scheduler.take(ctx)
	require.True(t, ok)
	task.run(task.ctx)
	scheduler.finish(task)
	require.Zero(t, readBudgetUsed(budget))
}

func TestDroppedGetObjectsQueryReleasesBudget(t *testing.T) {
	payload, err := message.Encode(&message.GetObjectByHash{Query: true})
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := newServeScheduler(ctx, 1, 1, 1, 1)
	peer := newLatencyTestPeer(t)
	require.True(t, scheduler.Submit(ctx, peer.ID(), func(context.Context) {}))
	overlay := &Overlay{
		cfg:            DefaultConfig(),
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: scheduler,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: message.TypeGetObjects,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})

	require.Zero(t, readBudgetUsed(budget))
	require.Equal(t, uint64(1), overlay.DroppedServeJobs())
}

func TestServeShutdownReleasesQueuedDecodedBudget(t *testing.T) {
	payload, err := message.Encode(&message.GetObjectByHash{Query: true})
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := newServeScheduler(ctx, 1, 1, 1, 1)
	peer := newLatencyTestPeer(t)
	overlay := &Overlay{
		cfg:            DefaultConfig(),
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: scheduler,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: message.TypeGetObjects,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})
	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))

	scheduler.close()
	require.Zero(t, readBudgetUsed(budget))
}

func TestServePeerCancellationReleasesQueuedDecodedBudget(t *testing.T) {
	payload, err := message.Encode(&message.GetObjectByHash{Query: true})
	require.NoError(t, err)

	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := newServeScheduler(ctx, 1, 1, 1, 1)
	peer := newLatencyTestPeer(t)
	overlay := &Overlay{
		cfg:            DefaultConfig(),
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: scheduler,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: message.TypeGetObjects,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})
	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))

	scheduler.CancelPeer(peer.ID())
	require.Zero(t, readBudgetUsed(budget))
}

func TestReplayAndProofRequestsRetainBudgetUntilServeCompletion(t *testing.T) {
	tests := []struct {
		name    string
		msgType message.MessageType
		request message.Message
	}{
		{
			name:    "replay delta",
			msgType: message.TypeReplayDeltaReq,
			request: &message.ReplayDeltaRequest{LedgerHash: bytes.Repeat([]byte{0x01}, 32)},
		},
		{
			name:    "proof path",
			msgType: message.TypeProofPathReq,
			request: &message.ProofPathRequest{
				Key:        bytes.Repeat([]byte{0x02}, 32),
				LedgerHash: bytes.Repeat([]byte{0x03}, 32),
				MapType:    message.LedgerMapAccountState,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := message.Encode(tt.request)
			require.NoError(t, err)
			budget := newReadBudget(int64(len(payload)))
			require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scheduler := newServeScheduler(ctx, 1, 1, 1, 1)
			peer := newLatencyTestPeer(t)
			capabilities := NewPeerCapabilities()
			capabilities.Features.Enable(FeatureLedgerReplay)
			peer.capabilities = capabilities
			cfg := DefaultConfig()
			cfg.EnableLedgerReplay = true
			overlay := &Overlay{
				cfg:            cfg,
				peers:          map[PeerID]*Peer{peer.ID(): peer},
				serveScheduler: scheduler,
				ctx:            ctx,
				lifecycleState: overlayLifecycleRunning,
				ledgerSync:     NewLedgerSyncHandler(nil),
			}
			overlay.onMessageReceived(Event{
				Type:        EventMessageReceived,
				PeerID:      peer.ID(),
				MessageType: tt.msgType,
				Payload:     payload,
				reservation: newInboundReservation(budget, int64(len(payload))),
			})
			require.Equal(t, int64(len(payload)), readBudgetUsed(budget))

			task, ok := scheduler.take(ctx)
			require.True(t, ok)
			task.run(task.ctx)
			scheduler.finish(task)
			require.Zero(t, readBudgetUsed(budget))
		})
	}
}

func TestStopDrainsRetainedEventsQueuedAfterRunCompletion(t *testing.T) {
	o, err := New(WithDataDir(t.TempDir()))
	require.NoError(t, err)

	budget := newReadBudget(1)
	require.NoError(t, budget.acquire(context.Background(), nil, 1))
	o.events <- Event{reservation: newInboundReservation(budget, 1)}

	runComplete := make(chan struct{})
	close(runComplete)
	o.lifecycleMu.Lock()
	o.runComplete = runComplete
	o.lifecycleMu.Unlock()

	require.NoError(t, o.Stop())
	require.Zero(t, readBudgetUsed(budget))
}

func TestTransactionsBatchRetainsBudgetUntilAllChildrenClose(t *testing.T) {
	identity, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, identity, nil)
	capabilities := NewPeerCapabilities()
	capabilities.Features.Enable(FeatureTxReduceRelay)
	peer.capabilities = capabilities

	payload, err := message.Encode(&message.Transactions{Transactions: []message.Transaction{
		{RawTransaction: []byte{0x12, 0x00, 0x01}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x02}, Status: message.TxStatusCurrent},
	}})
	require.NoError(t, err)
	budget := newReadBudget(int64(len(payload)))
	require.NoError(t, budget.acquire(context.Background(), nil, int64(len(payload))))
	overlay := &Overlay{
		cfg:        Config{EnableTxReduceRelay: true},
		peers:      map[PeerID]*Peer{peer.ID(): peer},
		txMessages: make(chan *InboundMessage, 2),
	}
	overlay.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
		reservation: newInboundReservation(budget, int64(len(payload))),
	})

	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))
	first := <-overlay.txMessages
	second := <-overlay.txMessages
	require.NoError(t, first.Close())
	require.Equal(t, int64(len(payload)), readBudgetUsed(budget))
	require.NoError(t, second.Close())
	require.Zero(t, readBudgetUsed(budget))
}

func TestOverlayShutdownReleasesQueuedManifestSpool(t *testing.T) {
	payload := bytes.Repeat([]byte{0x4d}, manifestSpoolThreshold+1)
	wire, err := message.BuildWireMessage(message.TypeManifests, payload)
	require.NoError(t, err)

	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)
	budget := newReadBudget(int64(2 * len(payload)))
	manifests := make(chan *InboundMessage, 1)
	peer := newLatencyTestPeer(t)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire))
	peer.SetManifestMessages(manifests)
	peer.SetInboundReadBudget(budget)
	peer.SetManifestSpoolDir(spoolDir)
	done := make(chan error, 1)
	go func() { done <- peer.readLoop(context.Background()) }()

	inbound := <-manifests
	spoolPath := inbound.ManifestFrame.path
	require.Equal(t, int64(2*len(payload)), readBudgetUsed(budget))

	overlay := &Overlay{manifestMessages: manifests}
	overlay.manifestMessages <- inbound
	overlay.releaseQueuedInbound()

	require.Zero(t, readBudgetUsed(budget))
	_, err = os.Stat(spoolPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, <-done, io.EOF)
}

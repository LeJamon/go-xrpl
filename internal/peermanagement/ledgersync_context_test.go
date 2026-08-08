package peermanagement

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

type contextLedgerProvider struct {
	canceled bool
	started  chan struct{}
}

func (p *contextLedgerProvider) GetReplayDelta([]byte) ([]byte, [][]byte, error) {
	return []byte("header"), [][]byte{[]byte("tx")}, nil
}

func (p *contextLedgerProvider) GetProofPath([]byte, []byte, message.LedgerMapType) ([]byte, [][]byte, error) {
	return []byte("header"), [][]byte{[]byte("path")}, nil
}

func (p *contextLedgerProvider) MakeFetchPack([32]byte, int) ([]message.IndexedObject, error) {
	return nil, nil
}

func (p *contextLedgerProvider) GetReplayDeltaContext(ctx context.Context, _ []byte) ([]byte, [][]byte, error) {
	if p.started != nil {
		close(p.started)
	}
	<-ctx.Done()
	p.canceled = true
	return nil, nil, ctx.Err()
}

func (p *contextLedgerProvider) GetProofPathContext(ctx context.Context, _ []byte, _ []byte, _ message.LedgerMapType) ([]byte, [][]byte, error) {
	<-ctx.Done()
	p.canceled = true
	return nil, nil, ctx.Err()
}

func TestLedgerSyncContextProviderCancellation(t *testing.T) {
	events := make(chan Event, 1)
	provider := &contextLedgerProvider{started: make(chan struct{})}
	h := NewLedgerSyncHandler(events)
	h.SetProvider(provider)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.HandleMessage(ctx, 1, &message.ReplayDeltaRequest{LedgerHash: make([]byte, 32)})
	}()
	<-provider.started
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) || !provider.canceled {
		t.Fatalf("HandleMessage error/provider cancellation = %v/%v, want context canceled/true", err, provider.canceled)
	}
	if len(events) != 0 {
		t.Fatal("canceled request emitted a response")
	}
}

func TestLedgerSyncChargesMalformedAndUnavailable(t *testing.T) {
	events := make(chan Event, 4)
	h := NewLedgerSyncHandler(events)
	var charges []resource.Charge
	h.SetChargePeer(func(_ PeerID, fee resource.Charge, _ string) { charges = append(charges, fee) })
	if err := h.HandleMessage(context.Background(), 1, &message.ReplayDeltaRequest{LedgerHash: []byte{1}}); !errors.Is(err, ErrPeerBadRequest) {
		t.Fatalf("malformed request error = %v, want ErrPeerBadRequest", err)
	}
	if len(charges) != 1 || charges[0] != resource.FeeMalformedRequest() {
		t.Fatalf("malformed charges = %#v, want FeeMalformedRequest", charges)
	}
	charges = nil
	if err := h.HandleMessage(context.Background(), 1, &message.ReplayDeltaRequest{LedgerHash: make([]byte, 32)}); err != nil {
		t.Fatalf("unavailable provider error = %v", err)
	}
	if len(charges) != 1 || charges[0] != resource.FeeRequestNoReply() {
		t.Fatalf("unavailable charges = %#v, want FeeRequestNoReply", charges)
	}
}

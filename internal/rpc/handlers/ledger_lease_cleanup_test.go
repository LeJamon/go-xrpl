package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

type leaseTrackingReader struct {
	types.LedgerReader
	sequence uint32
	closes   atomic.Int32
}

func (r *leaseTrackingReader) Sequence() uint32 { return r.sequence }

func (r *leaseTrackingReader) Hash() [32]byte {
	var hash [32]byte
	hash[0] = 1
	return hash
}

func (r *leaseTrackingReader) ParentHash() [32]byte { return [32]byte{} }

func (r *leaseTrackingReader) IsClosed() bool { return true }

func (r *leaseTrackingReader) IsValidated() bool { return true }

func (r *leaseTrackingReader) TotalDrops() uint64 { return 0 }

func (r *leaseTrackingReader) CloseTime() int64 { return 0 }

func (r *leaseTrackingReader) CloseTimeResolution() uint32 { return 0 }

func (r *leaseTrackingReader) CloseFlags() uint8 { return 0 }

func (r *leaseTrackingReader) ParentCloseTime() int64 { return 0 }

func (r *leaseTrackingReader) TxMapHash() [32]byte { return [32]byte{} }

func (r *leaseTrackingReader) StateMapHash() [32]byte { return [32]byte{} }

func (r *leaseTrackingReader) ForEachTransaction(func([32]byte, []byte) bool) error { return nil }

func (r *leaseTrackingReader) Close() error {
	r.closes.Add(1)
	return nil
}

type contextHashLedgerService struct {
	types.LedgerService
	reader types.LedgerReader
	err    error
}

func (s *contextHashLedgerService) GetLedgerByHashContext(context.Context, [32]byte) (types.LedgerReader, error) {
	return s.reader, s.err
}

func leaseTestContext(svc types.LedgerService) *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: svc}),
	}
}

func ledgerHashParams(t *testing.T) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]string{"ledger_hash": strings.Repeat("AB", 32)})
	require.NoError(t, err)
	return params
}

func TestLedgerRequestClosesContextHashLeaseOnSuccess(t *testing.T) {
	reader := &leaseTrackingReader{sequence: 7}
	service := &contextHashLedgerService{reader: reader}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(leaseTestContext(service), ledgerHashParams(t))

	require.Nil(t, rpcErr)
	require.NotNil(t, result)
	require.Equal(t, int32(1), reader.closes.Load())
}

func TestLedgerRequestClosesContextHashLeaseOnError(t *testing.T) {
	reader := &leaseTrackingReader{sequence: 7}
	service := &contextHashLedgerService{reader: reader, err: errors.New("lookup failed")}

	result, rpcErr := (&handlers.LedgerRequestMethod{}).Handle(leaseTestContext(service), ledgerHashParams(t))

	require.Nil(t, result)
	require.Error(t, rpcErr)
	require.Equal(t, int32(1), reader.closes.Load())
}

func TestCanDeleteClosesContextHashLeaseOnSuccess(t *testing.T) {
	reader := &leaseTrackingReader{sequence: 314159}
	service := &contextHashLedgerService{reader: reader}
	container := &types.ServiceContainer{
		AdvisoryDeleteState: &fakeAdvisory{enabled: true},
		Ledger:              service,
	}

	result, rpcErr := runCanDelete(t, container, canDeleteParams(t, strings.Repeat("AB", 32)))

	require.Nil(t, rpcErr)
	require.Equal(t, float64(314159), result["can_delete"])
	require.Equal(t, int32(1), reader.closes.Load())
}

func TestCanDeleteClosesContextHashLeaseOnError(t *testing.T) {
	reader := &leaseTrackingReader{sequence: 314159}
	service := &contextHashLedgerService{reader: reader, err: errors.New("lookup failed")}
	container := &types.ServiceContainer{
		AdvisoryDeleteState: &fakeAdvisory{enabled: true},
		Ledger:              service,
	}

	result, rpcErr := runCanDelete(t, container, canDeleteParams(t, strings.Repeat("AB", 32)))

	require.Nil(t, result)
	require.Error(t, rpcErr)
	require.Equal(t, int32(1), reader.closes.Load())
}

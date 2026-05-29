package handlers_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdvisory is a test double for the advisory-delete state subsystem.
type fakeAdvisory struct {
	enabled     bool
	canDelete   uint32
	lastRotated uint32
}

func (f *fakeAdvisory) AdvisoryDelete() bool { return f.enabled }
func (f *fakeAdvisory) GetCanDelete() uint32 { return f.canDelete }
func (f *fakeAdvisory) SetCanDelete(seq uint32) (uint32, error) {
	if f.enabled {
		f.canDelete = seq
	}
	return f.canDelete, nil
}
func (f *fakeAdvisory) GetLastRotated() uint32 { return f.lastRotated }

// stubCanDeleteLedger satisfies types.LedgerService by embedding the
// interface; only GetLedgerByHash is exercised by the hash-resolution path.
type stubCanDeleteLedger struct {
	types.LedgerService
	seq uint32
	err error
}

func (s stubCanDeleteLedger) GetLedgerByHash([32]byte) (types.LedgerReader, error) {
	if s.err != nil {
		return nil, s.err
	}
	return stubLedgerReader{seq: s.seq}, nil
}

type stubLedgerReader struct {
	types.LedgerReader
	seq uint32
}

func (r stubLedgerReader) Sequence() uint32 { return r.seq }

func canDeleteParams(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"can_delete": value})
	require.NoError(t, err)
	return raw
}

func runCanDelete(t *testing.T, svc *types.ServiceContainer, params json.RawMessage) (map[string]interface{}, *types.RpcError) {
	t.Helper()
	method := &handlers.CanDeleteMethod{}
	result, rpcErr := method.Handle(adminCtx(svc), params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return decodeResponse(t, result), nil
}

func TestCanDelete_NotEnabled(t *testing.T) {
	// No advisory-delete subsystem wired.
	_, rpcErr := runCanDelete(t, &types.ServiceContainer{}, nil)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)

	// Subsystem present but advisory delete disabled.
	svc := &types.ServiceContainer{AdvisoryDeleteState: &fakeAdvisory{enabled: false}}
	_, rpcErr = runCanDelete(t, svc, nil)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
}

func TestCanDelete_Get(t *testing.T) {
	svc := &types.ServiceContainer{AdvisoryDeleteState: &fakeAdvisory{enabled: true, canDelete: 42}}
	resp, rpcErr := runCanDelete(t, svc, nil)
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(42), resp["can_delete"])
}

func TestCanDelete_SetNumber(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, 12345))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(12345), resp["can_delete"])
	assert.Equal(t, uint32(12345), store.canDelete)
}

func TestCanDelete_SetNumericString(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "678"))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(678), resp["can_delete"])
}

func TestCanDelete_Never(t *testing.T) {
	store := &fakeAdvisory{enabled: true, canDelete: 999}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "never"))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(0), resp["can_delete"])
}

func TestCanDelete_Always(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "always"))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(^uint32(0)), resp["can_delete"])
}

func TestCanDelete_NowNotReady(t *testing.T) {
	store := &fakeAdvisory{enabled: true, lastRotated: 0}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	_, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "now"))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_READY, rpcErr.Code)
}

func TestCanDelete_Now(t *testing.T) {
	store := &fakeAdvisory{enabled: true, lastRotated: 5000}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "now"))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(5000), resp["can_delete"])
}

func TestCanDelete_ByHash(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{
		AdvisoryDeleteState: store,
		Ledger:              stubCanDeleteLedger{seq: 314159},
	}
	hash := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	resp, rpcErr := runCanDelete(t, svc, canDeleteParams(t, hash))
	require.Nil(t, rpcErr)
	assert.Equal(t, float64(314159), resp["can_delete"])
}

func TestCanDelete_HashNotFound(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{
		AdvisoryDeleteState: store,
		Ledger:              stubCanDeleteLedger{err: errors.New("not found")},
	}
	hash := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	_, rpcErr := runCanDelete(t, svc, canDeleteParams(t, hash))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
}

func TestCanDelete_InvalidString(t *testing.T) {
	store := &fakeAdvisory{enabled: true}
	svc := &types.ServiceContainer{AdvisoryDeleteState: store}
	_, rpcErr := runCanDelete(t, svc, canDeleteParams(t, "garbage"))
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
}

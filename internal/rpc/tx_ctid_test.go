package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureCTIDMockPublishedRange(mock *mockLedgerService, first, last uint32) {
	mock.validatedLedgerIndex = last
	mock.serverInfo.HaveValidated = true
	mock.serverInfo.ValidatedLedgerSeq = last
	mock.serverInfo.HavePublished = true
	mock.serverInfo.PublishedLedgerSeq = last
	mock.serverInfo.HaveValidatedRange = true
	mock.serverInfo.ValidatedRangeMin = first
	mock.serverInfo.ValidatedRangeMax = last
	mock.serverInfo.CompleteLedgers = fmt.Sprintf("%d-%d", first, last)
}

func newCTIDTestReader(t *testing.T, sequence uint32, closed, validated bool) *mockLedgerReader {
	t.Helper()

	reader := newDefaultLedgerReader(sequence, validated)
	reader.closed = closed
	reader.validated = validated
	data, err := json.Marshal(handlers.StoredTransaction{
		TxJSON: validStoredPaymentTransaction(),
		Meta:   validStoredMetadata(),
	})
	require.NoError(t, err)
	reader.transactions = append(reader.transactions, struct {
		hash [32]byte
		data []byte
	}{hash: [32]byte{1}, data: data})
	return reader
}

func newCTIDTestContext(
	t *testing.T,
	first, published uint32,
	reader types.LedgerReader,
) (*types.RpcContext, *ledgerMock, *int) {
	t.Helper()

	base := newMockLedgerService()
	configureCTIDMockPublishedRange(base, first, published)
	service := &ledgerMock{mockLedgerService: base}
	lookupCalls := 0
	service.getLedgerBySequenceFn = func(sequence uint32) (types.LedgerReader, error) {
		lookupCalls++
		if reader != nil && sequence == reader.Sequence() && sequence >= first {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}
	return ctx, service, &lookupCalls
}

func TestTxMethodCTIDValidatedLedgerJSONAndBinary(t *testing.T) {
	const ledgerSequence = 100
	reader := newCTIDTestReader(t, ledgerSequence, true, true)
	ctx, _, _ := newCTIDTestContext(t, 1, ledgerSequence, reader)
	ctid, ok := handlers.EncodeCTID(ledgerSequence, 0, 0)
	require.True(t, ok)

	for _, tc := range []struct {
		name   string
		binary bool
	}{
		{name: "json"},
		{name: "binary", binary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{
				"ctid":   ctid,
				"binary": tc.binary,
			})
			require.NoError(t, err)

			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
			require.Nil(t, rpcErr)
			response, ok := result.(map[string]any)
			require.True(t, ok)
			assert.NotEmpty(t, response["hash"])
			if tc.binary {
				assert.Contains(t, response, "tx")
				assert.Contains(t, response, "meta")
			} else {
				assert.EqualValues(t, ledgerSequence, response["ledger_index"])
				assert.Contains(t, response, "meta")
			}
		})
	}
}

func TestTxMethodCTIDRejectsOpenUnvalidatedAndOutOfRangeLedgers(t *testing.T) {
	tests := []struct {
		name        string
		first       uint32
		published   uint32
		requested   uint32
		reader      *mockLedgerReader
		wantLookups int
	}{
		{
			name:        "current open ledger above published frontier",
			first:       1,
			published:   100,
			requested:   101,
			reader:      newCTIDTestReader(t, 101, false, false),
			wantLookups: 0,
		},
		{
			name:        "open reader at published frontier",
			first:       1,
			published:   101,
			requested:   101,
			reader:      newCTIDTestReader(t, 101, false, false),
			wantLookups: 1,
		},
		{
			name:        "unvalidated closed ledger",
			first:       1,
			published:   100,
			requested:   100,
			reader:      newCTIDTestReader(t, 100, true, false),
			wantLookups: 1,
		},
		{
			name:        "sequence above published frontier",
			first:       1,
			published:   100,
			requested:   101,
			reader:      newCTIDTestReader(t, 101, true, true),
			wantLookups: 0,
		},
		{
			name:        "sequence below retained available range",
			first:       100,
			published:   100,
			requested:   99,
			reader:      newCTIDTestReader(t, 99, true, true),
			wantLookups: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, lookupCalls := newCTIDTestContext(t, tc.first, tc.published, tc.reader)
			ctid, ok := handlers.EncodeCTID(tc.requested, 0, 0)
			require.True(t, ok)
			params, err := json.Marshal(map[string]any{"ctid": ctid})
			require.NoError(t, err)

			result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcTXN_NOT_FOUND, rpcErr.Code)
			assert.Equal(t, tc.wantLookups, *lookupCalls)
		})
	}
}

func TestTxMethodCTIDRejectsWithoutPublishedFrontier(t *testing.T) {
	const ledgerSequence = 100
	reader := newCTIDTestReader(t, ledgerSequence, true, true)
	ctx, service, lookupCalls := newCTIDTestContext(t, 1, ledgerSequence, reader)
	service.serverInfo.HavePublished = false
	service.serverInfo.HaveValidatedRange = false
	ctid, ok := handlers.EncodeCTID(ledgerSequence, 0, 0)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{"ctid": ctid})
	require.NoError(t, err)

	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcTXN_NOT_FOUND, rpcErr.Code)
	assert.Zero(t, *lookupCalls)
}

func TestTxMethodCTIDTracksAvailableRangeAdvancement(t *testing.T) {
	const ledgerSequence = 100
	reader := newCTIDTestReader(t, ledgerSequence, true, true)
	ctx, service, lookupCalls := newCTIDTestContext(t, 1, ledgerSequence, reader)
	ctid, ok := handlers.EncodeCTID(ledgerSequence, 0, 0)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{"ctid": ctid})
	require.NoError(t, err)

	service.serverInfo.ValidatedRangeMax = ledgerSequence - 1
	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcTXN_NOT_FOUND, rpcErr.Code)
	assert.Zero(t, *lookupCalls)

	service.serverInfo.ValidatedRangeMax = ledgerSequence
	result, rpcErr = (&handlers.TxMethod{}).Handle(ctx, params)
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, response["hash"])
	assert.Equal(t, 1, *lookupCalls)
}

func TestTxMethodCTIDNetworkErrorPrecedesValidatedRangeLookup(t *testing.T) {
	ctx, service, lookupCalls := newCTIDTestContext(t, 1, 0, nil)
	service.serverInfo.NetworkID = 2
	ctid, ok := handlers.EncodeCTID(100, 0, 3)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{"ctid": ctid})
	require.NoError(t, err)

	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcWRONG_NETWORK, rpcErr.Code)
	assert.Zero(t, *lookupCalls)
}

func TestTxMethodCTIDRangeErrorPrecedesValidatedRangeLookup(t *testing.T) {
	ctx, _, lookupCalls := newCTIDTestContext(t, 1, 0, nil)
	ctid, ok := handlers.EncodeCTID(100, 0, 0)
	require.True(t, ok)
	params, err := json.Marshal(map[string]any{
		"ctid":       ctid,
		"min_ledger": 100,
		"max_ledger": 99,
	})
	require.NoError(t, err)

	result, rpcErr := (&handlers.TxMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_LGR_RANGE, rpcErr.Code)
	assert.Zero(t, *lookupCalls)
}

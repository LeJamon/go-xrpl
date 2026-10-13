package handlers

import (
	"context"
	"errors"
	"testing"

	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

type exactLedgerViewReader struct {
	types.LedgerReader
	types.LedgerStateView
	sequence  uint32
	hash      [32]byte
	closed    bool
	validated bool
	closes    int
}

func (r *exactLedgerViewReader) Sequence() uint32  { return r.sequence }
func (r *exactLedgerViewReader) Hash() [32]byte    { return r.hash }
func (r *exactLedgerViewReader) IsClosed() bool    { return r.closed }
func (r *exactLedgerViewReader) IsValidated() bool { return r.validated }
func (r *exactLedgerViewReader) Close() error {
	r.closes++
	return nil
}

var _ types.LedgerReader = (*exactLedgerViewReader)(nil)
var _ types.LedgerStateView = (*exactLedgerViewReader)(nil)

type countingStateViewService struct {
	types.LedgerService
	replacement types.LedgerStateView
	seqCalls    int
	hashCalls   int
}

func (s *countingStateViewService) GetLedgerViewBySeq(uint32) (types.LedgerStateView, types.LedgerReader, error) {
	s.seqCalls++
	return s.replacement, nil, nil
}

func (s *countingStateViewService) GetLedgerViewByHash([32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	s.hashCalls++
	return s.replacement, nil, nil
}

func TestResolvedLedgerStateViewReusesCapturedViewReader(t *testing.T) {
	captured := &exactLedgerViewReader{sequence: 10, hash: [32]byte{10}}
	service := &countingStateViewService{
		replacement: &exactLedgerViewReader{sequence: 11, hash: [32]byte{11}},
	}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}

	view, err := resolvedLedgerStateView(ctx, captured)

	require.NoError(t, err)
	require.Same(t, captured, view)
	require.Zero(t, service.seqCalls)
	require.Zero(t, service.hashCalls)
}

type failingLedgerLookupService struct {
	types.LedgerService
	reader types.LedgerReader
	err    error
}

func (s *failingLedgerLookupService) GetLedgerBySequence(uint32) (types.LedgerReader, error) {
	return s.reader, s.err
}

func (s *failingLedgerLookupService) GetLedgerByHash([32]byte) (types.LedgerReader, error) {
	return s.reader, s.err
}

func TestResolveLedgerSelectionClosesReaderOnLookupError(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection ledgerselector.Selector
	}{
		{name: "sequence", selection: ledgerselector.FromSequence(10)},
		{name: "hash", selection: ledgerselector.FromHash([32]byte{10})},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &exactLedgerViewReader{sequence: 10, hash: [32]byte{10}}
			service := &failingLedgerLookupService{
				reader: reader,
				err:    errors.New("lookup failed"),
			}
			ctx := &types.RpcContext{
				Context:    context.Background(),
				ApiVersion: types.ApiVersion2,
				Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
			}

			_, rpcErr := resolveLedgerSelection(ctx, test.selection)

			require.NotNil(t, rpcErr)
			require.Equal(t, 1, reader.closes)
		})
	}
}

package handlers

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestLedgerMapHashesPropagatesAdapterFailure(t *testing.T) {
	wantErr := errors.New("state map read failed")
	reader := failingLedgerReader{err: wantErr}
	_, _, err := ledgerMapHashes(reader)
	require.ErrorIs(t, err, wantErr)
}

func TestLedgerAmendmentRulesPropagatesAdapterFailure(t *testing.T) {
	wantErr := errors.New("amendment rules read failed")
	reader := failingLedgerReader{rulesErr: wantErr}
	_, err := ledgerAmendmentRules(reader)
	require.ErrorIs(t, err, wantErr)
}

type failingLedgerReader struct {
	types.LedgerReader
	err      error
	rulesErr error
}

func (r failingLedgerReader) TxMapHashWithError() ([32]byte, error) {
	return [32]byte{}, r.err
}

func (r failingLedgerReader) StateMapHashWithError() ([32]byte, error) {
	return [32]byte{}, r.err
}

func (r failingLedgerReader) LedgerAmendmentRulesWithError() (*amendment.Rules, error) {
	return nil, r.rulesErr
}

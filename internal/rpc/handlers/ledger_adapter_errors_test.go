package handlers

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestLedgerAmendmentRulesPropagatesAdapterFailure(t *testing.T) {
	wantErr := errors.New("amendment rules read failed")
	reader := failingLedgerReader{rulesErr: wantErr}
	_, err := ledgerAmendmentRules(reader)
	require.ErrorIs(t, err, wantErr)
}

type failingLedgerReader struct {
	types.LedgerReader
	rulesErr error
}

func (r failingLedgerReader) LedgerAmendmentRulesWithError() (*amendment.Rules, error) {
	return nil, r.rulesErr
}

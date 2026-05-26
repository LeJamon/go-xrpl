package service

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"
)

// TestService_AmendmentTableResync verifies that AcceptLedger folds the
// validated ledger's amendment set into the shared amendment table and that the
// node is not blocked when every enabled amendment is supported.
func TestService_AmendmentTableResync(t *testing.T) {
	tbl := amendment.NewAmendmentTable()
	cfg := DefaultConfig()
	cfg.AmendmentTable = tbl

	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	require.False(t, svc.IsAmendmentBlocked(), "fresh node must not be amendment-blocked")

	closedSeq, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)

	validated := svc.GetClosedLedger()
	require.NotNil(t, validated)

	// Every amendment enabled in the validated ledger must now be reflected in
	// the table — proof the resync copied the SLE into the in-memory table.
	data, err := validated.Read(keylet.Amendments())
	require.NoError(t, err)
	if data != nil {
		sle, perr := pseudo.ParseAmendmentsSLE(data)
		require.NoError(t, perr)
		for _, id := range sle.Amendments {
			require.True(t, tbl.IsEnabled(id), "table must reflect validated-ledger amendment %x", id)
		}
	}

	// DoValidatedLedger ran, so a same-window resync is no longer needed.
	require.False(t, tbl.NeedValidatedLedger(closedSeq),
		"lastUpdateSeq must advance after the resync")

	require.False(t, svc.IsAmendmentBlocked(), "all genesis amendments are supported")
	if _, ok := svc.AmendmentFirstUnsupportedExpected(); ok {
		t.Fatal("no unsupported amendment holds majority")
	}
}

// TestService_NilAmendmentTable verifies the accessors are safe when no table
// is configured.
func TestService_NilAmendmentTable(t *testing.T) {
	var s Service
	require.False(t, s.IsAmendmentBlocked())
	if _, ok := s.AmendmentFirstUnsupportedExpected(); ok {
		t.Fatal("nil table must report no projection")
	}
	require.Nil(t, s.AmendmentTable())
}

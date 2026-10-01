package payment

import (
	"encoding/binary"
	"maps"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestBookForwardAggregateOverflow(t *testing.T) {
	issuer, first, second, taker := [20]byte{1}, [20]byte{2}, [20]byte{3}, [20]byte{4}
	id := keylet.MakeMPTID(1, issuer)
	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureMPTokensV2).Build()
	for _, owner := range [][20]byte{issuer, first, second, taker} {
		view.createAccount(owner, 100_000_000, 2)
	}
	putMPTIssuance(t, view, id, protocol.MaxMPTokenAmount, 0)
	// This intentionally exceeds the issuance's supply. A valid ledger cannot
	// fund a forward output total above MaxInt64 for a single MPT issuance.
	const offered = int64(5_000_000_000_000_000_000)
	putMPTHolding(t, view, id, first, uint64(offered))
	putMPTHolding(t, view, id, second, uint64(offered))
	step := NewBookStep(Issue{Currency: "XRP"}, NewMPTIssue(id), taker, issuer, nil, false)
	directory := step.bookBaseKey()
	quality := QualityFromAmounts(NewXRPEitherAmount(1), NewMPTEitherAmount(offered, id))
	binary.BigEndian.PutUint64(directory[24:], quality.Value)
	firstKey := putMPTBookOffer(t, view, step, first, 1, tx.NewXRPAmount(1), newMPTAmount(offered, id), directory)
	putMPTBookOffer(t, view, step, second, 1, tx.NewXRPAmount(1), newMPTAmount(offered, id), directory)
	before := maps.Clone(view.data)
	base := NewPaymentSandbox(view)
	sb := NewChildSandbox(base)
	removals := make(map[[32]byte]bool)
	failure := recoverFlowError(t, func() {
		step.Fwd(sb, NewChildSandbox(base), removals, NewXRPEitherAmount(2))
	})
	require.Equal(t, ter.TecPATH_DRY, failure.ter)
	require.Empty(t, removals)
	require.Empty(t, step.PermRemovals())
	require.Nil(t, step.cache)
	data, err := sb.Read(keylet.Keylet{Key: firstKey})
	require.NoError(t, err)
	require.Empty(t, data, "the first offer was consumed before the aggregate overflow")
	require.Equal(t, before, view.data, "the failed step's sandbox must remain unapplied")
}

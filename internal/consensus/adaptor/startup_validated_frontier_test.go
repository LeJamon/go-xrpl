package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func TestFreshNonStandaloneValidatedFrontierAdvancesAtQuorum(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a := New(Config{LedgerService: svc})

	require.Equal(t, consensus.LedgerID{}, a.GetValidatedLedgerHash())

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	a.OnLedgerFullyValidated(consensus.LedgerID(closed.Hash()), closed.Sequence())

	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	require.Equal(t, closed.Sequence(), svc.GetValidatedLedgerIndex())
	require.Equal(t, closed.Hash(), validated.Hash())
	require.Equal(t, consensus.LedgerID(closed.Hash()), a.GetValidatedLedgerHash())
}

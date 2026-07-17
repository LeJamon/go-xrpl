package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/stretchr/testify/require"
)

func TestBookStepDeleteOfferIgnoresMissingDirectoryEntry(t *testing.T) {
	t.Parallel()

	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)
	var owner [20]byte
	owner[19] = 1
	offer := &state.LedgerOffer{
		Account:  state.EncodeAccountIDSafe(owner),
		Sequence: 7,
	}

	require.NoError(t, (&BookStep{}).deleteOffer(sandbox, offer, owner))
}

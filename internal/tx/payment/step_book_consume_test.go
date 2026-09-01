package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/stretchr/testify/require"
)

func TestBookStepDeleteOfferRejectsMissingDirectoryEntry(t *testing.T) {
	t.Parallel()

	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)
	var owner [20]byte
	owner[19] = 1
	offer := &state.LedgerOffer{
		Account:  state.EncodeAccountIDSafe(owner),
		Sequence: 7,
	}

	err := (&BookStep{}).deleteOffer(sandbox, offer, owner)
	require.ErrorContains(t, err, "tefBAD_LEDGER: offer removal incomplete")
}

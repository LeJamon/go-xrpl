package escrow

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

// TestEscrowFinish_DeletedDestination_TecNO_DST verifies that finishing an
// escrow whose destination account no longer exists returns tecNO_DST, not
// tefINTERNAL. rippled returns tecNO_DST because escrow cannot fund a new
// account (Escrow.cpp:1105-1108); the destination read returning nil data
// (account deleted after the escrow was created) must map to tecNO_DST rather
// than falling through to a parse of nil data.
func TestEscrowFinish_DeletedDestination_TecNO_DST(t *testing.T) {
	view := newMapView()

	// Account IDs: owner created the escrow, finisher submits the finish, dest
	// is the (now-deleted) recipient. Distinct addresses so this is a
	// cross-account escrow with an sfDestinationNode.
	var ownerID, finisherID, destID [20]byte
	ownerID[0], ownerID[19] = 0x01, 0x11
	finisherID[0], finisherID[19] = 0x02, 0x22
	destID[0], destID[19] = 0x03, 0x33

	ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	require.NoError(t, err)
	finisherAddr, err := addresscodec.EncodeAccountIDToClassicAddress(finisherID[:])
	require.NoError(t, err)
	destAddr, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	require.NoError(t, err)

	const offerSeq = uint32(5)
	const finishAfter = uint32(1000)

	// Build a finishable XRP escrow: FinishAfter in the past, no condition.
	create := &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, ownerAddr),
		Amount:      tx.NewXRPAmount(1_000_000),
		Destination: destAddr,
		FinishAfter: ptrUint32(finishAfter),
	}
	escrowBlob, err := serializeEscrow(create, ownerID, destID, 0,
		0 /*ownerNode*/, 0 /*destNode*/, true /*hasDestNode*/, 0, false)
	require.NoError(t, err)

	escrowKey := keylet.Escrow(ownerID, offerSeq)
	require.NoError(t, view.Insert(escrowKey, escrowBlob))

	// The finisher (transaction submitter) exists; the destination does not.
	finisherAcct := &state.AccountRoot{
		Account:  finisherAddr,
		Balance:  100_000_000,
		Sequence: 1,
	}
	finisherBlob, err := state.SerializeAccountRoot(finisherAcct)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(finisherID), finisherBlob))

	finish := &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, finisherAddr),
		Owner:         ownerAddr,
		OfferSequence: offerSeq,
	}

	ctx := &tx.ApplyContext{
		View:      view,
		Account:   finisherAcct,
		AccountID: finisherID,
		Config: tx.EngineConfig{
			Rules: amendment.AllSupportedRules(),
			// ParentCloseTime is strictly after FinishAfter so the escrow is
			// finishable and execution reaches the destination read.
			ParentCloseTime:  finishAfter + 1,
			ReserveBase:      200_000_000,
			ReserveIncrement: 50_000_000,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}

	result := finish.Apply(ctx)
	require.Equal(t, tx.TecNO_DST, result,
		"finishing an escrow with a deleted destination must be tecNO_DST, not tefINTERNAL")
}

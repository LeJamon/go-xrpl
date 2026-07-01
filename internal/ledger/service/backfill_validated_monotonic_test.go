package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The validated tip is monotonic (rippled LedgerMaster::setFullLedger,
// LedgerMaster.cpp:948). A history backfill of a below-tip skipped ledger
// whose seq still has a stashed pending-validation must mark the ledger
// validated but must NOT rewind s.validatedLedger — the drain used to
// promote unconditionally on any TTL-fresh hash match.
func TestBackfill_BelowTipDrainDoesNotRewindValidated(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashLow, hashTip [32]byte
	hashLow[0] = 0x77
	hashTip[0] = 0x99

	tip := makeStubLedger(t, baseSeq+5, hashTip, [32]byte{0x98})
	_ = tip.SetValidated()

	svc.mu.Lock()
	svc.ledgerHistory[tip.Sequence()] = tip
	svc.closedLedger = tip
	svc.validatedLedger = tip
	// The skipped seq reached quorum while we were behind; its validation
	// was stashed for the eventual adopt.
	svc.pendingLedgerValidations[baseSeq] = pendingValidationEntry{
		expectedHash: hashLow,
		at:           time.Now(),
	}
	svc.pendingLedgerValidationsOrder = append(svc.pendingLedgerValidationsOrder, baseSeq)
	svc.mu.Unlock()

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: baseSeq,
		Hash:        hashLow,
		ParentHash:  [32]byte{0x76},
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	require.NotNil(t, svc.validatedLedger)
	assert.Equal(t, tip.Sequence(), svc.validatedLedger.Sequence(),
		"a below-tip backfill drain must not rewind the validated pointer")
	assert.Equal(t, hashTip, svc.validatedLedger.Hash())

	adopted, ok := svc.ledgerHistory[baseSeq]
	require.True(t, ok, "the backfilled ledger must still be installed")
	assert.True(t, adopted.IsValidated(),
		"the backfilled ledger itself is validated — only the pointer must not move")
	_, stashed := svc.pendingLedgerValidations[baseSeq]
	assert.False(t, stashed, "the stash must be consumed either way")
}

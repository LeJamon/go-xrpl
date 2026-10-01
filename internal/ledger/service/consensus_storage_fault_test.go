package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type consensusMissingNodeFamily struct {
	shamap.Family
	missing [32]byte
}

func (f consensusMissingNodeFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.missing {
		return nil, nil
	}
	return f.Family.Fetch(ctx, hash)
}

func TestLedgerAcceptanceMissingPaymentDestinationBlocksPublication(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone=%t", standalone), func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.svc.StopStateBaseRecertification()
			masterKey := keylet.Account(jtx.MasterAccount().AccountID()).Key
			var destinationName string
			var destinationKey [32]byte
			for i := range 100 {
				destinationName = fmt.Sprintf("storage-fault-destination-%d", i)
				destinationKey = keylet.Account(jtx.NewAccount(destinationName).AccountID()).Key
				if destinationKey[0]>>4 != masterKey[0]>>4 {
					break
				}
			}
			require.NotEqual(t, masterKey[0]>>4, destinationKey[0]>>4)
			root, err := f.db.Fetch(t.Context(), nodestore.Hash256(f.stateRoot))
			require.NoError(t, err)
			reader, err := shamap.DeserializeFromPrefix(root.Data)
			require.NoError(t, err)
			missing, err := reader.(shamap.InnerNodeReader).ChildHash(int(destinationKey[0] >> 4))
			require.NoError(t, err)
			require.NotEqual(t, [32]byte{}, missing)
			family := consensusMissingNodeFamily{Family: f.svc.shamapFamily, missing: missing}
			stateMap, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, f.stateRoot, family)
			require.NoError(t, err)
			txMap, err := f.validated.TxMapSnapshot()
			require.NoError(t, err)
			parent, err := ledger.NewFromHeader(f.validated.Header(), stateMap, txMap, f.validated.Fees())
			require.NoError(t, err)
			f.svc.mu.Lock()
			f.svc.closedLedger = parent
			beforeOpen := f.svc.openLedger
			f.svc.mu.Unlock()
			blob, _ := startupPaymentBlob(t, destinationName, 1)
			if standalone {
				pending, parseErr := openledger.ParsePendingTx(blob)
				require.NoError(t, parseErr)
				f.svc.mu.Lock()
				f.svc.pendingTxs = []openledger.PendingTx{pending}
				f.svc.mu.Unlock()
				_, err = f.svc.AcceptLedger(t.Context())
			} else {
				_, err = f.svc.AcceptConsensusResult(t.Context(), parent, [][]byte{blob}, nil, time.Now(), true)
			}
			require.ErrorIs(t, err, shamap.ErrNodeNotInStore)
			var missingErr *shamap.MissingNodeError
			require.ErrorAs(t, err, &missingErr)
			require.Equal(t, missing, missingErr.Hash)
			require.Same(t, parent, f.svc.GetClosedLedger())
			require.Same(t, f.validated, f.svc.GetValidatedLedger())
			require.Same(t, beforeOpen, f.svc.GetOpenLedger())
			require.True(t, f.svc.ReplayBlocked())
			require.Equal(t, replayfault.MissingState, f.svc.replayFaults.Snapshot().Class)
			require.ErrorIs(t, f.svc.WithValidatorDuty(func() error {
				t.Fatal("validator duty ran after missing execution state")
				return nil
			}), replayfault.ErrBlocked)
		})
	}
}

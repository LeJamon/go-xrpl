package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

func TestLedgerAcceptanceMissingPaymentStateBlocksPublication(t *testing.T) {
	for _, applyPhase := range []bool{false, true} {
		for _, standalone := range []bool{false, true} {
			t.Run(fmt.Sprintf("apply=%t/standalone=%t", applyPhase, standalone), func(t *testing.T) {
				f := newStateBaseRecertificationFixture(t)
				f.svc.StopStateBaseRecertification()
				master := jtx.MasterAccount()
				masterKey := keylet.Account(master.AccountID()).Key
				var destinationName string
				var missingKey keylet.Keylet
				var found bool
				for i := range 100 {
					destinationName = fmt.Sprintf("storage-fault-destination-%d", i)
					destinationID := jtx.NewAccount(destinationName).AccountID()
					missingKey = keylet.Account(destinationID)
					if applyPhase {
						if state.CompareAccountIDs(destinationID, master.AccountID()) < 0 {
							continue
						}
						missingKey = keylet.Line(master.AccountID(), destinationID, "USD")
						if missingKey.Key[0]>>4 == keylet.Account(destinationID).Key[0]>>4 {
							continue
						}
					}
					branch := missingKey.Key[0] >> 4
					if branch != masterKey[0]>>4 && branch != keylet.Amendments().Key[0]>>4 &&
						branch != keylet.Fees().Key[0]>>4 && branch != keylet.LedgerHashes().Key[0]>>4 {
						found = true
						break
					}
				}
				require.True(t, found)
				require.NotEqual(t, masterKey[0]>>4, missingKey.Key[0]>>4)
				blob, _ := startupPaymentBlob(t, destinationName, 1)
				if applyPhase {
					destination := jtx.NewAccount(destinationName)
					insertAccountRoot(t, f.svc, destination.Address, 100_000_000, 0)
					insertTrustLine(t, f.svc, destination.Address, master.Address, "USD", "0")
					_, err := f.svc.AcceptLedger(t.Context())
					require.NoError(t, err)
					f.svc.FlushPersists()
					f.validated = f.svc.GetValidatedLedger()
					f.stateRoot = f.validated.Header().AccountHash
					line, err := f.validated.Read(missingKey)
					require.NoError(t, err)
					require.NotEmpty(t, line)
					transaction := payment.PayIssued(master, destination, master.IOU("USD", 1)).Sequence(1).Build()
					env := jtx.NewTestEnv(t)
					env.SetVerifySignatures(true)
					env.SignWith(transaction, master)
					txJSON, err := transaction.Flatten()
					require.NoError(t, err)
					blobHex, err := binarycodec.Encode(txJSON)
					require.NoError(t, err)
					blob, err = hex.DecodeString(blobHex)
					require.NoError(t, err)
				}
				root, err := f.db.Fetch(t.Context(), nodestore.Hash256(f.stateRoot))
				require.NoError(t, err)
				reader, err := shamap.DeserializeFromPrefix(root.Data)
				require.NoError(t, err)
				missing, err := reader.(shamap.InnerNodeReader).ChildHash(int(missingKey.Key[0] >> 4))
				require.NoError(t, err)
				require.NotEqual(t, [32]byte{}, missing)
				family := consensusMissingNodeFamily{Family: f.svc.shamapFamily, missing: missing}
				stateMap, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, f.stateRoot, family)
				require.NoError(t, err)
				txMap, err := f.validated.TxMapSnapshot()
				require.NoError(t, err)
				parent, err := ledger.NewFromHeader(f.validated.Header(), stateMap, txMap, f.validated.Fees())
				require.NoError(t, err)
				if applyPhase {
					for _, account := range []*jtx.Account{master, jtx.NewAccount(destinationName)} {
						data, readErr := parent.Read(keylet.Account(account.AccountID()))
						require.NoError(t, readErr)
						require.NotEmpty(t, data)
					}
				}
				simOpen, err := openledger.New(parent, openledger.Config{})
				require.NoError(t, err)
				f.svc.mu.Lock()
				originalOpenView := f.svc.openLedgerView
				f.svc.openLedgerView = simOpen
				f.svc.mu.Unlock()
				transaction, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				simulation, err := f.svc.SimulateTransaction(transaction)
				require.NoError(t, err)
				require.Equal(t, ter.TefEXCEPTION, simulation.Result)
				require.False(t, simulation.Applied)
				f.svc.mu.Lock()
				f.svc.openLedgerView = originalOpenView
				f.svc.closedLedger = parent
				beforeOpen := f.svc.openLedger
				f.svc.mu.Unlock()
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
}

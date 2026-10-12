package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type consensusMissingNodeFamily struct {
	shamap.Family
	mu      sync.RWMutex
	missing [32]byte
}

func (f *consensusMissingNodeFamily) SetMissing(hash [32]byte) {
	f.mu.Lock()
	f.missing = hash
	f.mu.Unlock()
}

func (f *consensusMissingNodeFamily) isMissing(hash [32]byte) bool {
	f.mu.RLock()
	missing := f.missing
	f.mu.RUnlock()
	return missing != ([32]byte{}) && hash == missing
}

func (f *consensusMissingNodeFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.isMissing(hash) {
		return nil, nil
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *consensusMissingNodeFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.isMissing(hash) {
		return nil, nil
	}
	durable, ok := f.Family.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	if !ok {
		return f.Family.Fetch(ctx, hash)
	}
	return durable.FetchDurable(ctx, hash)
}

func (f *consensusMissingNodeFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	durable, ok := f.Family.(interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	})
	if !ok {
		return [32]byte{}, nil, fmt.Errorf("family has no durable snapshot")
	}
	return durable.AcquireDurableSnapshot(ctx)
}

func (f *consensusMissingNodeFamily) FullBelowCache() *shamap.FullBelowCache {
	provider, ok := f.Family.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	if !ok {
		return nil
	}
	return provider.FullBelowCache()
}

func newConsensusStorageFaultFixture(t *testing.T) (*stateBaseRecertificationFixture, *consensusMissingNodeFamily) {
	t.Helper()
	ctx := context.Background()
	db := newTestNodeStore(t, 100_000)
	repositories := newTestRepositories(t, ctx)
	family := &consensusMissingNodeFamily{Family: backend.New(db)}
	execution := newTestExecutionStore(t, family)
	svc, err := New(Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		NodeStore: db, SHAMapFamily: family, Execution: execution,
		RelationalDB: repositories, FastLoad: true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	svc.recertificationMu.Lock()
	svc.recertificationStopped = true
	svc.recertificationMu.Unlock()
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, db.Close())
	})

	for branch := range shamap.BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	rawTx, _ := validRelationalTestTransaction(t, 1)
	txBlob, txHash := makeTxMetaBlobForTest(t, rawTx, 0)
	require.NoError(t, svc.openLedger.AddTransactionWithMeta(txHash, txBlob))
	closeStoredLedgerFixture(t, svc)
	svc.FlushPersists()
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	h := validated.Header()
	require.NoError(t, db.WithDurableSnapshot(ctx, func(fingerprint [32]byte) error {
		if err := svc.verifyStoredSHAMap(ctx, h.AccountHash, shamap.TypeState); err != nil {
			return err
		}
		if h.TxHash != ([32]byte{}) {
			if err := svc.verifyStoredSHAMap(ctx, h.TxHash, shamap.TypeTransaction); err != nil {
				return err
			}
		}
		svc.rememberValidatedStateBase(h, fingerprint)
		return nil
	}))
	svc.recertificationMu.Lock()
	svc.recertificationStopped = false
	svc.recertificationMu.Unlock()

	rootNode, err := db.Fetch(ctx, nodestore.Hash256(h.AccountHash))
	require.NoError(t, err)
	require.NotNil(t, rootNode)
	rootReader, err := shamap.DeserializeFromPrefix(rootNode.Data)
	require.NoError(t, err)
	rootInner, ok := rootReader.(shamap.InnerNodeReader)
	require.True(t, ok)
	var childHash [32]byte
	for branch := range shamap.BranchFactor {
		if rootInner.IsEmptyBranch(branch) {
			continue
		}
		childHash, err = rootInner.ChildHash(branch)
		require.NoError(t, err)
		break
	}
	require.NotEqual(t, [32]byte{}, childHash)
	childNode, err := db.Fetch(ctx, nodestore.Hash256(childHash))
	require.NoError(t, err)
	require.NotNil(t, childNode)

	return &stateBaseRecertificationFixture{
		svc: svc, db: db, validated: validated,
		stateRoot: h.AccountHash, childHash: childHash,
	}, family
}

func TestLedgerAcceptanceMissingPaymentStateBlocksPublication(t *testing.T) {
	for _, applyPhase := range []bool{false, true} {
		for _, standalone := range []bool{false, true} {
			t.Run(fmt.Sprintf("apply=%t/standalone=%t", applyPhase, standalone), func(t *testing.T) {
				f, faultFamily := newConsensusStorageFaultFixture(t)
				t.Cleanup(func() { faultFamily.SetMissing([32]byte{}) })
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
				destination := jtx.NewAccount(destinationName)
				insertAccountRoot(t, f.svc, destination.Address, 100_000_000, 0)
				if applyPhase {
					insertTrustLine(t, f.svc, destination.Address, master.Address, "USD", "0")
				}
				closeStoredLedgerFixture(t, f.svc)
				f.svc.FlushPersists()
				f.validated = f.svc.GetValidatedLedger()
				f.stateRoot = f.validated.Header().AccountHash
				if applyPhase {
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
				stateMap, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, f.stateRoot, f.svc.shamapFamily)
				require.NoError(t, err)
				txMap, err := f.validated.TxMapSnapshot()
				require.NoError(t, err)
				parent, err := ledger.NewFromHeader(f.validated.Header(), stateMap, txMap, f.validated.Fees(), f.svc.config.Execution)
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
				t.Cleanup(func() { simOpen.Current().Discard() })
				f.svc.mu.Lock()
				originalOpenView := f.svc.openLedgerView
				f.svc.openLedgerView = simOpen
				f.svc.mu.Unlock()
				faultFamily.SetMissing(missing)
				transaction, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				transaction.GetCommon().TxnSignature = ""
				simulation, err := f.svc.SimulateTransaction(transaction)
				require.NoError(t, err)
				require.Equal(t, ter.TefEXCEPTION, simulation.Result)
				require.False(t, simulation.Applied)
				faultFamily.SetMissing([32]byte{})
				f.svc.mu.Lock()
				f.svc.openLedgerView = originalOpenView
				f.svc.mu.Unlock()
				if standalone {
					result, submitErr := f.svc.SubmitOpenLedgerTxDetailed(blob, false)
					require.NoError(t, submitErr)
					require.Equal(t, ter.TesSUCCESS, result.Result)
					require.True(t, result.Applied)
					require.Equal(t, uint32(1), f.svc.GetOpenLedger().TxCount())
				}
				faultFamily.SetMissing(missing)
				f.svc.mu.Lock()
				f.svc.closedLedger = parent
				beforeOpen := f.svc.currentOpenLedgerLocked()
				f.svc.mu.Unlock()
				if standalone {
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

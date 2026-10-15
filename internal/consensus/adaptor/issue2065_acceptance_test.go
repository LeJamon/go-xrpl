package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

// issue2065EmptyPoolAdaptor drives the close path with a deliberately empty
// agreed set while the real adaptor/service path replays disputed input later.
type issue2065EmptyPoolAdaptor struct {
	*Adaptor
	successorBlob []byte
	signed        chan *consensus.Validation
}

func (a *issue2065EmptyPoolAdaptor) GetPendingTxs() [][]byte {
	return [][]byte{{0x01}}
}

func (a *issue2065EmptyPoolAdaptor) GetProposableTxs(consensus.Ledger) [][]byte {
	return [][]byte{{0x01}}
}

func (a *issue2065EmptyPoolAdaptor) BuildTxSet(_ [][]byte) (consensus.TxSet, error) {
	return a.Adaptor.BuildTxSet(nil)
}

func (a *issue2065EmptyPoolAdaptor) BuildLedger(
	ctx context.Context,
	parent consensus.Ledger,
	txSet consensus.TxSet,
	closeTime time.Time,
	closeTimeCorrect bool,
	disputedTxs [][]byte,
) (consensus.Ledger, error) {
	// Keep the agreed set empty while supplying one disputed transaction to
	// the real adaptor callback. This exercises successor replay without
	// warming the selected state path through ingress first.
	if len(a.successorBlob) != 0 {
		disputedTxs = [][]byte{a.successorBlob}
	}
	return a.Adaptor.BuildLedger(ctx, parent, txSet, closeTime, closeTimeCorrect, disputedTxs)
}

func (a *issue2065EmptyPoolAdaptor) SignValidation(validation *consensus.Validation) error {
	if err := a.Adaptor.SignValidation(validation); err != nil {
		return err
	}
	copy := *validation
	select {
	case a.signed <- &copy:
	default:
	}
	return nil
}

type issue2065BlockingFamily struct {
	shamap.Family
	blocked map[[32]byte]struct{}
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *issue2065BlockingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	_, selected := f.blocked[hash]
	if (f.blocked == nil || selected) && f.armed.CompareAndSwap(true, false) {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *issue2065BlockingFamily) unblock() {
	f.once.Do(func() { close(f.release) })
}

func issue2065ColdParent(t testing.TB, parent *ledger.Ledger) (*ledger.Ledger, *issue2065BlockingFamily) {
	t.Helper()
	child, err := ledger.NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	require.NoError(t, err)
	require.NoError(t, child.Close(parent.CloseTime().Add(10*time.Second), 0))

	memory := backend.NewMemory()
	state, err := child.StateMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, state.StoreDirty(func(entries []shamap.FlushEntry) error {
		return memory.StoreBatch(t.Context(), entries)
	}))
	root, err := state.Hash()
	require.NoError(t, err)
	family := &issue2065BlockingFamily{
		Family:  memory,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(family.unblock)
	coldState, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	require.Zero(t, child.TxCount())
	cold, err := ledger.NewFromHeader(child.Header(), coldState, txMap, child.Fees())
	require.NoError(t, err)
	return cold, family
}

type issue2065PathFamily struct {
	shamap.Family
	mu     sync.Mutex
	hashes [][32]byte
}

func (f *issue2065PathFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.hashes = append(f.hashes, hash)
	f.mu.Unlock()
	return f.Family.Fetch(ctx, hash)
}

func (f *issue2065PathFamily) path() [][32]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := make([][32]byte, len(f.hashes))
	copy(path, f.hashes)
	return path
}

func issue2065StatePath(t testing.TB, root [32]byte, base shamap.Family, key [32]byte) [][32]byte {
	t.Helper()
	recording := &issue2065PathFamily{Family: base}
	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, root, recording)
	require.NoError(t, err)
	_, _, err = stateMap.GetContext(t.Context(), key)
	require.NoError(t, err)
	return recording.path()
}

func issue2065SuccessorParent(t testing.TB, parent *ledger.Ledger, account [20]byte) (*ledger.Ledger, *issue2065BlockingFamily) {
	t.Helper()
	cold, family := issue2065ColdParent(t, parent)
	root, err := cold.StateMapHash()
	require.NoError(t, err)
	base := family.Family
	accountPath := issue2065StatePath(t, root, base, keylet.Account(account).Key)
	feePath := issue2065StatePath(t, root, base, keylet.Fees().Key)
	feeNodes := make(map[[32]byte]struct{}, len(feePath))
	for _, hash := range feePath {
		feeNodes[hash] = struct{}{}
	}
	blocked := make(map[[32]byte]struct{}, len(accountPath))
	for _, hash := range accountPath {
		if _, shared := feeNodes[hash]; !shared {
			blocked[hash] = struct{}{}
		}
	}
	require.NotEmpty(t, blocked, "account and fee paths must diverge")
	family.blocked = blocked
	return cold, family
}

func issue2065SignedPaymentBlob(
	t testing.TB,
	env *jtx.TestEnv,
	sender, receiver *jtx.Account,
	amount uint64,
	sequence uint32,
) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)
	transaction := payment.Pay(sender, receiver, amount).Sequence(sequence).Build()
	env.SignWith(transaction, sender)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func TestIssue2065AdaptorValidationDoesNotWaitForSpeculativeSuccessor(t *testing.T) {
	tests := []struct {
		name      string
		proposing bool
		full      bool
	}{
		{name: "full", proposing: true, full: true},
		{name: "partial", proposing: false, full: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := newTestAdaptor(t)
			t.Cleanup(base.ledgerService.Stop)
			base.SetOperatingMode(consensus.OpModeFull)

			account := jtx.MasterAccount()
			cold, family := issue2065SuccessorParent(t, base.ledgerService.GetClosedLedger(), account.AccountID())
			require.NoError(t, base.ledgerService.SwitchToPreferredLedger(cold))
			closed := base.ledgerService.GetClosedLedger()
			require.Same(t, cold, closed)

			env := jtx.NewTestEnv(t)
			blob, hash := issue2065SignedPaymentBlob(t, env, account, jtx.NewAccount("issue-2065-"+tc.name), 1_000_000, 1)
			family.armed.Store(true)

			adaptor := &issue2065EmptyPoolAdaptor{
				Adaptor:       base,
				successorBlob: blob,
				signed:        make(chan *consensus.Validation, 2),
			}
			config := rcl.DefaultConfig()
			config.ManualTick = true
			var clockMu sync.Mutex
			clockNow := time.Now()
			config.Clock = func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return clockNow
			}
			config.Timing.LedgerMinClose = 0
			config.Timing.LedgerIdleInterval = 0
			config.Timing.LedgerMinConsensus = 0
			config.Timing.LedgerMaxConsensus = time.Millisecond
			config.Timing.LedgerAbandonConsensus = time.Second
			engine := rcl.NewEngine(adaptor, config)
			require.NoError(t, engine.Start(t.Context()))
			t.Cleanup(func() {
				family.unblock()
				require.NoError(t, engine.Stop())
			})

			require.NoError(t, engine.StartRound(consensus.RoundID{
				Seq:        closed.Sequence() + 1,
				ParentHash: consensus.LedgerID(closed.Hash()),
			}, tc.proposing))

			runTick := func() {
				done := make(chan struct{})
				go func() {
					engine.TimerEntry()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("consensus timer blocked before acceptance started")
				}
			}
			runTick()
			runTick()
			clockMu.Lock()
			clockNow = clockNow.Add(2 * time.Millisecond)
			clockMu.Unlock()
			runTick()

			select {
			case <-family.entered:
			case <-time.After(time.Second):
				t.Fatal("real acceptance did not enter speculative successor work")
			}

			var signedValidation *consensus.Validation
			select {
			case signedValidation = <-adaptor.signed:
				require.NotNil(t, signedValidation)
				require.Equal(t, tc.full, signedValidation.Full)
			case <-time.After(time.Second):
				t.Fatal("validation waited for the blocked speculative successor")
			}

			require.Equal(t, closed.Sequence()+1, signedValidation.LedgerSeq)
			accepted := base.ledgerService.GetClosedLedger()
			require.NotNil(t, accepted)
			require.Equal(t, consensus.LedgerID(accepted.Hash()), signedValidation.LedgerID)
			require.Equal(t, [32]byte{}, accepted.Header().TxHash,
				"the agreed empty set must remain empty while successor work is blocked")
			require.Equal(t, accepted.Hash(), base.ledgerService.GetOpenLedger().ParentHash())
			exists, err := base.ledgerService.OpenLedgerHasTx(hash)
			require.NoError(t, err)
			require.False(t, exists, "local input belongs to the deferred successor, not the skeleton")

			family.unblock()
			require.Eventually(t, func() bool {
				exists, err := base.ledgerService.OpenLedgerHasTx(hash)
				return err == nil && exists
			}, 5*time.Second, time.Millisecond,
				"speculative successor did not replay local input")
			select {
			case duplicate := <-adaptor.signed:
				t.Fatalf("acceptance emitted a duplicate validation for seq %d", duplicate.LedgerSeq)
			default:
			}
		})
	}
}

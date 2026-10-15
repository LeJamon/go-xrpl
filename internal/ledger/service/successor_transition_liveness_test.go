package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestConsensusSuccessorPreparationKeepsReadersAndIngressResponsive(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	sender, destination := jtx.MasterAccount(), jtx.NewAccount("consensus-successor")
	prior, priorHash := preferredSwitchPaymentBlob(t, env, sender, destination, 20_000_000, 100, 1)
	late, lateHash := preferredSwitchPaymentBlob(t, env, sender, destination, 1_000_000, 100, 2)
	parent, family := successorPreparationParent(t, svc.GetClosedLedger(), sender.AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	oldClosed := svc.GetClosedLedger()
	priorTx, err := openledger.ParsePendingTx(prior)
	require.NoError(t, err)
	svc.localTxs.PushBack(svc.openLedgerView.Current().Sequence(), priorTx)

	family.armed.Store(true)
	accepted := make(chan error, 1)
	go func() {
		_, acceptErr := svc.AcceptConsensusResult(
			t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true,
		)
		accepted <- acceptErr
	}()
	awaitSuccessorPreparation(t, family.entered, family.unblock, &family.fetches)
	assertSuccessorReadersAndGate(t, svc, oldClosed, family.unblock)

	submitted := make(chan error, 1)
	go func() {
		result, submitErr := svc.SubmitOpenLedgerTxDetailed(late, true)
		if submitErr == nil && result.Result.String() != "tesSUCCESS" {
			submitErr = fmt.Errorf("late submission returned %s", result.Result)
		}
		submitted <- submitErr
	}()
	select {
	case submitErr := <-submitted:
		t.Fatalf("late ingress completed before successor publication: %v", submitErr)
	case <-time.After(50 * time.Millisecond):
	}
	family.unblock()
	require.NoError(t, <-accepted)
	require.NoError(t, <-submitted)

	closed := svc.GetClosedLedger()
	require.NotSame(t, parent, closed)
	require.Equal(t, parent.Sequence()+1, closed.Sequence())
	current := svc.openLedgerView.Current()
	require.Equal(t, closed.Hash(), current.ParentHash())
	for _, hash := range [][32]byte{priorHash, lateHash} {
		present, txErr := current.TxExists(hash)
		require.NoError(t, txErr)
		require.True(t, present, "successor should retain transaction %x", hash)
	}
}

func TestConsensusSuccessorSurvivesCompletedAcceptanceContext(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	sender, destination := jtx.MasterAccount(), jtx.NewAccount("consensus-failure")
	prior, priorHash := preferredSwitchPaymentBlob(t, env, sender, destination, 20_000_000, 100, 1)
	late, lateHash := preferredSwitchPaymentBlob(t, env, sender, destination, 21_000_000, 100, 2)
	parent, family := successorPreparationParent(t, svc.GetClosedLedger(), sender.AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	priorTx, err := openledger.ParsePendingTx(prior)
	require.NoError(t, err)
	svc.localTxs.PushBack(svc.openLedgerView.Current().Sequence(), priorTx)

	family.armed.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	accepted := make(chan error, 1)
	go func() {
		_, acceptErr := svc.AcceptConsensusResult(
			ctx, parent, nil, nil, parent.CloseTime().Add(time.Second), true,
		)
		accepted <- acceptErr
	}()
	awaitSuccessorPreparation(t, family.entered, family.unblock, &family.fetches)
	assertSuccessorReadersAndGate(t, svc, svc.GetClosedLedger(), family.unblock)

	cancel()
	family.unblock()
	require.NoError(t, <-accepted)
	require.NoError(t, svc.waitSpeculativeSuccessor(t.Context()))

	submitted := make(chan error, 1)
	go func() {
		result, submitErr := svc.SubmitOpenLedgerTxDetailed(late, true)
		if submitErr == nil && result.Result.String() != "tesSUCCESS" {
			submitErr = fmt.Errorf("late submission returned %s", result.Result)
		}
		submitted <- submitErr
	}()
	require.NoError(t, <-submitted)
	require.Equal(t, parent.Sequence()+1, svc.GetClosedLedger().Sequence())
	current := svc.openLedgerView.Current()
	for _, hash := range [][32]byte{priorHash, lateHash} {
		present, txErr := current.TxExists(hash)
		require.NoError(t, txErr)
		require.True(t, present, "successor must retain replay and late ingress: %x", hash)
	}
	_, held := svc.localTxs.Get(priorHash)
	require.True(t, held, "local replay must retain the held transaction")
}

type successorPreparationParentFamily struct {
	shamap.Family
	blocked map[[32]byte]struct{}
	armed   atomic.Bool
	fail    atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	fetches atomic.Int64
	err     error
}

func (f *successorPreparationParentFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
	if f.fail.Load() {
		return nil, f.err
	}
	if _, ok := f.blocked[hash]; ok && f.armed.CompareAndSwap(true, false) {
		close(f.entered)
		<-f.release
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.fail.Load() {
		return nil, f.err
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *successorPreparationParentFamily) unblock() {
	f.once.Do(func() { close(f.release) })
}

type successorPathFamily struct {
	shamap.Family
	mu     sync.Mutex
	hashes [][32]byte
}

func (f *successorPathFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.hashes = append(f.hashes, hash)
	f.mu.Unlock()
	return f.Family.Fetch(ctx, hash)
}

func (f *successorPathFamily) path() [][32]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := make([][32]byte, len(f.hashes))
	copy(path, f.hashes)
	return path
}

func successorPreparationParent(t testing.TB, parent *ledger.Ledger, account [20]byte) (*ledger.Ledger, *successorPreparationParentFamily) {
	t.Helper()
	cold, backing := coldAcceptanceParent(t, parent)
	root, err := cold.StateMapHash()
	require.NoError(t, err)
	base := backing.Family
	accountPath := successorStatePath(t, root, base, keylet.Account(account).Key)
	feePath := successorStatePath(t, root, base, keylet.Fees().Key)
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
	family := &successorPreparationParentFamily{
		Family:  base,
		blocked: blocked,
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("successor cold read failed"),
	}
	cold.SetStateMapFamily(family)
	t.Cleanup(family.unblock)
	return cold, family
}

func successorStatePath(t testing.TB, root [32]byte, base shamap.Family, key [32]byte) [][32]byte {
	t.Helper()
	recording := &successorPathFamily{Family: base}
	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, root, recording)
	require.NoError(t, err)
	_, _, err = stateMap.GetContext(t.Context(), key)
	require.NoError(t, err)
	return recording.path()
}

func awaitSuccessorPreparation(t *testing.T, entered <-chan struct{}, unblock func(), fetches *atomic.Int64) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		unblock()
		t.Fatalf("successor preparation did not reach the cold read (fetches=%d)", fetches.Load())
	}
}

func assertSuccessorReadersAndGate(t *testing.T, svc *Service, oldClosed *ledger.Ledger, unblock func()) {
	t.Helper()
	reads := make(chan error, 1)
	go func() {
		if _, err := svc.GetLedgerBySequence(oldClosed.Sequence()); err != nil {
			reads <- err
			return
		}
		_ = svc.GetClosedLedger()
		_ = svc.GetValidatedLedger()
		_ = svc.TxQMetrics()
		reads <- nil
	}()
	select {
	case err := <-reads:
		require.NoError(t, err)
	case <-time.After(time.Second):
		unblock()
		t.Fatal("successor preparation blocked service/history readers")
	}
	if svc.openLedgerMu.Snapshot().Held {
		unblock()
		t.Fatal("successor preparation retained the service gate")
	}
}

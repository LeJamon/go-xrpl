package adaptor

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type issue2065ValidationDelayFamily struct {
	shamap.Family
	delayed      map[[32]byte]struct{}
	delay        time.Duration
	selectedHits atomic.Int64
}

func (f *issue2065ValidationDelayFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if _, selected := f.delayed[hash]; selected && f.delay > 0 {
		f.selectedHits.Add(1)
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return f.Family.Fetch(ctx, hash)
}

func issue2065TimedSuccessorParent(
	t testing.TB,
	parent *ledger.Ledger,
	account [20]byte,
	delay time.Duration,
) (*ledger.Ledger, *issue2065ValidationDelayFamily) {
	t.Helper()
	child, err := ledger.NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	require.NoError(t, err)
	require.NoError(t, child.Close(parent.CloseTime().Add(10*time.Second), 0))
	state, err := child.StateMapSnapshot()
	require.NoError(t, err)
	wireNodes, err := state.WalkWireNodes()
	require.NoError(t, err)
	memory := backend.NewMemory()
	entries := make([]shamap.FlushEntry, 0, len(wireNodes))
	for _, wireNode := range wireNodes {
		entry, entryErr := shamap.FlushEntryFromWire(wireNode.Data, child.Sequence(), shamap.TypeState)
		require.NoError(t, entryErr)
		entries = append(entries, entry)
	}
	require.NoError(t, memory.StoreBatch(t.Context(), entries))
	root, err := state.Hash()
	require.NoError(t, err)
	accountPath := issue2065StatePath(t, root, memory, keylet.Account(account).Key)
	feePath := issue2065StatePath(t, root, memory, keylet.Fees().Key)
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
	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, root, memory)
	require.NoError(t, err)
	cold, err := ledger.NewFromHeader(child.Header(), stateMap, shamap.New(shamap.TypeTransaction), child.Fees())
	require.NoError(t, err)
	delayed := &issue2065ValidationDelayFamily{
		Family:  memory,
		delayed: blocked,
		delay:   delay,
	}
	cold.SetStateMapFamily(delayed)
	return cold, delayed
}

func newIssue2065BenchmarkAdaptor(b *testing.B, initialAccounts []genesis.InitialAccount) *Adaptor {
	b.Helper()
	svc, err := service.New(service.Config{
		Standalone: true,
		GenesisConfig: func() genesis.Config {
			config := genesis.DefaultConfig()
			config.InitialAccounts = initialAccounts
			return config
		}(),
	})
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	b.Cleanup(svc.Stop)

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(b, err)
	return New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
}

func issue2065SignedOfferBlob(b testing.TB, env *jtx.TestEnv, sender *jtx.Account, amount uint64) ([]byte, [32]byte) {
	b.Helper()
	env.SetVerifySignatures(true)
	issuer := jtx.NewAccount("issue-2065-adaptor-offer-issuer")
	transaction := offertest.OfferCreate(
		sender,
		jtx.USD(issuer, 1),
		jtx.XRPTxAmountFromXRP(float64(amount)),
	).Sequence(1).Build()
	env.SignWith(transaction, sender)
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(b, err)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(b, err)
	return blob, hash
}

func issue2065ReportValidationQuantiles(b *testing.B, samples []time.Duration) {
	b.Helper()
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	quantile := func(percentile int) time.Duration {
		index := (len(sorted)*percentile + 99) / 100
		if index > 0 {
			index--
		}
		return sorted[index]
	}
	b.Logf("issue2065 validation raw=%v", samples)
	b.Logf("issue2065 validation p50=%s p95=%s p99=%s", quantile(50), quantile(95), quantile(99))
}

func BenchmarkIssue2065AdaptorValidationLatency(b *testing.B) {
	b.StopTimer()
	accountCount := b.N
	if accountCount < 128 {
		accountCount = 128
	}
	accounts := make([]*jtx.Account, accountCount)
	initialAccounts := make([]genesis.InitialAccount, accountCount)
	for i := range accountCount {
		accounts[i] = jtx.NewAccount(fmt.Sprintf("issue-2065-adaptor-offer-sender-%d", i))
		initialAccounts[i] = genesis.InitialAccount{
			Address: accounts[i].Address,
			Balance: 100_000_000,
		}
	}
	base := newIssue2065BenchmarkAdaptor(b, initialAccounts)
	base.SetOperatingMode(consensus.OpModeFull)
	parent, delayed := issue2065TimedSuccessorParent(
		b,
		base.ledgerService.GetClosedLedger(),
		accounts[0].AccountID(),
		0,
	)
	require.NoError(b, base.ledgerService.SwitchToPreferredLedger(parent))
	delayed.delay = 2 * time.Millisecond
	closed := base.ledgerService.GetClosedLedger()
	env := jtx.NewTestEnv(b)
	offerBlobs := make([][]byte, b.N)
	offerHashes := make([][32]byte, b.N)
	for round := range b.N {
		offerBlobs[round], offerHashes[round] = issue2065SignedOfferBlob(
			b, env, accounts[round], uint64(round+1),
		)
	}

	adaptor := &issue2065EmptyPoolAdaptor{
		Adaptor:       base,
		successorBlob: offerBlobs[0],
		signed:        make(chan *consensus.Validation, b.N+8),
	}
	config := rcl.DefaultConfig()
	config.ManualTick = true
	clockNow := time.Now()
	config.Clock = func() time.Time { return clockNow }
	config.Timing.LedgerMinClose = 0
	config.Timing.LedgerIdleInterval = 0
	config.Timing.LedgerMinConsensus = time.Millisecond
	config.Timing.LedgerMaxConsensus = time.Millisecond
	config.Timing.LedgerAbandonConsensus = time.Second
	engine := rcl.NewEngine(adaptor, config)
	require.NoError(b, engine.Start(b.Context()))
	defer func() {
		delayed.delay = 0
		_ = engine.Stop()
		base.ledgerService.Stop()
	}()
	b.Cleanup(func() {
		delayed.delay = 0
		require.NoError(b, engine.Stop())
	})
	require.NoError(b, engine.StartRound(consensus.RoundID{
		Seq:        closed.Sequence() + 1,
		ParentHash: consensus.LedgerID(closed.Hash()),
	}, true))

	runPreparationTick := func() {
		for range 8 {
			engine.TimerEntry()
			if engine.Phase() == consensus.PhaseEstablish {
				break
			}
		}
		require.Equal(b, consensus.PhaseEstablish, engine.Phase(),
			"benchmark setup must stop before ledger acceptance")
		clockNow = clockNow.Add(2 * time.Millisecond)
	}

	samples := make([]time.Duration, 0, b.N)
	for round := range b.N {
		if round > 0 {
			clockNow = clockNow.Add(30 * time.Second)
			parent, nextDelayed := issue2065TimedSuccessorParent(
				b,
				closed,
				accounts[round].AccountID(),
				0,
			)
			require.NoError(b, base.ledgerService.SwitchToPreferredLedger(parent))
			require.NoError(b, engine.RestartRound(true))
			clockNow = clockNow.Add(2 * time.Millisecond)
			delayed = nextDelayed
			delayed.delay = 2 * time.Millisecond
			delayed.selectedHits.Store(0)
			closed = base.ledgerService.GetClosedLedger()
		}
		adaptor.successorBlob = offerBlobs[round]
		runPreparationTick()
		beforeFetches := delayed.selectedHits.Load()
		b.StartTimer()
		started := time.Now()
		engine.TimerEntry()
		var validation *consensus.Validation
		select {
		case validation = <-adaptor.signed:
		case <-time.After(5 * time.Second):
			b.Fatal("real adaptor did not emit validation")
		}
		elapsed := time.Since(started)
		b.StopTimer()
		require.NotNil(b, validation)
		require.Equal(b, closed.Sequence()+1, validation.LedgerSeq)
		samples = append(samples, elapsed)
		closed = base.ledgerService.GetClosedLedger()
		require.NotNil(b, closed)
		require.Eventually(b, func() bool {
			return delayed.selectedHits.Load() > beforeFetches
		}, 5*time.Second, time.Millisecond,
			"successor replay must traverse the delayed account path")
		require.Eventually(b, func() bool {
			exists, err := base.ledgerService.OpenLedgerHasTx(offerHashes[round])
			return err == nil && exists
		}, 5*time.Second, time.Millisecond,
			"successor OfferCreate was not replayed")
		require.Eventually(b, func() bool {
			round, ok := engine.CurrentRound()
			return ok && round.Seq == validation.LedgerSeq+1
		}, 5*time.Second, time.Millisecond,
			"engine did not advance to the next round")
	}
	issue2065ReportValidationQuantiles(b, samples)
}

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func BenchmarkService_Issue2065OfferCreateSuccessor(b *testing.B) {
	cases := []struct {
		name        string
		withRefresh bool
	}{
		{name: "cold", withRefresh: false},
		{name: "cold-refresh", withRefresh: true},
	}

	b.StopTimer()
	fixture := newBenchmarkRefreshFixture(b, 16_384, 256<<10)
	env := jtx.NewTestEnv(b)
	offerBlob, offerHash := issue2065OfferCreateBlob(b, env)
	b.SetBytes(int64(len(offerBlob)))

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			acceptReturn := make([]time.Duration, 0, b.N)
			speculativeReady := make([]time.Duration, 0, b.N)
			gateWait := make([]time.Duration, 0, b.N)
			gateHold := make([]time.Duration, 0, b.N)
			selectedFetches := make([]int64, 0, b.N)

			for round := range b.N {
				b.StopTimer()
				logger := &consensusAcceptanceBenchmarkLogger{}
				svc, parent, delayed := newIssue2065OfferBenchmarkService(
					b, fixture, logger, 2*time.Millisecond, round,
				)
				offerTx, err := openledger.ParsePendingTx(offerBlob)
				if err != nil {
					svc.Stop()
					b.Fatalf("parse signed OfferCreate: %v", err)
				}
				svc.localTxs.PushBack(svc.openLedgerView.Current().Sequence(), offerTx)

				var refreshDone <-chan error
				if tc.withRefresh {
					fixture.db.promotionStart = make(chan struct{})
					fixture.db.promotionOnce = sync.Once{}
					fixture.db.promotionDelay = 100 * time.Microsecond
					done := make(chan error, 1)
					refreshDone = done
					go func() {
						done <- svc.refreshGenerationStateWithBatch(
							b.Context(), fixture.root, fixture.seq, fixture.db, nil, 1,
							storedSHAMapPromotionBatchNodes, storedSHAMapPromotionBatchBytes,
						)
					}()
					select {
					case <-fixture.db.promotionStart:
					case err := <-done:
						svc.Stop()
						b.Fatalf("refresh ended before its first promotion: %v", err)
					case <-time.After(5 * time.Second):
						svc.Stop()
						b.Fatal("refresh did not reach its first promotion")
					}
				}

				b.StartTimer()
				start := time.Now()
				_, err = svc.AcceptConsensusResult(
					b.Context(), parent, nil, nil, parent.CloseTime().Add(10*time.Second), true,
				)
				acceptElapsed := time.Since(start)
				b.StopTimer()
				if err != nil {
					if refreshDone != nil {
						<-refreshDone
					}
					svc.Stop()
					b.Fatalf("accept signed OfferCreate successor: %v", err)
				}

				readyAt := time.Now()
				for {
					exists, existsErr := svc.OpenLedgerHasTx(offerHash)
					if existsErr == nil && exists {
						break
					}
					if time.Since(readyAt) > 5*time.Second {
						svc.Stop()
						b.Fatalf("successor OfferCreate was not observable: %v", existsErr)
					}
					time.Sleep(time.Millisecond)
				}
				readyElapsed := time.Since(readyAt)
				acceptReturn = append(acceptReturn, acceptElapsed)
				speculativeReady = append(speculativeReady, readyElapsed)
				selectedFetches = append(selectedFetches, delayed.selectedHits.Load())

				if refreshDone != nil {
					if err := <-refreshDone; err != nil {
						svc.Stop()
						b.Fatalf("refresh generation state: %v", err)
					}
				}
				var sample consensusAcceptanceBenchmarkTiming
				var ok bool
				for attempt := 0; attempt < 500; attempt++ {
					if sample, ok = logger.latest(); ok {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if !ok {
					svc.Stop()
					b.Fatal("acceptance did not emit timing sample")
				}
				gateWait = append(gateWait, sample.openLedgerWait)
				gateHold = append(gateHold, sample.openLedgerHold)
				fixture.db.promotionDelay = 0
				svc.Stop()
			}

			reportConsensusAcceptanceQuantiles(b, "accept-return", acceptReturn)
			reportConsensusAcceptanceQuantiles(b, "speculative-ready", speculativeReady)
			reportConsensusAcceptanceQuantiles(b, "gate-wait", gateWait)
			reportConsensusAcceptanceQuantiles(b, "gate-hold", gateHold)
			b.Logf("issue2065 raw accept-return=%v speculative-ready=%v gate-wait=%v gate-hold=%v", acceptReturn, speculativeReady, gateWait, gateHold)
			b.Logf("issue2065 selected-fetches=%v", selectedFetches)
		})
	}
}

type issue2065OfferDelayFamily struct {
	shamap.Family
	delayed      map[[32]byte]struct{}
	delay        time.Duration
	fetches      atomic.Int64
	selectedHits atomic.Int64
}

func (f *issue2065OfferDelayFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
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

func newIssue2065OfferBenchmarkService(
	b *testing.B,
	fixture *benchmarkRefreshFixture,
	logger log.Logger,
	delay time.Duration,
	round int,
) (*Service, *ledger.Ledger, *issue2065OfferDelayFamily) {
	b.Helper()
	// Reuse the established persisted fixture and service startup path, then
	// replace its broad delay with an account-only delay. FeeSettings and the
	// state-map root stay warm, so the timed delay belongs to successor replay.
	svc, parent, _, _ := newConsensusAcceptanceBenchmarkService(b, fixture, logger, 0, round)
	root, err := parent.StateMapHash()
	require.NoError(b, err)
	base := svc.shamapFamily
	master := jtx.MasterAccount()
	accountPath := successorStatePath(b, root, base, keylet.Account(master.AccountID()).Key)
	feePath := successorStatePath(b, root, base, keylet.Fees().Key)
	feeNodes := make(map[[32]byte]struct{}, len(feePath))
	for _, hash := range feePath {
		feeNodes[hash] = struct{}{}
	}
	selected := make(map[[32]byte]struct{}, len(accountPath))
	for _, hash := range accountPath {
		if _, shared := feeNodes[hash]; !shared {
			selected[hash] = struct{}{}
		}
	}
	require.NotEmpty(b, selected, "OfferCreate account path must have a fee-independent node")
	delayed := &issue2065OfferDelayFamily{Family: base, delayed: selected, delay: delay}
	parent.SetStateMapFamily(delayed)
	return svc, parent, delayed
}

func issue2065OfferCreateBlob(b testing.TB, env *jtx.TestEnv) ([]byte, [32]byte) {
	b.Helper()
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	issuer := jtx.NewAccount("issue-2065-offer-issuer")
	transaction := offertest.OfferCreate(
		master,
		jtx.USD(issuer, 1),
		jtx.XRPTxAmountFromXRP(1),
	).Sequence(1).Build()
	env.SignWith(transaction, master)
	blob, err := tx.SerializeTransaction(transaction)
	if err != nil {
		b.Fatalf("serialize OfferCreate: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(transaction)
	if err != nil {
		b.Fatalf("hash OfferCreate: %v", err)
	}
	return blob, hash
}

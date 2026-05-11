package consensus

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	testenv "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// newConvergenceServiceAndAdaptor spins up an independent Service+Adaptor
// pair with UseIncrementalOpenLedger on. Each call builds a fresh genesis
// ledger and an isolated open view, so two pairs share no mutable state.
func newConvergenceServiceAndAdaptor(t *testing.T) (*service.Service, *adaptor.Adaptor) {
	t.Helper()
	cfg := service.Config{
		Standalone:               true,
		GenesisConfig:            genesis.DefaultConfig(),
		UseIncrementalOpenLedger: true,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	identity, err := adaptor.NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	if err != nil {
		t.Fatalf("NewValidatorIdentity: %v", err)
	}
	a := adaptor.New(adaptor.Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	return svc, a
}

// signedPaymentBlob signs and binary-encodes a Payment, returning the
// raw blob. Mirrors the helper used by the open-ledger service/adaptor
// tests but kept local so this test file stays self-contained.
func signedPaymentBlob(t *testing.T, env *testenv.TestEnv, sender, receiver *testenv.Account, dropsAmount uint64, senderSeq uint32) []byte {
	t.Helper()
	txn := payment.Pay(sender, receiver, dropsAmount).Sequence(senderSeq).Build()
	env.SignWith(txn, sender)
	txMap, err := txn.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

// fundAccountsAndClose funds nSenders accounts from the master account
// via SubmitOpenLedgerTx, then drives one consensus close so the funded
// accounts exist in the closed ledger of svc. Returns the senders and
// the destination, plus the closed-ledger sequence at which the senders
// were created (which becomes their initial AccountRoot.Sequence under
// featureDeletableAccounts and is therefore the sequence each sender's
// first payment must use).
func fundAccountsAndClose(
	t *testing.T,
	svc *service.Service,
	env *testenv.TestEnv,
	nSenders int,
) (senders []*testenv.Account, dest *testenv.Account, senderInitialSeq uint32) {
	t.Helper()
	env.SetVerifySignatures(true)
	master := testenv.MasterAccount()
	dest = testenv.NewAccount("convergence-dest")

	senders = make([]*testenv.Account, nSenders)
	// nSenders + 1 funding blobs: nSenders for the sender accounts plus
	// one for the destination. The destination must be funded above the
	// 200 XRP reserve up front; otherwise payments to it from the senders
	// would classify as TecNO_DST_INSUF_XRP under retry=true (Submit's
	// initial classification — see apply.applyAndClassify), which maps to
	// ResultRetry and is therefore dropped from the open view rather than
	// committed.
	fundingBlobs := make([][]byte, nSenders+1)
	for i := 0; i < nSenders; i++ {
		senders[i] = testenv.NewAccount(fmt.Sprintf("convergence-sender-%03d", i))
		// 1000 XRP per sender: well above the 200 XRP reserve, leaves
		// plenty of headroom for the payment + fee.
		fundingBlobs[i] = signedPaymentBlob(t, env, master, senders[i], 1_000_000_000, uint32(i+1))
	}
	// Fund the destination above the reserve so test-workload payments
	// are simple account-to-account transfers, not account creations.
	fundingBlobs[nSenders] = signedPaymentBlob(t, env, master, dest, 1_000_000_000, uint32(nSenders+1))

	for i, blob := range fundingBlobs {
		res, err := svc.SubmitOpenLedgerTx(blob)
		if err != nil {
			t.Fatalf("fund sender %d: SubmitOpenLedgerTx: %v", i, err)
		}
		if res != openledger.ResultSuccess {
			t.Fatalf("fund sender %d: result = %v, want Success", i, res)
		}
	}

	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger nil before AcceptConsensusResult")
	}
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, fundingBlobs, time.Now(), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}

	closed := svc.GetClosedLedger()
	if closed == nil {
		t.Fatal("GetClosedLedger nil after AcceptConsensusResult")
	}
	// featureDeletableAccounts is in the default supported preset, so
	// new accounts are created with Sequence = LedgerSequence at apply
	// time. The funding txs applied during the close that produced
	// `closed`, so the senders' starting sequence equals closed.Sequence().
	senderInitialSeq = closed.Sequence()
	return senders, dest, senderInitialSeq
}

// TestOpenLedger_ConvergenceUnderOrderShuffling is the unit-level proof of
// the #407 fix. Two independent Service+Adaptor pairs receive the SAME
// 100 payment blobs — one in canonical order, the other in a deterministic
// shuffle — and their resulting OpenLedgerTxs() sets must be identical.
//
// Design notes:
//
//   - The test uses 100 distinct sender accounts (one payment each) rather
//     than the 10x10 shape sketched in the issue body. OpenLedger.Submit
//     is single-shot: a tx that classifies as Retry (e.g. terPRE_SEQ from
//     arriving out-of-sequence) is dropped, not held for a later pass.
//     Within a single sender, chained sequences are therefore arrival-
//     order-sensitive at ingress. The order-independence property #407
//     fixes is about which set of INDEPENDENT txs ends up in the propose-
//     time view — not about reordering dependency chains, which is
//     handled separately by the consensus-close 3-pass ApplyTxs loop.
//
//   - Both services start from identical genesis configs and submit the
//     same byte-for-byte funding blobs in the same order before the test
//     workload begins. That keeps the post-funding state byte-identical
//     across A and B, which is the precondition for asking whether
//     shuffled ingress of the test workload converges.
//
//   - The 100 payments are independent: each comes from a distinct sender
//     account at its starting sequence (post-funding-close), targets a
//     single shared destination, and carries a unique drops amount so
//     blobs are all distinct.
//
// Refs: #407.
func TestOpenLedger_ConvergenceUnderOrderShuffling(t *testing.T) {
	const (
		nPayments = 100
		rngSeed   = int64(0xC07407)
	)

	envA := testenv.NewTestEnv(t)
	envB := testenv.NewTestEnv(t)

	svcA, adA := newConvergenceServiceAndAdaptor(t)
	svcB, adB := newConvergenceServiceAndAdaptor(t)

	sendersA, destA, seqStartA := fundAccountsAndClose(t, svcA, envA, nPayments)
	sendersB, destB, seqStartB := fundAccountsAndClose(t, svcB, envB, nPayments)

	if seqStartA != seqStartB {
		t.Fatalf("post-funding starting sequence diverges: A=%d B=%d (state mismatch breaks the test premise)", seqStartA, seqStartB)
	}
	for i := range sendersA {
		if sendersA[i].ID != sendersB[i].ID {
			t.Fatalf("sender %d account ID diverges across envs (test premise broken)", i)
		}
	}
	if destA.ID != destB.ID {
		t.Fatal("destination account ID diverges across envs (test premise broken)")
	}

	blobsA := make([][]byte, nPayments)
	blobsB := make([][]byte, nPayments)
	for i := 0; i < nPayments; i++ {
		amount := uint64(2_000_000 + i*100)
		blobsA[i] = signedPaymentBlob(t, envA, sendersA[i], destA, amount, seqStartA)
		blobsB[i] = signedPaymentBlob(t, envB, sendersB[i], destB, amount, seqStartB)
		if !bytes.Equal(blobsA[i], blobsB[i]) {
			t.Fatalf("blob %d differs between envs (signing or mint is non-deterministic)", i)
		}
	}

	orderA := append([][]byte(nil), blobsA...)
	orderB := append([][]byte(nil), blobsB...)
	shuffleRNG := rand.New(rand.NewSource(rngSeed + 1))
	shuffleRNG.Shuffle(len(orderB), func(i, j int) {
		orderB[i], orderB[j] = orderB[j], orderB[i]
	})

	differs := false
	for i := range orderA {
		if !bytes.Equal(orderA[i], orderB[i]) {
			differs = true
			break
		}
	}
	if !differs {
		t.Fatal("shuffle produced identical order; pick a different seed (test would prove nothing)")
	}

	// Pre-flight: confirm one sender's account actually exists in svc's
	// view with the expected sequence. Failing this means the funding
	// close didn't land the accounts where we think it did, which would
	// make every test-workload submit fail and obscure the convergence
	// question.
	if info, err := svcA.GetAccountInfo(sendersA[0].Address, "current"); err != nil {
		t.Fatalf("pre-flight GetAccountInfo(sender[0]) err = %v (account missing post-funding-close)", err)
	} else if info.Sequence != seqStartA {
		t.Fatalf("pre-flight sender[0].Sequence = %d, want %d (DeletableAccounts assumption wrong; test must use info.Sequence as the payment seq)", info.Sequence, seqStartA)
	}

	for _, b := range orderA {
		adA.AddPendingTx(b)
	}
	for _, b := range orderB {
		adB.AddPendingTx(b)
	}

	gotA := svcA.OpenLedgerTxs()
	gotB := svcB.OpenLedgerTxs()

	sortBlobs := func(blobs [][]byte) {
		sort.Slice(blobs, func(i, j int) bool {
			return bytes.Compare(blobs[i], blobs[j]) < 0
		})
	}
	sortBlobs(gotA)
	sortBlobs(gotB)

	if len(gotA) != len(gotB) {
		t.Fatalf("convergence failed on cardinality: |A|=%d |B|=%d (want %d on both)", len(gotA), len(gotB), nPayments)
	}
	if len(gotA) != nPayments {
		t.Fatalf("OpenLedgerTxs cardinality = %d, want %d (some payments did not apply — investigate before treating this as a convergence claim)", len(gotA), nPayments)
	}
	for i := range gotA {
		if !bytes.Equal(gotA[i], gotB[i]) {
			t.Errorf("blob %d diverges between A and B", i)
		}
	}
	if t.Failed() {
		return
	}

	// Defence-in-depth: also confirm both adaptors' propose-time output
	// agrees byte-for-byte. Catches a regression where GetProposableTxs
	// drifts from OpenLedgerTxs() (e.g. a filter pass re-introduced).
	proposeA := adA.GetProposableTxs(adaptor.WrapLedger(svcA.GetClosedLedger()))
	proposeB := adB.GetProposableTxs(adaptor.WrapLedger(svcB.GetClosedLedger()))
	sortBlobs(proposeA)
	sortBlobs(proposeB)
	if len(proposeA) != len(proposeB) {
		t.Fatalf("GetProposableTxs cardinality diverges: |A|=%d |B|=%d", len(proposeA), len(proposeB))
	}
	for i := range proposeA {
		if !bytes.Equal(proposeA[i], proposeB[i]) {
			t.Errorf("propose-time blob %d diverges between A and B", i)
		}
	}
}

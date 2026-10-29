package openledger_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type nftOfferAccountState struct {
	Balance    uint64
	Sequence   uint32
	OwnerCount uint32
}

func readNFTokenOfferAccount(t *testing.T, view *ledger.Ledger, account *jtx.Account) nftOfferAccountState {
	t.Helper()
	data, err := view.Read(keylet.Account(account.ID))
	if err != nil {
		t.Fatalf("read %s account: %v", account.Name, err)
	}
	parsed, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("parse %s account: %v", account.Name, err)
	}
	return nftOfferAccountState{
		Balance:    parsed.Balance,
		Sequence:   parsed.Sequence,
		OwnerCount: parsed.OwnerCount,
	}
}

func readNFTokenOfferMetadata(t *testing.T, view *ledger.Ledger, hash [32]byte) []byte {
	t.Helper()
	stored, found, err := view.GetTransaction(hash)
	if err != nil || !found {
		t.Fatalf("GetTransaction(%x): found=%v err=%v", hash, found, err)
	}
	_, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
	if err != nil {
		t.Fatalf("SplitTxWithMetaBlobStrict(%x): %v", hash, err)
	}
	return append([]byte(nil), metadata...)
}

func newOpenLedgerFromParent(t *testing.T, parent *ledger.Ledger) *ledger.Ledger {
	t.Helper()
	view, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return view
}

func applyOpenLedgerTransactions(t *testing.T, view *ledger.Ledger, pending ...openledger.PendingTx) {
	t.Helper()
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, pending, &retries, openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		ParentCloseTime:  100,
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.BuildLedgerMode,
	}); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("leftover retries = %d", len(retries))
	}
}

type nftOfferIndexWorkload struct {
	parent         *ledger.Ledger
	local          []openledger.PendingTx
	canonical      []openledger.PendingTx
	hashes         [][32]byte
	expiredHash    [32]byte
	canonicalIndex uint32
}

func buildSimpleNFTokenOfferIndexWorkload(t *testing.T, localIndex, canonicalIndex int) nftOfferIndexWorkload {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("index-offer-alice")
	bob := jtx.NewAccount("index-offer-bob")
	carol := jtx.NewAccount("index-offer-carol")
	env.Fund(alice, bob, carol)
	env.Close()
	nftID := nft.GetNextNFTokenID(env, alice, 0, 0, 0)
	if result := env.Submit(nft.NFTokenMint(alice, 0).Build()); !result.Success {
		t.Fatalf("mint: %s", result.Code)
	}
	env.Close()
	parent := env.LastClosedLedger()
	offer := nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1)).
		Expiration(1).Sequence(env.Seq(alice)).Build()
	offerPending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, offer, alice))
	if err != nil {
		t.Fatalf("ParsePendingTx offer: %v", err)
	}

	payments := make([]openledger.PendingTx, canonicalIndex)
	startSequence := env.Seq(bob)
	for i := range payments {
		payments[i] = pendingPayment(
			t,
			env,
			payment.Pay(bob, carol, 1_000_000).Sequence(startSequence+uint32(i)),
			bob,
		)
	}
	local := make([]openledger.PendingTx, 0, canonicalIndex+1)
	local = append(local, payments[:localIndex]...)
	local = append(local, offerPending)
	local = append(local, payments[localIndex:]...)
	canonical := append([]openledger.PendingTx(nil), payments...)
	canonical = append(canonical, offerPending)
	hashes := make([][32]byte, 0, len(local))
	for _, pending := range local {
		hashes = append(hashes, pending.Hash)
	}
	return nftOfferIndexWorkload{
		parent:         parent,
		local:          local,
		canonical:      canonical,
		hashes:         hashes,
		expiredHash:    offerPending.Hash,
		canonicalIndex: uint32(canonicalIndex),
	}
}

func buildBatchNFTokenOfferIndexWorkload(t *testing.T) nftOfferIndexWorkload {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	env.EnableFeatureNow("BatchV1_1")
	alice := jtx.NewAccount("index-batch-offer-alice")
	bob := jtx.NewAccount("index-batch-offer-bob")
	carol := jtx.NewAccount("index-batch-offer-carol")
	batchBeforeAccount := jtx.NewAccount("index-batch-offer-before")
	batchAfterAccount := jtx.NewAccount("index-batch-offer-after")
	batchSink := jtx.NewAccount("index-batch-offer-sink")
	env.Fund(alice, bob, carol, batchBeforeAccount, batchAfterAccount, batchSink)
	env.Close()
	nftID := nft.GetNextNFTokenID(env, alice, 0, 0, 0)
	if result := env.Submit(nft.NFTokenMint(alice, 0).Build()); !result.Success {
		t.Fatalf("mint: %s", result.Code)
	}
	env.Close()
	parent := env.LastClosedLedger()
	offer := nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1)).
		Expiration(1).Sequence(env.Seq(alice)).Build()
	offerPending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, offer, alice))
	if err != nil {
		t.Fatalf("ParsePendingTx offer: %v", err)
	}

	startSequence := env.Seq(bob)
	beforePayments := make([]openledger.PendingTx, 3)
	for i := range beforePayments {
		beforePayments[i] = pendingPayment(
			t,
			env,
			payment.Pay(bob, carol, 1_000_000).Sequence(startSequence+uint32(i)),
			bob,
		)
	}
	afterPayments := make([]openledger.PendingTx, 16)
	for i := range afterPayments {
		afterPayments[i] = pendingPayment(
			t,
			env,
			payment.Pay(bob, carol, 1_000_000).Sequence(startSequence+3+uint32(i)),
			bob,
		)
	}

	buildBatch := func(account *jtx.Account, destination *jtx.Account) (*batchtx.Batch, openledger.PendingTx) {
		sequence := env.Seq(account)
		batch := batchtest.NewBatchBuilder(account, sequence, batchtest.CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
			AddInnerTx(batchtest.MakeInnerPaymentXRP(account, destination, 1, sequence+1)).
			AddInnerTx(batchtest.MakeInnerPaymentXRP(account, destination, 1, sequence+2)).
			MustBuild()
		pending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, batch, account))
		if err != nil {
			t.Fatalf("ParsePendingTx Batch: %v", err)
		}
		return batch, pending
	}
	batchBefore, batchBeforePending := buildBatch(batchBeforeAccount, batchSink)
	batchAfter, batchAfterPending := buildBatch(batchAfterAccount, batchSink)

	local := make([]openledger.PendingTx, 0, 22)
	local = append(local, beforePayments...)
	local = append(local, batchBeforePending, offerPending)
	local = append(local, afterPayments...)
	local = append(local, batchAfterPending)
	canonical := make([]openledger.PendingTx, 0, 22)
	canonical = append(canonical, beforePayments...)
	canonical = append(canonical, batchBeforePending)
	canonical = append(canonical, afterPayments...)
	canonical = append(canonical, batchAfterPending, offerPending)
	hashes := make([][32]byte, 0, 26)
	for _, pending := range local {
		hashes = append(hashes, pending.Hash)
	}
	for _, batch := range []*batchtx.Batch{batchBefore, batchAfter} {
		for _, inner := range batch.RawTransactions {
			hash, err := tx.ComputeTransactionHash(inner.RawTransaction.InnerTx)
			if err != nil {
				t.Fatalf("ComputeTransactionHash inner: %v", err)
			}
			hashes = append(hashes, hash)
		}
	}
	return nftOfferIndexWorkload{
		parent:         parent,
		local:          local,
		canonical:      canonical,
		hashes:         hashes,
		expiredHash:    offerPending.Hash,
		canonicalIndex: 25,
	}
}

func TestNFTokenCreateOfferIncidentIndexWorkloads(t *testing.T) {
	tests := []struct {
		name           string
		localIndex     int
		canonicalIndex int
	}{
		{name: "21846175", localIndex: 12, canonicalIndex: 16},
		{name: "21846353", localIndex: 21, canonicalIndex: 42},
		{name: "21847219", localIndex: 15, canonicalIndex: 16},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workload := buildSimpleNFTokenOfferIndexWorkload(t, test.localIndex, test.canonicalIndex)
			assertNFTokenOfferIndexWorkload(t, workload)
		})
	}
	t.Run("21848997", func(t *testing.T) {
		assertNFTokenOfferIndexWorkload(t, buildBatchNFTokenOfferIndexWorkload(t))
	})
}

func assertNFTokenOfferIndexWorkload(t *testing.T, workload nftOfferIndexWorkload) {
	t.Helper()
	actual := newOpenLedgerFromParent(t, workload.parent)
	canonical := newOpenLedgerFromParent(t, workload.parent)
	applyOpenLedgerTransactions(t, actual, workload.local...)
	applyOpenLedgerTransactions(t, canonical, workload.canonical...)
	if got, want := actual.TxCount(), workload.canonicalIndex+1; got != want {
		t.Fatalf("transaction count = %d, want %d", got, want)
	}
	offerIndex, offerResult, _ := transactionMetadata(t, actual, workload.expiredHash)
	if offerIndex != workload.canonicalIndex || offerResult != ter.TecEXPIRED.String() {
		t.Fatalf("expired offer metadata = index %d result %s, want index %d tecEXPIRED", offerIndex, offerResult, workload.canonicalIndex)
	}
	for _, hash := range workload.hashes {
		got, want := readNFTokenOfferMetadata(t, actual, hash), readNFTokenOfferMetadata(t, canonical, hash)
		if !bytes.Equal(got, want) {
			t.Fatalf("metadata %x differs from canonical order:\n got  %x\n want %x", hash, got, want)
		}
	}
	stateHash, err := actual.StateMapHash()
	if err != nil {
		t.Fatalf("actual StateMapHash: %v", err)
	}
	canonicalStateHash, err := canonical.StateMapHash()
	if err != nil {
		t.Fatalf("canonical StateMapHash: %v", err)
	}
	if stateHash != canonicalStateHash {
		t.Fatalf("state root differs from canonical order: got %x want %x", stateHash, canonicalStateHash)
	}
	txHash, err := actual.TxMapHash()
	if err != nil {
		t.Fatalf("actual TxMapHash: %v", err)
	}
	canonicalTxHash, err := canonical.TxMapHash()
	if err != nil {
		t.Fatalf("canonical TxMapHash: %v", err)
	}
	if txHash != canonicalTxHash {
		t.Fatalf("transaction root differs from canonical order: got %x want %x", txHash, canonicalTxHash)
	}
	closeTime := workload.parent.CloseTime().Add(time.Second)
	if err := actual.Close(closeTime, 0); err != nil {
		t.Fatalf("close actual view: %v", err)
	}
	if err := canonical.Close(closeTime, 0); err != nil {
		t.Fatalf("close canonical view: %v", err)
	}
	if actual.Hash() != canonical.Hash() {
		t.Fatalf("ledger hash differs from canonical order: got %x want %x", actual.Hash(), canonical.Hash())
	}
}

// An expired NFTokenCreateOffer must return tecEXPIRED from Preclaim. This
// keeps the retry pass from claiming its fee or advancing the transaction map.
func TestApplyTxs_ExpiredNFTokenCreateOfferDefersFeeClaim(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("expired-offer-alice")
	bob := jtx.NewAccount("expired-offer-bob")
	carol := jtx.NewAccount("expired-offer-carol")
	env.Fund(alice, bob, carol)
	env.Close()

	nftID := nft.GetNextNFTokenID(env, alice, 0, 0, 0)
	if result := env.Submit(nft.NFTokenMint(alice, 0).Build()); !result.Success {
		t.Fatalf("mint: %s", result.Code)
	}
	env.Close()

	view := freshView(t, env)
	parent := env.LastClosedLedger()
	offerSequence := env.Seq(alice)
	offer := nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1)).
		Expiration(1).Sequence(offerSequence).Build()
	offerPending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, offer, alice))
	if err != nil {
		t.Fatalf("ParsePendingTx offer: %v", err)
	}
	paymentPending := pendingPayment(
		t,
		env,
		payment.Pay(bob, carol, 1_000_000).Sequence(env.Seq(bob)),
		bob,
	)

	// Apply the first pass directly to prove the transaction has no staged
	// effects before the final fee-claim pass.
	retryView := newOpenLedgerFromParent(t, parent)
	beforeRetry := readNFTokenOfferAccount(t, retryView, alice)
	parsedOffer, err := tx.ParseFromBinary(offerPending.Blob)
	if err != nil {
		t.Fatalf("ParseFromBinary offer: %v", err)
	}
	parsedOffer.SetRawBytes(offerPending.Blob)
	config := tx.EngineConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   retryView.Sequence(),
		ParentCloseTime:  100,
		ParentHash:       retryView.ParentHash(),
		Rules:            amendment.AllSupportedRules(),
		ApplyFlags:       tx.TapRETRY,
	}
	engine := txengine.NewEngine(retryView, config)
	engine.SetBaseTxCount(retryView.TxCount())
	retryResult, err := txengine.NewBlockProcessor(engine).ApplyLedgerTransaction(parsedOffer, offerPending.Blob)
	if err != nil {
		t.Fatalf("retry ApplyLedgerTransaction: %v", err)
	}
	if retryResult.ApplyResult.Result != ter.TecEXPIRED {
		t.Fatalf("retry result = %s, want tecEXPIRED", retryResult.ApplyResult.Result)
	}
	if retryResult.ApplyResult.Applied || retryResult.ApplyResult.Fee != 0 || retryResult.ApplyResult.Metadata != nil {
		t.Fatalf("retry committed effects: %+v", retryResult.ApplyResult)
	}
	if engine.TxCount() != retryView.TxCount() || ledgerTxExists(t, retryView, offerPending.Hash) {
		t.Fatalf("retry committed transaction: engine count=%d view count=%d", engine.TxCount(), retryView.TxCount())
	}
	if afterRetry := readNFTokenOfferAccount(t, retryView, alice); afterRetry != beforeRetry {
		t.Fatalf("alice changed during retry: before=%+v after=%+v", beforeRetry, afterRetry)
	}

	applyOpenLedgerTransactions(t, view, offerPending, paymentPending)

	if got, want := view.TxCount(), uint32(2); got != want {
		t.Fatalf("transaction count = %d, want %d", got, want)
	}
	offerIndex, offerResult, offerNodes := transactionMetadata(t, view, offerPending.Hash)
	if offerIndex != 1 || offerResult != ter.TecEXPIRED.String() {
		t.Fatalf("offer metadata = index %d result %s, want index 1 tecEXPIRED", offerIndex, offerResult)
	}
	if len(offerNodes) != 1 || offerNodes[0].NodeType != "ModifiedNode" || offerNodes[0].LedgerEntryType != "AccountRoot" {
		t.Fatalf("expired offer fee metadata nodes = %+v, want only source AccountRoot", offerNodes)
	}
	paymentIndex, paymentResult, _ := transactionMetadata(t, view, paymentPending.Hash)
	if paymentIndex != 0 || paymentResult != ter.TesSUCCESS.String() {
		t.Fatalf("payment metadata = index %d result %s, want index 0 tesSUCCESS", paymentIndex, paymentResult)
	}
	after := readNFTokenOfferAccount(t, view, alice)
	if after.Balance != beforeRetry.Balance-10 || after.Sequence != beforeRetry.Sequence+1 || after.OwnerCount != beforeRetry.OwnerCount {
		t.Fatalf("final offer fee claim account = %+v, want balance %d sequence %d owner count %d", after, beforeRetry.Balance-10, beforeRetry.Sequence+1, beforeRetry.OwnerCount)
	}
	if data, err := view.Read(keylet.NFTokenOffer(alice.ID, offerSequence)); err != nil || data != nil {
		t.Fatalf("expired offer ledger entry = %x, err=%v; want absent", data, err)
	}
}

func TestApplyTxs_ExpiredNFTokenCreateOfferWithTicketDefersTicketClaim(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("ticket-expired-offer-alice")
	env.Fund(alice)
	env.Close()

	ticketSequence := env.CreateTickets(alice, 1)
	env.Close()
	nftID := nft.GetNextNFTokenID(env, alice, 0, 0, 0)
	if result := env.Submit(nft.NFTokenMint(alice, 0).Build()); !result.Success {
		t.Fatalf("mint: %s", result.Code)
	}
	env.Close()

	view := freshView(t, env)
	parent := env.LastClosedLedger()
	offer := nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1)).Expiration(1).Build()
	jtx.WithTicketSeq(offer, ticketSequence)
	offerPending, err := openledger.ParsePendingTx(buildSignedBlob(t, env, offer, alice))
	if err != nil {
		t.Fatalf("ParsePendingTx offer: %v", err)
	}
	before := readNFTokenOfferAccount(t, view, alice)
	if data, err := view.Read(keylet.Ticket(alice.ID, ticketSequence)); err != nil || data == nil {
		t.Fatalf("ticket missing before retry: data=%x err=%v", data, err)
	}

	retryView := newOpenLedgerFromParent(t, parent)
	parsedOffer, err := tx.ParseFromBinary(offerPending.Blob)
	if err != nil {
		t.Fatalf("ParseFromBinary offer: %v", err)
	}
	parsedOffer.SetRawBytes(offerPending.Blob)
	config := tx.EngineConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   retryView.Sequence(),
		ParentCloseTime:  100,
		ParentHash:       retryView.ParentHash(),
		Rules:            amendment.AllSupportedRules(),
		ApplyFlags:       tx.TapRETRY,
	}
	retryResult, err := txengine.NewBlockProcessor(txengine.NewEngine(retryView, config)).ApplyLedgerTransaction(parsedOffer, offerPending.Blob)
	if err != nil {
		t.Fatalf("retry ApplyLedgerTransaction: %v", err)
	}
	if retryResult.ApplyResult.Result != ter.TecEXPIRED || retryResult.ApplyResult.Applied || retryResult.ApplyResult.Fee != 0 {
		t.Fatalf("ticket retry result = %+v, want deferred tecEXPIRED", retryResult.ApplyResult)
	}
	if got := readNFTokenOfferAccount(t, retryView, alice); got != before {
		t.Fatalf("ticket offer changed account during retry: before=%+v after=%+v", before, got)
	}
	if data, err := retryView.Read(keylet.Ticket(alice.ID, ticketSequence)); err != nil || data == nil {
		t.Fatalf("ticket consumed during retry: data=%x err=%v", data, err)
	}

	applyOpenLedgerTransactions(t, view, offerPending)
	index, result, _ := transactionMetadata(t, view, offerPending.Hash)
	if index != 0 || result != ter.TecEXPIRED.String() {
		t.Fatalf("ticket offer metadata = index %d result %s, want index 0 tecEXPIRED", index, result)
	}
	after := readNFTokenOfferAccount(t, view, alice)
	if after.Balance != before.Balance-10 || after.Sequence != before.Sequence || after.OwnerCount != before.OwnerCount-1 {
		t.Fatalf("ticket fee claim account = %+v, want balance %d sequence %d owner count %d", after, before.Balance-10, before.Sequence, before.OwnerCount-1)
	}
	if data, err := view.Read(keylet.Ticket(alice.ID, ticketSequence)); err != nil || data != nil {
		t.Fatalf("ticket remains after final pass: data=%x err=%v", data, err)
	}
}

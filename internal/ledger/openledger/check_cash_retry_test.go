package openledger_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestApplyTxs_ExpiredCheckCashDefersFeeClaim(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	writer := jtx.NewAccount("expired-check-writer")
	casher := jtx.NewAccount("expired-check-casher")
	sender := jtx.NewAccount("expired-check-sender")
	receiver := jtx.NewAccount("expired-check-receiver")
	env.Fund(writer, casher, sender, receiver)

	checkSequence := env.Seq(writer)
	expiration := env.NowRipple() + 20
	checkKey := keylet.Check(writer.ID, checkSequence)
	checkID := hex.EncodeToString(checkKey.Key[:])
	if result := env.Submit(checkbuilder.CheckCreate(
		writer,
		casher,
		tx.NewXRPAmount(jtx.XRP(10)),
	).Expiration(expiration).Build()); !result.Success {
		t.Fatalf("CheckCreate: %s", result.Code)
	}

	// Pin the <= expiry boundary at the parent close time.
	env.CloseToParentCloseTime(expiration)
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no closed parent")
	}
	view, err := ledger.NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	parentCloseTime := protocol.ToRippleTime(view.ParentCloseTime())
	if parentCloseTime != expiration {
		t.Fatalf("parent close time = %d, want expiration %d", parentCloseTime, expiration)
	}

	casherSequence := env.Seq(casher)
	cash := checkbuilder.CheckCashAmount(casher, checkID, tx.NewXRPAmount(jtx.XRP(10))).Build()
	cash.Fee = strconv.FormatUint(env.BaseFee(), 10)
	cash.SetSequence(casherSequence)
	cashBlob := buildSignedBlob(t, env, cash, casher)
	cashPending, err := openledger.ParsePendingTx(cashBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx CheckCash: %v", err)
	}

	// TapRETRY must defer tecEXPIRED without committing state.
	retryView, err := ledger.NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("ledger.NewOpen retry view: %v", err)
	}
	casherBeforeRetry := readCheckCashAccount(t, retryView, casher)
	checkBeforeRetry := readCheckCashEntry(t, retryView, checkKey)
	parsedCash, err := tx.ParseFromBinary(cashPending.Blob)
	if err != nil {
		t.Fatalf("ParseFromBinary CheckCash: %v", err)
	}
	parsedCash.SetRawBytes(cashPending.Blob)
	engineConfig := tx.EngineConfig{
		BaseFee:          env.BaseFee(),
		ReserveBase:      env.ReserveBase(),
		ReserveIncrement: env.ReserveIncrement(),
		LedgerSequence:   retryView.Sequence(),
		ParentCloseTime:  parentCloseTime,
		ParentHash:       retryView.ParentHash(),
		Rules:            amendment.AllSupportedRules(),
		ApplyFlags:       tx.TapRETRY,
	}
	engine := txengine.NewEngine(retryView, engineConfig)
	for _, failKey := range []keylet.Keylet{checkKey, keylet.Account(writer.ID)} {
		storageErr := errors.New("check storage unavailable")
		faultView := &checkCashReadErrorView{Ledger: retryView, failKey: failKey.Key, err: storageErr}
		for _, flags := range []tx.ApplyFlags{tx.TapRETRY, tx.TapNONE} {
			faultConfig := engineConfig
			faultConfig.ApplyFlags = flags
			result := txengine.NewEngine(faultView, faultConfig).Apply(parsedCash)
			if result.Result != ter.TefEXCEPTION || !errors.Is(result.Cause, storageErr) {
				t.Fatalf("read fault %x, flags %v: result=%s cause=%v", failKey.Key, flags, result.Result, result.Cause)
			}
			if result.Applied || result.Fee != 0 || result.Metadata != nil {
				t.Fatalf("read fault committed effects: %+v", result)
			}
		}
	}
	engine.SetBaseTxCount(retryView.TxCount())
	retryResult, err := txengine.NewBlockProcessor(engine).ApplyLedgerTransaction(parsedCash, cashPending.Blob)
	if err != nil {
		t.Fatalf("retry ApplyLedgerTransaction: %v", err)
	}
	if retryResult.ApplyResult.Result != ter.TecEXPIRED {
		t.Fatalf("retry result = %s, want tecEXPIRED", retryResult.ApplyResult.Result)
	}
	if retryResult.ApplyResult.Applied {
		t.Fatal("expired CheckCash applied during retry pass")
	}
	if retryResult.ApplyResult.Fee != 0 || retryResult.ApplyResult.Metadata != nil {
		t.Fatalf("retry fee/metadata = %d/%v, want 0/nil", retryResult.ApplyResult.Fee, retryResult.ApplyResult.Metadata)
	}
	if engine.TxCount() != 0 || retryView.TxCount() != 0 || ledgerTxExists(t, retryView, cashPending.Hash) {
		t.Fatalf("retry committed transaction: engine count=%d view count=%d", engine.TxCount(), retryView.TxCount())
	}
	casherAfterRetry := readCheckCashAccount(t, retryView, casher)
	if casherAfterRetry != casherBeforeRetry {
		t.Fatalf("casher changed during retry: before=%+v after=%+v", casherBeforeRetry, casherAfterRetry)
	}
	if got := readCheckCashEntry(t, retryView, checkKey); !bytes.Equal(got, checkBeforeRetry) {
		t.Fatal("check entry changed during retry")
	}

	// CheckCash is first in input order; the payment must still commit at index 0.
	paymentPending := pendingPayment(
		t,
		env,
		payment.Pay(sender, receiver, 1_000_000).Sequence(env.Seq(sender)),
		sender,
	)
	casherBefore := readCheckCashAccount(t, view, casher)
	checkBefore := readCheckCashEntry(t, view, checkKey)
	writerAccountBefore := readRawLedgerEntry(t, view, keylet.Account(writer.ID), "writer account")
	writerDirectoryBefore := readRawLedgerEntry(t, view, keylet.OwnerDir(writer.ID), "writer directory")
	casherDirectoryBefore := readRawLedgerEntry(t, view, keylet.OwnerDir(casher.ID), "casher directory")
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{cashPending, paymentPending}, &retries, openledger.ApplyConfig{
		BaseFee:          env.BaseFee(),
		ReserveBase:      env.ReserveBase(),
		ReserveIncrement: env.ReserveIncrement(),
		LedgerSequence:   view.Sequence(),
		ParentCloseTime:  parentCloseTime,
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.BuildLedgerMode,
	}); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("retries = %d, want 0", len(retries))
	}
	if got, want := view.TxCount(), uint32(2); got != want {
		t.Fatalf("committed transaction count = %d, want %d", got, want)
	}

	paymentIndex, paymentResult, _ := transactionMetadata(t, view, paymentPending.Hash)
	if paymentIndex != 0 || paymentResult != ter.TesSUCCESS.String() {
		t.Fatalf("payment metadata = index %d result %s, want index 0 tesSUCCESS", paymentIndex, paymentResult)
	}
	cashIndex, cashResult, cashNodes := transactionMetadata(t, view, cashPending.Hash)
	if cashIndex != 1 || cashResult != ter.TecEXPIRED.String() {
		t.Fatalf("CheckCash metadata = index %d result %s, want index 1 tecEXPIRED", cashIndex, cashResult)
	}
	casherAccountKey := keylet.Account(casher.ID)
	if len(cashNodes) != 1 || cashNodes[0].NodeType != "ModifiedNode" ||
		cashNodes[0].LedgerEntryType != "AccountRoot" ||
		!strings.EqualFold(cashNodes[0].LedgerIndex, hex.EncodeToString(casherAccountKey.Key[:])) {
		t.Fatalf("CheckCash fee metadata nodes = %+v, want only casher AccountRoot", cashNodes)
	}
	casherAfter := readCheckCashAccount(t, view, casher)
	if casherAfter.Balance != casherBefore.Balance-env.BaseFee() || casherAfter.Sequence != casherBefore.Sequence+1 {
		t.Fatalf("final CheckCash fee claim account = %+v, want balance %d sequence %d", casherAfter, casherBefore.Balance-env.BaseFee(), casherBefore.Sequence+1)
	}
	if got := readCheckCashEntry(t, view, checkKey); !bytes.Equal(got, checkBefore) {
		t.Fatal("expired CheckCash removed or modified the check entry")
	}
	if got := readRawLedgerEntry(t, view, keylet.Account(writer.ID), "writer account"); !bytes.Equal(got, writerAccountBefore) {
		t.Fatal("expired CheckCash changed the check writer account")
	}
	if got := readRawLedgerEntry(t, view, keylet.OwnerDir(writer.ID), "writer directory"); !bytes.Equal(got, writerDirectoryBefore) {
		t.Fatal("expired CheckCash changed the check writer owner directory")
	}
	if got := readRawLedgerEntry(t, view, keylet.OwnerDir(casher.ID), "casher directory"); !bytes.Equal(got, casherDirectoryBefore) {
		t.Fatal("expired CheckCash changed the destination owner directory")
	}
}

type checkCashAccountState struct {
	Balance  uint64
	Sequence uint32
}

func readCheckCashAccount(t *testing.T, view *ledger.Ledger, account *jtx.Account) checkCashAccountState {
	t.Helper()
	data, err := view.Read(keylet.Account(account.ID))
	if err != nil {
		t.Fatalf("read account %s: %v", account.Name, err)
	}
	if data == nil {
		t.Fatalf("account %s is missing", account.Name)
	}
	parsed, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("parse account %s: %v", account.Name, err)
	}
	return checkCashAccountState{Balance: parsed.Balance, Sequence: parsed.Sequence}
}

func readCheckCashEntry(t *testing.T, view *ledger.Ledger, key keylet.Keylet) []byte {
	return readRawLedgerEntry(t, view, key, "Check entry")
}

func readRawLedgerEntry(t *testing.T, view *ledger.Ledger, key keylet.Keylet, description string) []byte {
	t.Helper()
	data, err := view.Read(key)
	if err != nil {
		t.Fatalf("read %s: %v", description, err)
	}
	if data == nil {
		t.Fatalf("%s is missing", description)
	}
	return append([]byte(nil), data...)
}

type metadataNode struct {
	NodeType        string
	LedgerEntryType string
	LedgerIndex     string
}

func transactionMetadata(t *testing.T, view *ledger.Ledger, hash [32]byte) (uint32, string, []metadataNode) {
	t.Helper()
	data, found, err := view.GetTransaction(hash)
	if err != nil || !found {
		t.Fatalf("GetTransaction(%x): found=%v err=%v", hash, found, err)
	}
	_, metadata, err := tx.SplitTxWithMetaBlobStrict(data)
	if err != nil {
		t.Fatalf("SplitTxWithMetaBlobStrict(%x): %v", hash, err)
	}
	index, ok := tx.TransactionIndexFromMetadata(metadata)
	if !ok {
		t.Fatalf("transaction %x has no metadata index", hash)
	}
	fields, err := binarycodec.DecodeBytes(metadata)
	if err != nil {
		t.Fatalf("DecodeBytes metadata %x: %v", hash, err)
	}
	result, ok := fields["TransactionResult"].(string)
	if !ok {
		t.Fatalf("transaction %x metadata result has type %T", hash, fields["TransactionResult"])
	}
	nodesValue, ok := fields["AffectedNodes"].([]any)
	if !ok {
		t.Fatalf("transaction %x metadata affected nodes have type %T", hash, fields["AffectedNodes"])
	}
	nodes := make([]metadataNode, 0, len(nodesValue))
	for _, value := range nodesValue {
		nodeMap, ok := value.(map[string]any)
		if !ok || len(nodeMap) != 1 {
			t.Fatalf("transaction %x has malformed affected node %T", hash, value)
		}
		for nodeType, innerValue := range nodeMap {
			inner, ok := innerValue.(map[string]any)
			if !ok {
				t.Fatalf("transaction %x affected node %s has type %T", hash, nodeType, innerValue)
			}
			entryType, ok := inner["LedgerEntryType"].(string)
			if !ok {
				t.Fatalf("transaction %x affected node %s has LedgerEntryType %T", hash, nodeType, inner["LedgerEntryType"])
			}
			ledgerIndex, ok := inner["LedgerIndex"].(string)
			if !ok {
				t.Fatalf("transaction %x affected node %s has LedgerIndex %T", hash, nodeType, inner["LedgerIndex"])
			}
			nodes = append(nodes, metadataNode{
				NodeType:        nodeType,
				LedgerEntryType: entryType,
				LedgerIndex:     ledgerIndex,
			})
		}
	}
	return index, result, nodes
}

type checkCashReadErrorView struct {
	*ledger.Ledger
	failKey [32]byte
	err     error
}

func (v *checkCashReadErrorView) Read(key keylet.Keylet) ([]byte, error) {
	if key.Key == v.failKey {
		return nil, v.err
	}
	return v.Ledger.Read(key)
}

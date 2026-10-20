package engine_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	txpseudo "github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

var pseudoRegisterOnce sync.Once

func registerPseudo() {
	pseudoRegisterOnce.Do(txpseudo.Register)
}

func pseudoWireTransaction(t *testing.T, includeEmptySigners bool) (txcore.Transaction, []byte) {
	t.Helper()
	registerPseudo()
	fields := map[string]any{
		"TransactionType": "EnableAmendment",
		"Account":         protocol.ZeroAccount,
		"Sequence":        uint32(0),
		"Fee":             "0",
		"SigningPubKey":   "",
		"LedgerSequence":  uint32(1),
		"Amendment":       "0000000000000000000000000000000000000000000000000000000000000001",
	}
	if includeEmptySigners {
		fields["Signers"] = []map[string]any{}
	}

	raw, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("EncodeBytes: %v", err)
	}
	parsed, err := txcore.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	canonical, err := txcore.SerializeTransaction(parsed)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	parsed, err = txcore.ParseFromBinary(canonical)
	if err != nil {
		t.Fatalf("ParseFromBinary after SerializeTransaction: %v", err)
	}
	return parsed, canonical
}

func pseudoLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	genesisResult, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent, err := ledger.FromGenesis(genesisResult.Header, genesisResult.StateMap, genesisResult.TxMap, drops.Fees{})
	if err != nil {
		t.Fatalf("ledger.FromGenesis: %v", err)
	}
	view, err := ledger.NewOpen(parent, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return view
}

func pseudoEngine(t *testing.T, view *ledger.Ledger, configure func(*txcore.EngineConfig)) *txengine.Engine {
	t.Helper()
	config := txcore.EngineConfig{
		LedgerSequence:            view.Sequence(),
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	}
	if configure != nil {
		configure(&config)
	}
	return txengine.NewEngine(view, config)
}

func amendmentsBytes(t *testing.T, view *ledger.Ledger) []byte {
	t.Helper()
	data, err := view.Read(keylet.Amendments())
	if err != nil {
		t.Fatalf("read Amendments SLE: %v", err)
	}
	return data
}

func applyPseudoWire(t *testing.T, transaction txcore.Transaction, blob []byte, configure func(*txcore.EngineConfig)) (txcore.ApplyResult, []byte, []byte) {
	t.Helper()
	view := pseudoLedger(t)
	before := amendmentsBytes(t, view)
	result, err := txengine.NewBlockProcessor(pseudoEngine(t, view, configure)).ApplyLedgerTransaction(transaction, blob)
	if err != nil {
		t.Fatalf("ApplyLedgerTransaction: %v", err)
	}
	after := amendmentsBytes(t, view)
	return result.ApplyResult, before, after
}

func TestPseudoWire_EmptySignersRejectedWithoutMutation(t *testing.T) {
	transaction, blob := pseudoWireTransaction(t, true)
	if !transaction.GetCommon().HasField("Signers") {
		t.Fatal("binary round-trip lost explicit empty Signers presence")
	}

	result, before, after := applyPseudoWire(t, transaction, blob, nil)
	if result.Applied {
		t.Fatalf("empty Signers pseudo transaction applied: %+v", result)
	}
	if result.Result != ter.TemBAD_SIGNATURE {
		t.Fatalf("empty Signers result = %s, want temBAD_SIGNATURE", result.Result)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("rejected pseudo transaction mutated Amendments SLE: before=%x after=%x", before, after)
	}
}

func TestPseudoWire_OmittedSignersStillApplies(t *testing.T) {
	transaction, blob := pseudoWireTransaction(t, false)
	if transaction.GetCommon().HasField("Signers") {
		t.Fatal("binary round-trip marked omitted Signers as present")
	}

	result, before, after := applyPseudoWire(t, transaction, blob, nil)
	if !result.Applied || result.Result != ter.TesSUCCESS {
		t.Fatalf("omitted Signers result = %+v, want applied tesSUCCESS", result)
	}
	if bytes.Equal(before, after) {
		t.Fatalf("successful pseudo transaction did not update Amendments SLE: %x", after)
	}
}

func TestPseudoWire_OpenViewFlagsRejectPseudo(t *testing.T) {
	openFlags := []struct {
		name      string
		configure func(*txcore.EngineConfig)
	}{
		{name: "OpenLedger", configure: func(config *txcore.EngineConfig) { config.OpenLedger = true }},
		{name: "ViewOpen", configure: func(config *txcore.EngineConfig) { config.ViewOpen = true }},
		{name: "EnforceLoadFee", configure: func(config *txcore.EngineConfig) { config.EnforceLoadFee = true }},
		{name: "all", configure: func(config *txcore.EngineConfig) {
			config.OpenLedger = true
			config.ViewOpen = true
			config.EnforceLoadFee = true
		}},
	}

	for _, flag := range openFlags {
		t.Run(flag.name, func(t *testing.T) {
			transaction, blob := pseudoWireTransaction(t, false)
			result, before, after := applyPseudoWire(t, transaction, blob, flag.configure)
			if result.Applied {
				t.Fatalf("pseudo transaction applied with %s: %+v", flag.name, result)
			}
			if result.Result != ter.TemINVALID {
				t.Fatalf("pseudo transaction with %s result = %s, want temINVALID", flag.name, result.Result)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("rejected pseudo transaction with %s mutated Amendments SLE: before=%x after=%x", flag.name, before, after)
			}
		})
	}
}

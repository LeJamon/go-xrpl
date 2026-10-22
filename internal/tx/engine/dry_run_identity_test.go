package engine

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestApplyDryRunUsesCurrentIdentityForDuplicateAndAccountTxnID(t *testing.T) {
	base := newMockBaseView()
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	accountData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:         recoveryTestAccount,
		Balance:         1_000_000,
		Sequence:        1,
		HasAccountTxnID: true,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	base.data[keylet.Account(accountID).Key] = accountData

	original := accounttx.NewAccountSet(recoveryTestAccount)
	original.Fee = "10"
	original.SetSequence(1)
	raw, err := txcore.SerializeTransaction(original)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	parsed, err := txcore.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	originalID, err := txcore.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}

	// Keep the original wire identity while preparing a different simulation
	// request. The original ID is deliberately present in the view, so hashing
	// retained bytes would return tefALREADY before applying the current fields.
	parsed.GetCommon().Fee = "11"
	currentID, err := txcore.ComputeCurrentTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeCurrentTransactionHash: %v", err)
	}
	if currentID == originalID {
		t.Fatal("mutating Fee did not change the current transaction identity")
	}

	view := txExistsView{mockBaseView: base, existing: originalID}
	engine := NewEngine(view, txcore.EngineConfig{
		BaseFee:          10,
		ReserveBase:      20_000_000,
		ReserveIncrement: 2_000_000,
		LedgerSequence:   100,
		OpenLedger:       true,
		Rules:            amendment.AllSupportedRules(),
		ApplyFlags:       txcore.TapDRY_RUN,
	})

	result := engine.Apply(parsed)
	if result.Result != ter.TesSUCCESS {
		t.Fatalf("dry-run result = %s, want tesSUCCESS", result.Result)
	}
	if result.Applied {
		t.Fatal("dry-run result reported applied=true")
	}
	if result.Metadata == nil {
		t.Fatal("dry-run success returned nil metadata")
	}

	wantAccountTxnID := strings.ToUpper(hex.EncodeToString(currentID[:]))
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType != "AccountRoot" || node.NodeType != "ModifiedNode" {
			continue
		}
		got, ok := node.FinalFields["AccountTxnID"].(string)
		if !ok {
			continue
		}
		if got != wantAccountTxnID {
			t.Fatalf("simulated AccountTxnID = %q, want current transaction ID %q", got, wantAccountTxnID)
		}
		return
	}
	t.Fatal("dry-run metadata did not contain the AccountRoot AccountTxnID update")
}

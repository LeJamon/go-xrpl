package lending_test

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
)

const testAccount = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
const testHash = "1111111111111111111111111111111111111111111111111111111111111111"

var registerOnce sync.Once

func registerLending() { registerOnce.Do(lending.Register) }

// TestLendingAmendmentSupported pins LendingProtocol to rippled 3.1.0's
// registration: Supported::yes, VoteBehavior::DefaultNo. The Loan* transactors
// are fully implemented, so the node applies them once the amendment activates.
func TestLendingAmendmentSupported(t *testing.T) {
	f := amendment.FeatureByID(amendment.FeatureLendingProtocol)
	if f == nil {
		t.Fatal("LendingProtocol must be registered")
	}
	if f.Supported != amendment.SupportedYes {
		t.Errorf("LendingProtocol must be SupportedYes, got %v", f.Supported)
	}
	if f.Vote != amendment.VoteDefaultNo {
		t.Errorf("LendingProtocol must be VoteDefaultNo, got %v", f.Vote)
	}
}

// TestLoanTxTypeAndAmendment checks each stub reports the correct type code and
// requires the LendingProtocol amendment.
func TestLoanTxTypeAndAmendment(t *testing.T) {
	amt := tx.NewXRPAmount(1_000_000)
	cases := []struct {
		txn  tx.Transaction
		want tx.Type
	}{
		{lending.NewLoanBrokerSet(testAccount, testHash), tx.TypeLoanBrokerSet},
		{lending.NewLoanBrokerDelete(testAccount, testHash), tx.TypeLoanBrokerDelete},
		{lending.NewLoanBrokerCoverDeposit(testAccount, testHash, amt), tx.TypeLoanBrokerCoverDeposit},
		{lending.NewLoanBrokerCoverWithdraw(testAccount, testHash, amt), tx.TypeLoanBrokerCoverWithdraw},
		{lending.NewLoanBrokerCoverClawback(testAccount), tx.TypeLoanBrokerCoverClawback},
		{lending.NewLoanSet(testAccount, testHash, "1000"), tx.TypeLoanSet},
		{lending.NewLoanDelete(testAccount, testHash), tx.TypeLoanDelete},
		{lending.NewLoanManage(testAccount, testHash), tx.TypeLoanManage},
		{lending.NewLoanPay(testAccount, testHash, amt), tx.TypeLoanPay},
	}
	want := map[[32]byte]bool{
		amendment.FeatureLendingProtocol:  true,
		amendment.FeatureSingleAssetVault: true,
		amendment.FeatureMPTokensV1:       true,
	}
	for _, c := range cases {
		if got := c.txn.TxType(); got != c.want {
			t.Errorf("TxType() = %d, want %d", got, c.want)
		}
		req := c.txn.RequiredAmendments()
		if len(req) != len(want) {
			t.Errorf("%s RequiredAmendments() = %v, want the lending dependency chain", c.want, req)
			continue
		}
		for _, a := range req {
			if !want[a] {
				t.Errorf("%s RequiredAmendments() unexpected amendment %v", c.want, a)
			}
		}
	}
}

// TestLoanBinaryRoundTripAndTemplate confirms the surface a 3.0.0 node exposes:
// a serialized Loan transaction parses back into its concrete type (the codec
// knows every field and the type template accepts them), and a codec-known
// field that is disallowed for the type is rejected at parse — matching
// rippled's STTx template application.
func TestLoanBinaryRoundTripAndTemplate(t *testing.T) {
	registerLending()

	base := func(extra map[string]any) []byte {
		m := map[string]any{
			"Account":  testAccount,
			"Fee":      "10",
			"Sequence": uint32(1),
		}
		for k, v := range extra {
			m[k] = v
		}
		blob, err := binarycodec.EncodeBytes(m)
		if err != nil {
			t.Fatalf("EncodeBytes(%v): %v", extra, err)
		}
		return blob
	}

	// Positive: every allowed field parses back into the concrete type.
	positives := []struct {
		fields map[string]any
		want   tx.Type
	}{
		{map[string]any{"TransactionType": "LoanBrokerSet", "VaultID": testHash}, tx.TypeLoanBrokerSet},
		{map[string]any{"TransactionType": "LoanBrokerDelete", "LoanBrokerID": testHash}, tx.TypeLoanBrokerDelete},
		{map[string]any{"TransactionType": "LoanDelete", "LoanID": testHash}, tx.TypeLoanDelete},
		{map[string]any{"TransactionType": "LoanSet", "LoanBrokerID": testHash, "PrincipalRequested": "1000"}, tx.TypeLoanSet},
	}
	for _, p := range positives {
		parsed, err := tx.ParseFromBinary(base(p.fields))
		if err != nil {
			t.Errorf("ParseFromBinary(%v) failed: %v", p.fields, err)
			continue
		}
		if parsed.TxType() != p.want {
			t.Errorf("parsed type = %d, want %d", parsed.TxType(), p.want)
		}
	}

	// Negative: a codec-known field not in the type's template is rejected.
	// LoanBrokerDelete only allows LoanBrokerID; VaultID must be refused.
	blob := base(map[string]any{
		"TransactionType": "LoanBrokerDelete",
		"LoanBrokerID":    testHash,
		"VaultID":         testHash,
	})
	if _, err := tx.ParseFromBinary(blob); err == nil {
		t.Error("ParseFromBinary accepted a field disallowed by the LoanBrokerDelete template, want error")
	}
}

// TestLendingCodecDefinitions asserts the binary codec knows the lending
// transaction types, ledger-entry types, and a representative set of the new
// sfields at the exact codes rippled 3.0.0 assigns.
func TestLendingCodecDefinitions(t *testing.T) {
	d := definitions.Get()

	txTypes := map[string]int32{
		"LoanBrokerSet": 74, "LoanBrokerDelete": 75, "LoanBrokerCoverDeposit": 76,
		"LoanBrokerCoverWithdraw": 77, "LoanBrokerCoverClawback": 78,
		"LoanSet": 80, "LoanDelete": 81, "LoanManage": 82, "LoanPay": 84,
	}
	for name, code := range txTypes {
		got, err := d.TransactionTypeCode(name)
		if err != nil || got != code {
			t.Errorf("tx type %q: got (%d, %v), want %d", name, got, err, code)
		}
	}

	entryTypes := map[string]int32{"LoanBroker": 0x0088, "Loan": 0x0089}
	for name, code := range entryTypes {
		got, err := d.LedgerEntryTypeCode(name)
		if err != nil || got != code {
			t.Errorf("ledger entry %q: got (%d, %v), want %d", name, got, err, code)
		}
	}

	// Representative fields across the new type/ordinal ranges, including the
	// Int32 LoanScale and the notSigning CounterpartySignature object.
	fields := []struct {
		name    string
		typ     string
		signing bool
	}{
		{"ManagementFeeRate", "UInt16", true},
		{"LoanSequence", "UInt32", true},
		{"OverpaymentInterestRate", "UInt32", true},
		{"VaultNode", "UInt64", true},
		{"LoanBrokerNode", "UInt64", true},
		{"LoanBrokerID", "Hash256", true},
		{"LoanID", "Hash256", true},
		{"DebtTotal", "Number", true},
		{"PrincipalRequested", "Number", true},
		{"LoanScale", "Int32", true},
		{"Borrower", "AccountID", true},
		{"Counterparty", "AccountID", true},
		{"CounterpartySignature", "STObject", false},
	}
	for _, f := range fields {
		fi, err := d.FieldInstanceByName(f.name)
		if err != nil {
			t.Errorf("field %q: not found: %v", f.name, err)
			continue
		}
		if fi.Type != f.typ {
			t.Errorf("field %q: type %q, want %q", f.name, fi.Type, f.typ)
		}
		if fi.IsSigningField != f.signing {
			t.Errorf("field %q: IsSigningField %v, want %v", f.name, fi.IsSigningField, f.signing)
		}
	}
}

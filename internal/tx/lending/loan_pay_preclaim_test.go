package lending

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readOnlyView serves crafted SLE bytes by keylet; every mutating method is a
// no-op. Sufficient for LoanPay.Preclaim, which only reads the Loan entry before
// the overpayment-flag check.
type readOnlyView struct {
	data map[[32]byte][]byte
}

func (v readOnlyView) Read(k keylet.Keylet) ([]byte, error)      { return v.data[k.Key], nil }
func (v readOnlyView) Exists(k keylet.Keylet) (bool, error)      { _, ok := v.data[k.Key]; return ok, nil }
func (v readOnlyView) Insert(k keylet.Keylet, data []byte) error { return nil }
func (v readOnlyView) Update(k keylet.Keylet, data []byte) error { return nil }
func (v readOnlyView) Erase(k keylet.Keylet) error               { return nil }
func (v readOnlyView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (v readOnlyView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return nil
}
func (v readOnlyView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v readOnlyView) TxExists(txID [32]byte) bool { return false }
func (v readOnlyView) Rules() *amendment.Rules     { return nil }
func (v readOnlyView) LedgerSeq() uint32           { return 0 }

// TestLoanPay_OverpaymentOnNonOverpaymentLoan asserts the fixCleanup3_1_3 TER
// change: requesting an overpayment on a loan that does not allow it returns
// tecNO_PERMISSION post-amendment and temINVALID_FLAG pre-amendment.
func TestLoanPay_OverpaymentOnNonOverpaymentLoan(t *testing.T) {
	var accountID [20]byte
	for i := range accountID {
		accountID[i] = 0x44
	}
	accountAddr, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}

	var loanID, previousTxnID [32]byte
	for i := range loanID {
		loanID[i] = 0x55
		previousTxnID[i] = 0x56
	}
	loanIDHex := strings.ToUpper(hex.EncodeToString(loanID[:]))

	// Loan owned by the submitter, without the lsfLoanOverpayment flag.
	loanBytes, err := serializeLoan(&loanData{
		Borrower: accountID, PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
	})
	if err != nil {
		t.Fatalf("serializeLoan: %v", err)
	}
	view := readOnlyView{data: map[[32]byte][]byte{keylet.LoanByID(loanID).Key: loanBytes}}

	newPay := func() *LoanPay {
		lp := NewLoanPay(accountAddr, loanIDHex, tx.NewXRPAmount(1_000_000))
		lp.Common.SetFlags(TfLoanOverpayment)
		return lp
	}

	fixOn := tx.EngineConfig{Rules: amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_1_3})}
	fixOff := tx.EngineConfig{Rules: amendment.EmptyRules()}

	if got := newPay().Preclaim(view, fixOn); got != ter.TecNO_PERMISSION {
		t.Errorf("fixCleanup3_1_3 ON: got %v, want tecNO_PERMISSION", got)
	}
	if got := newPay().Preclaim(view, fixOff); got != ter.TemINVALID_FLAG {
		t.Errorf("fixCleanup3_1_3 OFF: got %v, want temINVALID_FLAG", got)
	}
}

// TestLoanPay_CalculateBaseFeeCap asserts the fixCleanup3_1_3 fee cap: a large
// Amount is charged at most loanMaximumPaymentsPerTransaction /
// loanPaymentsPerFeeIncrement (20) increments post-amendment, while pre-amendment
// the estimate scales unbounded with the Amount.
func TestLoanPay_CalculateBaseFeeCap(t *testing.T) {
	var brokerID, vaultID, loanID, previousTxnID [32]byte
	for i := range brokerID {
		brokerID[i] = 0x66
		vaultID[i] = 0x77
		loanID[i] = 0x88
		previousTxnID[i] = 0x99
	}
	var ownerID, pseudoID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x11
		pseudoID[i] = 0x22
	}
	ownerAddr, _ := state.EncodeAccountID(ownerID)
	pseudoAddr, _ := state.EncodeAccountID(pseudoID)

	// PeriodicPayment of 10 drops, LoanServiceFee 0 → regularPayment = 10.
	loanBytes, err := serializeLoan(&loanData{
		LoanBrokerID:     brokerID,
		PeriodicPayment:  "10",
		PaymentRemaining: 10, // > loanPaymentsPerFeeIncrement
		PreviousTxnID:    previousTxnID, PreviousTxnLgrSeq: 1,
	})
	if err != nil {
		t.Fatalf("serializeLoan: %v", err)
	}
	brokerBytes, err := serializeLoanBroker(&loanBrokerData{
		VaultID: vaultID, PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
	})
	if err != nil {
		t.Fatalf("serializeLoanBroker: %v", err)
	}
	vaultBytes, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             ownerAddr,
		"Account":           pseudoAddr,
		"Asset":             map[string]any{"currency": "XRP"},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  1,
		"PreviousTxnID":     strings.ToUpper(hex.EncodeToString(previousTxnID[:])),
		"PreviousTxnLgrSeq": 1,
	})
	if err != nil {
		t.Fatalf("encode vault: %v", err)
	}
	vaultRaw, _ := hex.DecodeString(vaultBytes)

	view := readOnlyView{data: map[[32]byte][]byte{
		keylet.LoanByID(loanID).Key:         loanBytes,
		keylet.LoanBrokerByID(brokerID).Key: brokerBytes,
		keylet.VaultByID(vaultID).Key:       vaultRaw,
	}}

	loanIDHex := strings.ToUpper(hex.EncodeToString(loanID[:]))
	// Amount = 2000 drops = regularPayment * 200: uncapped this estimates 200
	// payments → 40 fee increments; capped it is 20.
	pay := NewLoanPay(ownerAddr, loanIDHex, tx.NewXRPAmount(2000))

	cfg := func(fix313, fix320 bool) tx.EngineConfig {
		ids := [][32]byte{
			amendment.FeatureLendingProtocol,
			amendment.FeatureSingleAssetVault,
			amendment.FeatureMPTokensV1,
		}
		if fix313 {
			ids = append(ids, amendment.FeatureFixCleanup3_1_3)
		}
		if fix320 {
			ids = append(ids, amendment.FeatureFixCleanup3_2_0)
		}
		return tx.EngineConfig{BaseFee: 10, Rules: amendment.NewRules(ids)}
	}

	if got := pay.CalculateBaseFee(view, cfg(true, true)); got != 20*10 {
		t.Errorf("fixCleanup3_1_3 ON: got %d, want %d (capped at 20 increments)", got, 20*10)
	}
	if got := pay.CalculateBaseFee(view, cfg(false, true)); got != 40*10 {
		t.Errorf("fixCleanup3_1_3 OFF: got %d, want %d (40 increments, uncapped)", got, 40*10)
	}
	multisignedPay := NewLoanPay(ownerAddr, loanIDHex, tx.NewXRPAmount(2000))
	multisignedPay.Common.Signers = make([]tx.SignerWrapper, 2)
	if got := multisignedPay.CalculateBaseFee(view, cfg(true, true)); got != 20*30 {
		t.Errorf("multisigned capped payment: got %d, want %d", got, 20*30)
	}
	if got := multisignedPay.CalculateBaseFee(view, cfg(false, true)); got != 40*30 {
		t.Errorf("multisigned uncapped payment: got %d, want %d", got, 40*30)
	}
	multisignedPay.Common.SetFlags(TfLoanFullPayment)
	if got := multisignedPay.CalculateBaseFee(view, cfg(true, true)); got != 30 {
		t.Errorf("multisigned full payment: got %d, want 30", got)
	}

	overpayment := NewLoanPay(ownerAddr, loanIDHex, tx.NewXRPAmount(51))
	overpayment.Common.SetFlags(TfLoanOverpayment)
	for _, fix313 := range []bool{false, true} {
		for _, fix320 := range []bool{false, true} {
			if got := overpayment.CalculateBaseFee(view, cfg(fix313, fix320)); got != 2*10 {
				t.Errorf("fixCleanup3_1_3=%t fixCleanup3_2_0=%t: got %d, want %d (2 upward-rounded increments)", fix313, fix320, got, 2*10)
			}
		}
	}

	multisignedOverpayment := NewLoanPay(ownerAddr, loanIDHex, tx.NewXRPAmount(51))
	multisignedOverpayment.Common.SetFlags(TfLoanOverpayment)
	multisignedOverpayment.Common.Signers = make([]tx.SignerWrapper, 2)
	if got := multisignedOverpayment.CalculateBaseFee(view, cfg(true, true)); got != 6*10 {
		t.Errorf("multisigned overpayment: got %d, want %d", got, 6*10)
	}
}

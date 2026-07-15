package lending

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

func TestLendingNumberRulesPreserveCuspArithmeticAndWireBytes(t *testing.T) {
	legacyRules := amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol})
	fixedRules := amendment.NewRules([][32]byte{
		amendment.FeatureLendingProtocol,
		amendment.FeatureFixCleanup3_2_0,
	})
	if got := lendingNumberScale(legacyRules); got != state.MantissaScaleLargeLegacy {
		t.Fatalf("legacy scale = %v, want LargeLegacy", got)
	}
	if got := lendingNumberScale(fixedRules); got != state.MantissaScaleLarge {
		t.Fatalf("fixed scale = %v, want Large", got)
	}

	const (
		left  = "1000000000000049863"
		right = "9223372036854315903"
	)
	legacy := lendNumForRules(left, legacyRules).
		MulRounded(lendNumForRules(right, legacyRules), state.RoundUpward)
	fixed := lendNumForRules(left, fixedRules).
		MulRounded(lendNumForRules(right, fixedRules), state.RoundUpward)
	if legacy.Mantissa() != (int64(math.MaxInt64)/100)*100 || legacy.Exponent() != 18 {
		t.Fatalf("legacy result = %de%d", legacy.Mantissa(), legacy.Exponent())
	}
	if fixed.Mantissa() != int64(math.MaxInt64)/10+1 || fixed.Exponent() != 19 {
		t.Fatalf("fixed result = %de%d", fixed.Mantissa(), fixed.Exponent())
	}

	var account, owner [20]byte
	account[0] = 1
	owner[0] = 2
	legacyData, err := serializeLoanBrokerForRules(&loanBrokerData{
		Account: account, Owner: owner, DebtTotal: numStr(legacy),
	}, legacyRules)
	if err != nil {
		t.Fatalf("serialize legacy LoanBroker: %v", err)
	}
	fixedData, err := serializeLoanBrokerForRules(&loanBrokerData{
		Account: account, Owner: owner, DebtTotal: numStr(fixed),
	}, fixedRules)
	if err != nil {
		t.Fatalf("serialize fixed LoanBroker: %v", err)
	}
	assertNumberWireBytes(t, legacyData, legacy.Mantissa(), legacy.Exponent())
	assertNumberWireBytes(t, fixedData, fixed.Mantissa(), fixed.Exponent())
	if bytes.Equal(legacyData, fixedData) {
		t.Fatal("legacy and fixed cusp encodings unexpectedly match")
	}
}

func assertNumberWireBytes(t *testing.T, data []byte, mantissa int64, exponent int) {
	t.Helper()
	want := make([]byte, 12)
	binary.BigEndian.PutUint64(want[:8], uint64(mantissa))
	binary.BigEndian.PutUint32(want[8:], uint32(int32(exponent)))
	if !bytes.Contains(data, want) {
		t.Fatalf("serialized Number %de%d is absent from %s", mantissa, exponent, hex.EncodeToString(data))
	}
}

func TestSerializeLoanBrokerRoundTrip(t *testing.T) {
	var vid, previousTxnID [32]byte
	var acct, owner [20]byte
	for i := range vid {
		vid[i] = byte(i + 1)
		previousTxnID[i] = byte(i + 2)
	}
	for i := range acct {
		acct[i] = byte(i + 1)
		owner[i] = byte(i + 100)
	}
	b := &loanBrokerData{
		Sequence: 5, OwnerNode: 0, VaultNode: 0, VaultID: vid,
		Account: acct, Owner: owner, LoanSequence: 1,
		ManagementFeeRate: 1000, CoverAvailable: "500", DebtTotal: "1000",
		CoverRateMinimum: 1000, CoverRateLiquidation: 1100,
		PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
	}
	data, err := serializeLoanBroker(b)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	got, err := parseLoanBroker(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Sequence != 5 || got.LoanSequence != 1 || got.ManagementFeeRate != 1000 {
		t.Fatalf("mismatch: %+v", got)
	}
	if got.CoverAvailable != "500" || got.DebtTotal != "1000" {
		t.Fatalf("number mismatch: cover=%q debt=%q", got.CoverAvailable, got.DebtTotal)
	}
}

func TestSerializeLoanBrokerCanonicalFieldStyles(t *testing.T) {
	var account, owner [20]byte
	for i := range account {
		account[i] = byte(i + 1)
		owner[i] = byte(i + 51)
	}
	accountAddr, err := state.EncodeAccountID(account)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	ownerAddr, err := state.EncodeAccountID(owner)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}

	t.Run("defaults omitted and required zeros retained", func(t *testing.T) {
		got, err := serializeLoanBroker(&loanBrokerData{Account: account, Owner: owner})
		if err != nil {
			t.Fatalf("serialize loan broker: %v", err)
		}
		assertLendingEncoding(t, got, map[string]any{
			"LedgerEntryType": "LoanBroker",
			"Flags":           uint32(0),
			"Sequence":        uint32(0),
			"OwnerNode":       "0",
			"VaultNode":       "0",
			"VaultID":         strings.Repeat("0", 64),
			"Account":         accountAddr,
			"Owner":           ownerAddr,
			"LoanSequence":    uint32(0),
		})
	})

	t.Run("nondefaults and deferred threading retained", func(t *testing.T) {
		var vaultID, previousTxnID [32]byte
		for i := range vaultID {
			vaultID[i] = 0xA1
			previousTxnID[i] = 0xB2
		}
		got, err := serializeLoanBroker(&loanBrokerData{
			Sequence:             5,
			OwnerNode:            0xA,
			VaultNode:            0xB,
			VaultID:              vaultID,
			Account:              account,
			Owner:                owner,
			LoanSequence:         6,
			Data:                 "abcd",
			ManagementFeeRate:    123,
			OwnerCount:           2,
			DebtTotal:            "1000",
			DebtMaximum:          "2000",
			CoverAvailable:       "300",
			CoverRateMinimum:     100,
			CoverRateLiquidation: 110,
			Flags:                4,
			PreviousTxnID:        previousTxnID,
			PreviousTxnLgrSeq:    12,
		})
		if err != nil {
			t.Fatalf("serialize loan broker: %v", err)
		}
		assertLendingEncoding(t, got, map[string]any{
			"LedgerEntryType":      "LoanBroker",
			"Flags":                uint32(4),
			"Sequence":             uint32(5),
			"OwnerNode":            "A",
			"VaultNode":            "B",
			"VaultID":              strings.Repeat("A1", 32),
			"Account":              accountAddr,
			"Owner":                ownerAddr,
			"LoanSequence":         uint32(6),
			"Data":                 "ABCD",
			"ManagementFeeRate":    123,
			"OwnerCount":           uint32(2),
			"DebtTotal":            "1000",
			"DebtMaximum":          "2000",
			"CoverAvailable":       "300",
			"CoverRateMinimum":     uint32(100),
			"CoverRateLiquidation": uint32(110),
			"PreviousTxnID":        strings.Repeat("B2", 32),
			"PreviousTxnLgrSeq":    uint32(12),
		})
	})
}

func TestSerializeLoanCanonicalFieldStyles(t *testing.T) {
	var borrower [20]byte
	for i := range borrower {
		borrower[i] = byte(i + 1)
	}
	borrowerAddr, err := state.EncodeAccountID(borrower)
	if err != nil {
		t.Fatalf("encode borrower: %v", err)
	}

	t.Run("defaults omitted and required zeros retained", func(t *testing.T) {
		got, err := serializeLoan(&loanData{Borrower: borrower})
		if err != nil {
			t.Fatalf("serialize loan: %v", err)
		}
		assertLendingEncoding(t, got, map[string]any{
			"LedgerEntryType": "Loan",
			"Flags":           uint32(0),
			"OwnerNode":       "0",
			"LoanBrokerNode":  "0",
			"LoanBrokerID":    strings.Repeat("0", 64),
			"LoanSequence":    uint32(0),
			"Borrower":        borrowerAddr,
			"StartDate":       uint32(0),
			"PaymentInterval": uint32(0),
			"PeriodicPayment": "0",
		})
	})

	t.Run("nondefaults and deferred threading retained", func(t *testing.T) {
		var brokerID, previousTxnID [32]byte
		for i := range brokerID {
			brokerID[i] = 0xC3
			previousTxnID[i] = 0xD4
		}
		got, err := serializeLoan(&loanData{
			OwnerNode:                0xC,
			LoanBrokerNode:           0xD,
			LoanBrokerID:             brokerID,
			LoanSequence:             8,
			Borrower:                 borrower,
			LoanOriginationFee:       "1",
			LoanServiceFee:           "2",
			LatePaymentFee:           "3",
			ClosePaymentFee:          "4",
			OverpaymentFee:           5,
			InterestRate:             6,
			LateInterestRate:         7,
			CloseInterestRate:        8,
			OverpaymentInterestRate:  9,
			StartDate:                10,
			PaymentInterval:          11,
			GracePeriod:              12,
			PreviousPaymentDueDate:   13,
			NextPaymentDueDate:       14,
			PaymentRemaining:         15,
			PeriodicPayment:          "16",
			PrincipalOutstanding:     "17",
			TotalValueOutstanding:    "18",
			ManagementFeeOutstanding: "19",
			LoanScale:                2,
			Flags:                    20,
			PreviousTxnID:            previousTxnID,
			PreviousTxnLgrSeq:        21,
		})
		if err != nil {
			t.Fatalf("serialize loan: %v", err)
		}
		assertLendingEncoding(t, got, map[string]any{
			"LedgerEntryType":          "Loan",
			"Flags":                    uint32(20),
			"OwnerNode":                "C",
			"LoanBrokerNode":           "D",
			"LoanBrokerID":             strings.Repeat("C3", 32),
			"LoanSequence":             uint32(8),
			"Borrower":                 borrowerAddr,
			"LoanOriginationFee":       "1",
			"LoanServiceFee":           "2",
			"LatePaymentFee":           "3",
			"ClosePaymentFee":          "4",
			"OverpaymentFee":           uint32(5),
			"InterestRate":             uint32(6),
			"LateInterestRate":         uint32(7),
			"CloseInterestRate":        uint32(8),
			"OverpaymentInterestRate":  uint32(9),
			"StartDate":                uint32(10),
			"PaymentInterval":          uint32(11),
			"GracePeriod":              uint32(12),
			"PreviousPaymentDueDate":   uint32(13),
			"NextPaymentDueDate":       uint32(14),
			"PaymentRemaining":         uint32(15),
			"PeriodicPayment":          "16",
			"PrincipalOutstanding":     "17",
			"TotalValueOutstanding":    "18",
			"ManagementFeeOutstanding": "19",
			"LoanScale":                2,
			"PreviousTxnID":            strings.Repeat("D4", 32),
			"PreviousTxnLgrSeq":        uint32(21),
		})
	})
}

func assertLendingEncoding(t *testing.T, got []byte, fields map[string]any) {
	t.Helper()
	want, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("encode expected ledger entry: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding mismatch\n got: %s\nwant: %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
}

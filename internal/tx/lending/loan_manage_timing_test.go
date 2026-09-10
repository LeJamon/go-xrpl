package lending

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanPaymentLateBoundaries(t *testing.T) {
	fix := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	legacy := amendment.EmptyRules()
	for _, tc := range []struct {
		name  string
		now   uint32
		rules *amendment.Rules
		want  bool
	}{
		{"fix before due", 99, fix, false},
		{"fix at due", 100, fix, false},
		{"fix after due", 101, fix, true},
		{"legacy before due", 99, legacy, false},
		{"legacy at due", 100, legacy, true},
		{"legacy after due", 101, legacy, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lmath.IsPaymentLate(tc.now, 100, tc.rules.Enabled(amendment.FeatureFixCleanup3_4_0)); got != tc.want {
				t.Fatalf("IsPaymentLate(%d): got %t, want %t", tc.now, got, tc.want)
			}
		})
	}
}

func TestLoanManageDefaultGraceBoundary(t *testing.T) {
	var loanID, brokerID, previousTxnID [32]byte
	for i := range loanID {
		loanID[i] = 0x11
		brokerID[i] = 0x22
		previousTxnID[i] = 0x33
	}
	var owner [20]byte
	for i := range owner {
		owner[i] = 0x44
	}
	ownerAddress, err := state.EncodeAccountID(owner)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	loanDataBytes, err := serializeLoan(&loanData{
		LoanBrokerID: brokerID, PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
		PaymentRemaining: 1, NextPaymentDueDate: 100, GracePeriod: 10,
	})
	if err != nil {
		t.Fatalf("serializeLoan: %v", err)
	}
	brokerDataBytes, err := serializeLoanBroker(&loanBrokerData{
		Owner: owner, PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
	})
	if err != nil {
		t.Fatalf("serializeLoanBroker: %v", err)
	}
	view := readOnlyView{data: map[[32]byte][]byte{
		keylet.LoanByID(loanID).Key:         loanDataBytes,
		keylet.LoanBrokerByID(brokerID).Key: brokerDataBytes,
	}}
	newManage := func() *LoanManage {
		manage := NewLoanManage(ownerAddress, strings.ToUpper(hex.EncodeToString(loanID[:])))
		manage.Common.SetFlags(TfLoanDefault)
		return manage
	}
	for _, tc := range []struct {
		name  string
		close uint32
		rules *amendment.Rules
		want  ter.Result
	}{
		{"fix one close before grace", 109, amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0}), ter.TecTOO_SOON},
		{"fix at grace boundary", 110, amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0}), ter.TecTOO_SOON},
		{"fix one close after grace", 111, amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0}), ter.TesSUCCESS},
		{"legacy at grace boundary", 110, amendment.EmptyRules(), ter.TesSUCCESS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := tx.EngineConfig{ParentCloseTime: tc.close, Rules: tc.rules}
			if got := newManage().Preclaim(view, config); got != tc.want {
				t.Fatalf("Preclaim at close %d: got %v, want %v", tc.close, got, tc.want)
			}
		})
	}
}

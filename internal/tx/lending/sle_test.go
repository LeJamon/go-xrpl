package lending

import "testing"

func TestSerializeLoanBrokerRoundTrip(t *testing.T) {
	var vid, ptid [32]byte
	var acct, owner [20]byte
	for i := range vid {
		vid[i] = byte(i + 1)
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
	_ = ptid
}

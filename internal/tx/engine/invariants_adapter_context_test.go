package engine

import "testing"

func TestInvariantsContextForApplyCarriesResolvedFeePayer(t *testing.T) {
	var account, delegate, sponsor [20]byte
	account[0] = 1
	delegate[0] = 2
	sponsor[0] = 3

	tests := []struct {
		name        string
		payer       feePayer
		wantID      [20]byte
		wantKnown   bool
		wantPrefund bool
	}{
		{
			name:      "ordinary account",
			payer:     feePayer{payerTy: feePayerAccount, accountID: account, known: true},
			wantID:    account,
			wantKnown: true,
		},
		{
			name:      "account fallback",
			payer:     feePayer{payerTy: feePayerAccount},
			wantID:    account,
			wantKnown: true,
		},
		{
			name:      "delegate",
			payer:     feePayer{payerTy: feePayerDelegate, accountID: delegate, known: true},
			wantID:    delegate,
			wantKnown: true,
		},
		{
			name:        "co-signed sponsor",
			payer:       feePayer{payerTy: feePayerSponsorCoSigned, accountID: sponsor, known: true},
			wantID:      sponsor,
			wantKnown:   true,
			wantPrefund: false,
		},
		{
			name:        "pre-funded sponsor",
			payer:       feePayer{payerTy: feePayerSponsorPreFunded, accountID: sponsor, known: true},
			wantID:      [20]byte{},
			wantKnown:   true,
			wantPrefund: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			context := invariantsContextForApply(&applyState{accountID: account, feePayer: tc.payer}, 1234)
			adapted := wrapTxForInvariantsWithContext(nil, context)
			provider, ok := adapted.(interface {
				FeePayer() ([20]byte, bool, bool)
				CurrentCloseTime() (uint32, bool)
			})
			if !ok {
				t.Fatal("invariants adapter does not expose apply context")
			}
			gotID, gotPrefund, gotKnown := provider.FeePayer()
			if gotID != tc.wantID || gotKnown != tc.wantKnown || gotPrefund != tc.wantPrefund {
				t.Fatalf("FeePayer() = (%x, %v, %v), want (%x, %v, %v)", gotID, gotPrefund, gotKnown, tc.wantID, tc.wantPrefund, tc.wantKnown)
			}
			if gotTime, gotKnown := provider.CurrentCloseTime(); gotTime != 1234 || !gotKnown {
				t.Fatalf("CurrentCloseTime() = (%d, %v), want (1234, true)", gotTime, gotKnown)
			}
		})
	}
}

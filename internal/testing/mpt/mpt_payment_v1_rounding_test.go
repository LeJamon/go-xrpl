package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	paybuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
)

func TestMPTPaymentV1TransferFeePrecisionBoundary(t *testing.T) {
	const amount int64 = 10_000_000_000_000_001
	const requiredSource int64 = 11_000_000_000_000_000

	for _, test := range []struct {
		name    string
		sendMax int64
		wantTER string
	}{
		{name: "rounded source amount succeeds", sendMax: requiredSource, wantTER: jtx.TesSUCCESS},
		{name: "one unit below rounded source amount fails", sendMax: requiredSource - 1, wantTER: jtx.TecPATH_PARTIAL},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.DisableFeature("MPTokensV2")
			env.DisableFeature("SingleAssetVault")
			env.DisableFeature("LendingProtocol")
			env.Close()

			issuer := jtx.NewAccount("issuer")
			sender := jtx.NewAccount("sender")
			receiver := jtx.NewAccount("receiver")
			token := mpt.NewMPTTester(t, env, issuer, mpt.MPTInit{
				Holders: []*jtx.Account{sender, receiver},
			})
			transferFee := uint16(10_000)
			token.Create(mpt.CreateOpts{
				TransferFee: &transferFee,
				OwnerCount:  mpt.PtrUint32(1),
				HolderCount: mpt.PtrUint32(0),
				Flags:       mpt.TfMPTCanTransfer,
			})
			token.Authorize(mpt.AuthorizeOpts{Account: sender})
			token.Authorize(mpt.AuthorizeOpts{Account: receiver})
			token.Pay(issuer, sender, requiredSource)

			result := env.Submit(
				paybuilder.PayIssued(sender, receiver, token.MPTAmount(amount)).
					SendMax(token.MPTAmount(test.sendMax)).
					Build(),
			)
			if test.wantTER == jtx.TesSUCCESS {
				jtx.RequireTxSuccess(t, result)
				token.RequireMPTokenAmount(sender, 0)
				token.RequireMPTokenAmount(receiver, amount)
				if got := token.IssuanceOutstandingAmount(); got != uint64(amount) {
					t.Fatalf("OutstandingAmount = %d, want %d", got, amount)
				}
				return
			}

			jtx.RequireTxClaimed(t, result, test.wantTER)
			token.RequireMPTokenAmount(sender, requiredSource)
			token.RequireMPTokenAmount(receiver, 0)
			if got := token.IssuanceOutstandingAmount(); got != uint64(requiredSource) {
				t.Fatalf("OutstandingAmount = %d, want %d", got, requiredSource)
			}
		})
	}
}

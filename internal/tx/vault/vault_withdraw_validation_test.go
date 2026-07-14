package vault

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestVaultWithdraw_DestinationTagWithoutDestinationIsValid(t *testing.T) {
	withdraw := NewVaultWithdraw("rOwner", makeValidVaultID(), tx.NewXRPAmount(1))
	withdraw.Common.Fee = "12"
	sequence := uint32(1)
	withdraw.Common.Sequence = &sequence
	tag := uint32(7)
	withdraw.DestinationTag = &tag

	if err := withdraw.Validate(); err != nil {
		t.Fatalf("Validate() returned %v", err)
	}
}

func TestVaultWithdrawNumberOverflowReturnsPathDry(t *testing.T) {
	view := newMPTArmsView()
	var account [20]byte
	account[19] = 1
	ctx := buildArmsCtx(t, view, account, rulesWithFix(true))
	shareID := [24]byte{1}
	var shareIssuer [20]byte
	copy(shareIssuer[:], shareID[4:])
	amount := state.NewMPTAmountWithIssuanceID(
		1,
		state.EncodeAccountIDSafe(shareIssuer),
		hex.EncodeToString(shareID[:]),
	)
	withdraw := NewVaultWithdraw(ctx.Account.Account, makeValidVaultID(), amount)
	vd := &vaultData{
		Account:          [20]byte{2},
		ShareMPTID:       shareID,
		Asset:            tx.Asset{Currency: "XRP"},
		AssetsTotal:      "10",
		AssetsAvailable:  "10",
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	}
	_, _, _, _, _, result := withdraw.withdrawalAmounts(ctx, vd, &state.MPTokenIssuanceData{})
	if result != ter.TecPATH_DRY {
		t.Fatalf("withdrawalAmounts() = %v, want tecPATH_DRY", result)
	}
}

package vault

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
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

func TestVaultWithdrawShareConversionWithoutSupply(t *testing.T) {
	for _, tc := range []struct {
		name   string
		assets string
		want   ter.Result
	}{
		{name: "empty", assets: "0", want: ter.TesSUCCESS},
		{name: "nonzero assets", assets: "10", want: ter.TecPATH_DRY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := newMPTArmsView()
			account := [20]byte{1}
			issuer := [20]byte{2}
			shareID := keylet.MakeMPTID(1, issuer)
			issuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
				Issuer:   issuer,
				Sequence: 1,
			})
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.MPTIssuance(shareID), issuance))
			vd := &vaultData{
				ShareMPTID:  shareID,
				Asset:       tx.Asset{Currency: "USD", Issuer: state.EncodeAccountIDSafe(issuer)},
				AssetsTotal: tc.assets,
			}
			withdraw := NewVaultWithdraw(
				state.EncodeAccountIDSafe(account),
				makeValidVaultID(),
				state.NewMPTAmountWithIssuanceID(1, "", hex.EncodeToString(shareID[:])),
			)
			amount, result := withdraw.sharesToAssetAmount(view, vd, account, rulesWithFix(true))
			require.Equal(t, tc.want, result)
			if result == ter.TesSUCCESS {
				require.Zero(t, amount.Signum())
				require.Equal(t, vd.Asset.Currency, amount.Currency)
				require.Equal(t, vd.Asset.Issuer, amount.Issuer)
			}
		})
	}
}

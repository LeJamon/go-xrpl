package paychan

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

const validChannelHex = "0000000000000000000000000000000000000000000000000000000000000001"

// preflightCode extracts the TER code carried by a preflight/seam error.
func preflightCode(t *testing.T, err error) ter.Result {
	t.Helper()
	require.Error(t, err)
	var re *ter.ResultError
	require.ErrorAs(t, err, &re)
	return re.Code
}

// rulesDisabling returns the all-supported rule set with one amendment removed.
func rulesDisabling(name string) *amendment.Rules {
	return amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).DisableByName(name).Build()
}

// TestPayChan_GetFlagsMaskGating pins the mask-position finding for all three
// payment-channel transactions: the mask is enforced at preflight0
// unconditionally (fix1543 retired).
func TestPayChan_GetFlagsMaskGating(t *testing.T) {
	on := amendment.AllSupportedRules()

	require.Equal(t, tx.TfUniversalMask, (&PaymentChannelCreate{}).GetFlagsMask(on))
	require.Equal(t, tx.TfUniversalMask, (&PaymentChannelFund{}).GetFlagsMask(on))
	require.Equal(t, tfPayChanClaimMask, (&PaymentChannelClaim{}).GetFlagsMask(on))
}

// TestPayChanClaim_CredentialsExtraFeature pins the CredentialIDs amendment gate:
// with Credentials disabled, a CredentialIDs-bearing claim is temDISABLED via
// CheckExtraFeatures (keyed on field presence, not element count).
func TestPayChanClaim_CredentialsExtraFeature(t *testing.T) {
	claim := &PaymentChannelClaim{
		BaseTx:        *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
		Channel:       validChannelHex,
		CredentialIDs: []string{"AB", "AB"},
	}
	require.Equal(t, ter.TemDISABLED, preflightCode(t, claim.CheckExtraFeatures(rulesDisabling("Credentials"))))
	require.NoError(t, claim.CheckExtraFeatures(amendment.AllSupportedRules()))
}

// TestPayChanClaim_BadAmountBeatsFlagConflict pins the Balance/Amount vs
// tfClose+tfRenew order: rippled checks amount validity first, so a non-XRP
// Balance surfaces temBAD_AMOUNT even when the flag conflict is also present.
func TestPayChanClaim_BadAmountBeatsFlagConflict(t *testing.T) {
	nonXRP := tx.NewIssuedAmountFromFloat64(100.0, "USD", "rGw")
	claim := &PaymentChannelClaim{
		BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
		Channel: validChannelHex,
		Balance: &nonXRP,
	}
	claim.SetClose()
	claim.SetRenew()
	require.Equal(t, ter.TemBAD_AMOUNT, preflightCode(t, claim.Validate()))
}

// TestPayChanClaim_BadSignatureBeatsCheckFields pins the Signature-block vs
// CredentialIDs-shape order: the CredentialIDs shape check runs LAST, so a bad
// claim signature surfaces temBAD_SIGNATURE even with duplicate CredentialIDs.
func TestPayChanClaim_BadSignatureBeatsCheckFields(t *testing.T) {
	bal := tx.NewXRPAmount(500)
	claim := &PaymentChannelClaim{
		BaseTx:        *tx.NewBaseTx(tx.TypePaymentChannelClaim, "rOwner"),
		Channel:       validChannelHex,
		Balance:       &bal,
		PublicKey:     "ED0000000000000000000000000000000000000000000000000000000000000000",
		Signature:     "DEADBEEF",
		CredentialIDs: []string{"AB", "AB"},
	}
	require.Equal(t, ter.TemBAD_SIGNATURE, preflightCode(t, claim.Validate()))
}

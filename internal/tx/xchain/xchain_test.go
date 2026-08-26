package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXChainAmendmentSupported(t *testing.T) {
	f := amendment.FeatureByID(amendment.FeatureXChainBridge)
	require.NotNil(t, f, "XChainBridge must be registered")
	assert.Equal(t, amendment.SupportedYes, f.Supported)
}

func TestValidateBridgeFieldsCanonicalIssues(t *testing.T) {
	const (
		lockingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
		issuingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	)
	accepted := []tx.Asset{
		{},
		{Currency: "XRP"},
		{Currency: "0000000000000000000000000000000000000000"},
	}
	for _, asset := range accepted {
		bridge := XChainBridge{
			LockingChainDoor: lockingDoor, LockingChainIssue: asset,
			IssuingChainDoor: issuingDoor, IssuingChainIssue: tx.Asset{Currency: "XRP"},
		}
		require.NoError(t, validateBridgeFields(bridge))
		require.True(t, bridgeAssetIsNative(asset))
	}

	rejected := []struct {
		name  string
		asset tx.Asset
	}{
		{name: "empty currency with issuer", asset: tx.Asset{Issuer: lockingDoor}},
		{name: "XRP with issuer", asset: tx.Asset{Currency: "XRP", Issuer: lockingDoor}},
		{name: "hex XRP with issuer", asset: tx.Asset{Currency: "0000000000000000000000000000000000000000", Issuer: lockingDoor}},
		{name: "IOU without issuer", asset: tx.Asset{Currency: "USD"}},
		{name: "no currency", asset: tx.Asset{Currency: "1", Issuer: lockingDoor}},
		{name: "no currency hex", asset: tx.Asset{Currency: "0000000000000000000000000000000000000001", Issuer: lockingDoor}},
		{name: "bad XRP currency", asset: tx.Asset{Currency: "0000000000000000000000005852500000000000", Issuer: lockingDoor}},
		{name: "IOU with XRP sentinel issuer", asset: tx.Asset{Currency: "USD", Issuer: "rrrrrrrrrrrrrrrrrrrrrhoLvTp"}},
		{name: "IOU with no-account sentinel issuer", asset: tx.Asset{Currency: "USD", Issuer: "rrrrrrrrrrrrrrrrrrrrBZbvji"}},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			bridge := XChainBridge{
				LockingChainDoor:  lockingDoor,
				LockingChainIssue: test.asset,
				IssuingChainDoor:  issuingDoor,
				IssuingChainIssue: tx.Asset{Currency: "XRP"},
			}
			require.Error(t, validateBridgeFields(bridge))
		})
	}
}

func TestAssetEqualUsesCurrencyBytes(t *testing.T) {
	issuer := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	assert.True(t, assetEqual(
		tx.Asset{Currency: "USD", Issuer: issuer},
		tx.Asset{Currency: "0000000000000000000000005553440000000000", Issuer: issuer},
	))
}

func TestRewardShareRounding(t *testing.T) {
	numberContext := state.NewNumberContext(state.MantissaScaleSmall, false)
	tests := []struct {
		name      string
		pool      int64
		count     uint64
		roundDown bool
		want      int64
	}{
		{name: "legacy nearest rounds up", pool: 2, count: 3, want: 1},
		{name: "fixed rounds positive down", pool: 2, count: 3, roundDown: true, want: 0},
		{name: "legacy tie to even", pool: 1, count: 2, want: 0},
		{name: "fixed rounds negative down", pool: -1, count: 2, roundDown: true, want: -1},
		{name: "legacy negative tie to even", pool: -1, count: 2, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rewardShare(tx.NewXRPAmount(test.pool), test.count, numberContext, test.roundDown)
			assert.Equal(t, test.want, got.Drops())
		})
	}
}

func TestNativeAmountLegalNetBoundaries(t *testing.T) {
	const (
		lockingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
		issuingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	)
	bridge := XChainBridge{
		LockingChainDoor: lockingDoor, LockingChainIssue: tx.Asset{Currency: "XRP"},
		IssuingChainDoor: issuingDoor, IssuingChainIssue: tx.Asset{Currency: "XRP"},
	}
	tooLarge := tx.NewXRPAmount(maxNativeDrops + 1)
	assert.Error(t, validateCreateBridge(lockingDoor, bridge, tooLarge, nil))
	assert.Error(t, validateModifyBridge(lockingDoor, bridge, &tooLarge, nil, 0))

	claim := &XChainClaim{
		BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, lockingDoor), XChainBridge: bridge,
		Amount: tooLarge, Destination: lockingDoor, XChainClaimID: 1,
	}
	assert.Error(t, claim.Validate())
	accountCreate := &XChainAccountCreateCommit{
		BaseTx: *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, lockingDoor), XChainBridge: bridge,
		Amount: tooLarge, SignatureReward: tx.NewXRPAmount(0), Destination: lockingDoor,
	}
	assert.Error(t, accountCreate.Validate())
}

func TestMaxStoredAttestations(t *testing.T) {
	assert.True(t, attestationsWithinLimit(make([]any, maxStoredAttestations)))
	assert.False(t, attestationsWithinLimit(make([]any, maxStoredAttestations+1)))
}

func TestAllXChainTransactionsRejectMalformedBridge(t *testing.T) {
	const (
		lockingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
		issuingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	)
	badBridge := XChainBridge{
		LockingChainDoor: lockingDoor, LockingChainIssue: tx.Asset{Currency: "1", Issuer: lockingDoor},
		IssuingChainDoor: issuingDoor, IssuingChainIssue: tx.Asset{Currency: "XRP"},
	}
	tests := []struct {
		name string
		tx   interface{ Validate() error }
	}{
		{name: "create bridge", tx: &XChainCreateBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateBridge, lockingDoor), XChainBridge: badBridge}},
		{name: "modify bridge", tx: &XChainModifyBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainModifyBridge, lockingDoor), XChainBridge: badBridge}},
		{name: "create claim id", tx: &XChainCreateClaimID{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateClaimID, lockingDoor), XChainBridge: badBridge}},
		{name: "commit", tx: &XChainCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainCommit, lockingDoor), XChainBridge: badBridge}},
		{name: "claim", tx: &XChainClaim{BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, lockingDoor), XChainBridge: badBridge}},
		{name: "account create commit", tx: &XChainAccountCreateCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, lockingDoor), XChainBridge: badBridge}},
		{name: "claim attestation", tx: &XChainAddClaimAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, lockingDoor), XChainBridge: badBridge}},
		{name: "account create attestation", tx: &XChainAddAccountCreateAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, lockingDoor), XChainBridge: badBridge}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, test.tx.Validate(), "temXCHAIN_BRIDGE_BAD_ISSUES")
		})
	}
}

func TestAdjustXRPBalancesNegativeReward(t *testing.T) {
	source := &state.AccountRoot{Balance: 100}
	destination := &state.AccountRoot{Balance: 0}
	require.Equal(t, ter.TecINVARIANT_FAILED, adjustXRPBalances(source, destination, -1))
	require.Equal(t, uint64(100), source.Balance)
	require.Zero(t, destination.Balance)

	destination.Balance = 10
	require.Equal(t, ter.TesSUCCESS, adjustXRPBalances(source, destination, -3))
	require.Equal(t, uint64(103), source.Balance)
	require.Equal(t, uint64(7), destination.Balance)
}

func TestClaimAttestationFinalizeFatalResult(t *testing.T) {
	assert.Equal(t, ter.TesSUCCESS, finalizeResult{
		main: ter.TecUNFUNDED_PAYMENT, reward: ter.TesSUCCESS, remove: ter.TesSUCCESS,
	}.claimAttestationFatalResult())
	assert.Equal(t, ter.TesSUCCESS, finalizeResult{
		main: ter.TesSUCCESS, reward: ter.TecUNFUNDED_PAYMENT, remove: ter.TesSUCCESS,
	}.claimAttestationFatalResult())
	assert.Equal(t, ter.TecINVARIANT_FAILED, finalizeResult{
		main: ter.TecXCHAIN_PAYMENT_FAILED, reward: ter.TecINVARIANT_FAILED, remove: ter.TesSUCCESS,
	}.claimAttestationFatalResult())
}

func TestAccountCreateAttestationFinalizeFatalResult(t *testing.T) {
	assert.Equal(t, ter.TecUNFUNDED_PAYMENT, finalizeResult{
		main: ter.TecUNFUNDED_PAYMENT, reward: ter.TesSUCCESS, remove: ter.TesSUCCESS,
	}.accountCreateAttestationFatalResult())
	assert.Equal(t, ter.TecUNFUNDED_PAYMENT, finalizeResult{
		main: ter.TesSUCCESS, reward: ter.TecUNFUNDED_PAYMENT, remove: ter.TesSUCCESS,
	}.accountCreateAttestationFatalResult())
	assert.Equal(t, ter.TecINVARIANT_FAILED, finalizeResult{
		main: ter.TecXCHAIN_PAYMENT_FAILED, reward: ter.TecINVARIANT_FAILED, remove: ter.TesSUCCESS,
	}.accountCreateAttestationFatalResult())
}

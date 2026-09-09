package vault

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestClosedEndedGapBoundaries(t *testing.T) {
	const subscription uint32 = 100
	cases := []struct {
		name       string
		redemption uint32
		wantValid  bool
	}{
		{name: "below minimum", redemption: subscription + 179},
		{name: "minimum", redemption: subscription + 180, wantValid: true},
		{name: "maximum minus one", redemption: subscription + 946_708_559, wantValid: true},
		{name: "maximum", redemption: subscription + 946_708_560},
		{name: "reverse dates", redemption: subscription - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidClosedEndedGap(subscription, tc.redemption); got != tc.wantValid {
				t.Fatalf("IsValidClosedEndedGap(%d, %d) = %v, want %v", subscription, tc.redemption, got, tc.wantValid)
			}
		})
	}

	if !IsValidClosedEndedGap(^uint32(0)-180, ^uint32(0)) {
		t.Fatal("valid gap near uint32 maximum was rejected")
	}
}

func TestVaultCreateClosedEndedValidation(t *testing.T) {
	valid := func(kind uint8, subscription, redemption *uint32) *VaultCreate {
		v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
		v.VaultKind = &kind
		v.SubscriptionDate = subscription
		v.RedemptionDate = redemption
		return v
	}

	subscription := uint32(100)
	redemption := uint32(280)
	cases := []struct {
		name string
		tx   *VaultCreate
		want ter.Result
	}{
		{name: "open-ended without dates", tx: valid(VaultKindOpenEnded, nil, nil)},
		{name: "unknown kind", tx: valid(2, nil, nil), want: ter.TemMALFORMED},
		{name: "open-ended dates forbidden", tx: valid(VaultKindOpenEnded, &subscription, &redemption), want: ter.TemMALFORMED},
		{name: "closed-ended dates required", tx: valid(VaultKindClosedEnded, &subscription, nil), want: ter.TemMALFORMED},
		{name: "closed-ended valid minimum", tx: valid(VaultKindClosedEnded, &subscription, func() *uint32 { v := subscription + 180; return &v }())},
		{name: "closed-ended gap too short", tx: valid(VaultKindClosedEnded, &subscription, func() *uint32 { v := subscription + 179; return &v }()), want: ter.TemMALFORMED},
		{name: "closed-ended gap too long", tx: valid(VaultKindClosedEnded, &subscription, func() *uint32 { v := subscription + 946_708_560; return &v }()), want: ter.TemMALFORMED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tx.Validate()
			if tc.want == ter.TesSUCCESS {
				if err != nil {
					t.Fatalf("Validate() = %v, want success", err)
				}
				return
			}
			if got := vaultResultCode(t, err); got != tc.want {
				t.Fatalf("Validate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVaultCreateLifecycleFieldsGateAndRoundTrip(t *testing.T) {
	kind := VaultKindClosedEnded
	subscription := uint32(100)
	redemption := uint32(280)
	v := NewVaultCreate("rOwner", tx.Asset{Currency: "XRP"})
	v.VaultKind = &kind
	v.SubscriptionDate = &subscription
	v.RedemptionDate = &redemption

	legacyRules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV1,
	})
	if got := vaultResultCode(t, v.CheckExtraFeatures(legacyRules)); got != ter.TemDISABLED {
		t.Fatalf("disabled lifecycle gate = %v, want temDISABLED", got)
	}
	enabledRules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV1,
		amendment.FeatureLendingProtocolV1_1,
	})
	if err := v.CheckExtraFeatures(enabledRules); err != nil {
		t.Fatalf("enabled lifecycle gate = %v", err)
	}
	flat, err := v.Flatten()
	if err != nil {
		t.Fatalf("Flatten() = %v", err)
	}
	for name := range map[string]any{
		"VaultKind":        uint8(1),
		"SubscriptionDate": uint32(100),
		"RedemptionDate":   uint32(280),
	} {
		if _, ok := flat[name]; !ok {
			t.Fatalf("Flatten() omitted %s: %#v", name, flat)
		}
	}
}

func TestVaultCreateApplyPersistsClosedEndedLifecycle(t *testing.T) {
	view := newMPTArmsView()
	var ownerID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x19
	}
	rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureLendingProtocolV1_1).Build()
	ctx := buildArmsCtx(t, view, ownerID, rules)
	sequence := uint32(1)
	kind := VaultKindClosedEnded
	subscription, redemption := uint32(100), uint32(280)
	create := NewVaultCreate(ctx.Account.Account, tx.Asset{Currency: "XRP"})
	create.Common.Sequence = &sequence
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	if got := create.Apply(ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}
	created, err := readVault(view, keylet.Vault(ownerID, sequence))
	if err != nil || created == nil {
		t.Fatalf("read created vault: vault=%v err=%v", created, err)
	}
	if created.VaultKind != VaultKindClosedEnded {
		t.Fatalf("VaultKind = %d, want %d", created.VaultKind, VaultKindClosedEnded)
	}
	if created.SubscriptionDate == nil || *created.SubscriptionDate != subscription {
		t.Fatalf("SubscriptionDate = %v, want %d", created.SubscriptionDate, subscription)
	}
	if created.RedemptionDate == nil || *created.RedemptionDate != redemption {
		t.Fatalf("RedemptionDate = %v, want %d", created.RedemptionDate, redemption)
	}
}

func TestVaultCreateRejectsExpiredLifecycleDates(t *testing.T) {
	var ownerID [20]byte
	for i := range ownerID {
		ownerID[i] = 0x27
	}
	rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureLendingProtocolV1_1).Build()
	view := newMPTArmsView()
	ctx := buildArmsCtx(t, view, ownerID, rules)
	kind := VaultKindClosedEnded
	subscription, redemption := uint32(100), uint32(280)
	create := NewVaultCreate(ctx.Account.Account, tx.Asset{Currency: "XRP"})
	create.Common.Sequence = func() *uint32 { value := uint32(1); return &value }()
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption

	for _, tc := range []struct {
		name string
		now  uint32
		want ter.Result
	}{
		{name: "before subscription", now: subscription - 1, want: ter.TesSUCCESS},
		{name: "at subscription", now: subscription, want: ter.TecEXPIRED},
		{name: "after subscription", now: subscription + 1, want: ter.TecEXPIRED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := tx.EngineConfig{Rules: rules, ParentCloseTime: tc.now, ParentHash: ctx.Config.ParentHash}
			if got := create.Preclaim(view, config); got != tc.want {
				t.Fatalf("Preclaim() at %d = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

func TestVaultPhaseBoundaries(t *testing.T) {
	subscription, redemption := uint32(100), uint32(280)
	cases := []struct {
		now  uint32
		want VaultPhase
	}{
		{now: 99, want: VaultPhaseSubscription},
		{now: 100, want: VaultPhaseSubscription},
		{now: 101, want: VaultPhaseInvestment},
		{now: 279, want: VaultPhaseInvestment},
		{now: 280, want: VaultPhaseRedemption},
	}
	for _, tc := range cases {
		if got := GetVaultPhase(VaultKindClosedEnded, &subscription, &redemption, tc.now); got != tc.want {
			t.Errorf("phase at %d = %v, want %v", tc.now, got, tc.want)
		}
	}
	if got := GetVaultPhase(VaultKindOpenEnded, &subscription, &redemption, 101); got != VaultPhaseNoPhase {
		t.Fatalf("open-ended phase = %v, want no phase", got)
	}
	if got := GetVaultPhase(VaultKindClosedEnded, nil, &redemption, 101); got != VaultPhaseSubscription {
		t.Fatalf("missing subscription date phase = %v, want subscription", got)
	}
	if got := GetVaultPhase(VaultKindClosedEnded, &subscription, nil, 101); got != VaultPhaseInvestment {
		t.Fatalf("missing redemption date phase = %v, want investment", got)
	}
}

func TestVaultSetRejectsImmutableLifecycleFields(t *testing.T) {
	for _, field := range []string{"VaultKind", "SubscriptionDate", "RedemptionDate"} {
		t.Run(field, func(t *testing.T) {
			values := map[string]any{
				"TransactionType": "VaultSet",
				"Account":         "rOwner",
				"VaultID":         strings.Repeat("01", 32),
				"Data":            "01",
			}
			if field == "VaultKind" {
				values[field] = uint8(1)
			} else {
				values[field] = uint32(100)
			}
			if err := tx.ValidateTemplateFields(tx.TypeVaultSet, values); err == nil {
				t.Fatalf("ValidateTemplateFields accepted immutable %s", field)
			}
			set := NewVaultSet("rOwner", strings.Repeat("01", 32))
			set.Data = "01"
			set.Common.SetPresentFields(map[string]bool{field: true})
			if got := vaultResultCode(t, set.Validate()); got != ter.TemMALFORMED {
				t.Fatalf("VaultSet.Validate() = %v, want temMALFORMED", got)
			}
		})
	}
}

func TestVaultDepositAndWithdrawPhaseGatesPrecedeAssetChecks(t *testing.T) {
	view, deposit, _, _ := vaultDepositFixture(t, 0, true)
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureLendingProtocolV1_1).Build()
	vaultID, ok := deposit.vaultIDBytes()
	if !ok {
		t.Fatal("fixture VaultID is malformed")
	}
	vd, err := readVault(view, keylet.VaultByID(vaultID))
	if err != nil || vd == nil {
		t.Fatalf("read fixture vault: %v", err)
	}
	subscription, redemption := uint32(100), uint32(280)
	vd.VaultKind = VaultKindClosedEnded
	vd.SubscriptionDate = &subscription
	vd.RedemptionDate = &redemption
	raw, err := serializeVault(vd)
	if err != nil {
		t.Fatalf("serialize fixture vault: %v", err)
	}
	view.data[keylet.VaultByID(vaultID).Key] = raw

	wrongAsset := *deposit
	wrongAsset.Amount = tx.NewXRPAmount(1)
	enabled := tx.EngineConfig{Rules: view.rules, ParentCloseTime: 101}
	if got := wrongAsset.Preclaim(view, enabled); got != ter.TecEXPIRED {
		t.Fatalf("deposit in investment with wrong asset = %v, want tecEXPIRED", got)
	}
	legacyRules := amendment.NewRules([][32]byte{
		amendment.FeatureMPTokensV1,
		amendment.FeatureSingleAssetVault,
	})
	legacy := tx.EngineConfig{Rules: legacyRules, ParentCloseTime: 101}
	if got := wrongAsset.Preclaim(view, legacy); got == ter.TecEXPIRED {
		t.Fatal("legacy deposit unexpectedly used closed-ended phase gate")
	}

	ownerAddress := deposit.Account
	withdraw := NewVaultWithdraw(ownerAddress, strings.ToUpper(hex.EncodeToString(vaultID[:])), tx.NewXRPAmount(1))
	ro := roView{data: view.data, rules: view.rules}
	if got := withdraw.Preclaim(ro, enabled); got != ter.TecTOO_SOON {
		t.Fatalf("withdraw in investment = %v, want tecTOO_SOON", got)
	}
	if got := withdraw.Preclaim(ro, legacy); got == ter.TecTOO_SOON {
		t.Fatal("legacy withdraw unexpectedly used closed-ended phase gate")
	}
}

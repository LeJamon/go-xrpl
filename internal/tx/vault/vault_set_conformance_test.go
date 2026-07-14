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

func TestVaultSetPermissionedDomainsGateUsesFieldPresence(t *testing.T) {
	disabled := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	enabled := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeaturePermissionedDomains,
	})

	tests := []struct {
		name     string
		domainID string
		present  bool
		wantGate bool
	}{
		{name: "absent", wantGate: false},
		{name: "nonzero", domainID: "1", wantGate: true},
		{name: "zero", domainID: "0", present: true, wantGate: true},
		{name: "present empty", present: true, wantGate: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := NewVaultSet("rOwner", strings.Repeat("01", 32))
			set.DomainID = test.domainID
			if test.present {
				set.Common.SetPresentFields(map[string]bool{"DomainID": true})
			}

			err := set.CheckExtraFeatures(disabled)
			if test.wantGate {
				if got := vaultResultCode(t, err); got != ter.TemDISABLED {
					t.Fatalf("CheckExtraFeatures() = %v, want temDISABLED", got)
				}
			} else if err != nil {
				t.Fatalf("CheckExtraFeatures() = %v, want nil", err)
			}
			if err := set.CheckExtraFeatures(enabled); err != nil {
				t.Fatalf("enabled CheckExtraFeatures() = %v", err)
			}
		})
	}
}

func TestVaultSetRejectsPresentEmptyDataBeforeNoOp(t *testing.T) {
	set := NewVaultSet("rOwner", strings.Repeat("01", 32))
	set.Common.SetPresentFields(map[string]bool{"Data": true})
	if got := set.Validate(); got != ErrVaultDataEmpty {
		t.Fatalf("Validate() = %v, want %v", got, ErrVaultDataEmpty)
	}
}

func TestVaultSetAcceptsNegativeZeroAssetsMaximum(t *testing.T) {
	maximum := "-0"
	set := NewVaultSet("rOwner", strings.Repeat("01", 32))
	set.AssetsMaximum = &maximum
	if err := set.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestVaultSetRejectsMalformedProgrammaticAssetsMaximum(t *testing.T) {
	maximum := "not-a-number"
	set := NewVaultSet("rOwner", strings.Repeat("01", 32))
	set.AssetsMaximum = &maximum
	if got := vaultResultCode(t, set.Validate()); got != ter.TemMALFORMED {
		t.Fatalf("Validate() = %v, want temMALFORMED", got)
	}
}

func TestVaultSetAssetsMaximumUsesRawValueForLimitThenCanonicalizes(t *testing.T) {
	var vaultID [32]byte
	for i := range vaultID {
		vaultID[i] = 1
	}
	vaultKey := keylet.VaultByID(vaultID)
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})

	build := func(t *testing.T, total string) (*VaultSet, *tx.ApplyContext, *mptArmsView) {
		t.Helper()
		view := newMPTArmsView()
		encoded, err := serializeVault(&vaultData{
			Owner:            [20]byte{1},
			Account:          [20]byte{2},
			Sequence:         1,
			ShareMPTID:       [24]byte{3},
			Asset:            tx.Asset{Currency: "XRP"},
			WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
			AssetsTotal:      total,
		})
		if err != nil {
			t.Fatalf("serialize vault: %v", err)
		}
		view.data[vaultKey.Key] = encoded
		maximum := "0.4"
		set := NewVaultSet("rOwner", strings.ToUpper(hex.EncodeToString(vaultID[:])))
		set.AssetsMaximum = &maximum
		ctx := &tx.ApplyContext{View: view, Config: tx.EngineConfig{Rules: rules}}
		return set, ctx, view
	}

	t.Run("raw value below total takes precedence", func(t *testing.T) {
		set, ctx, _ := build(t, "1e0")
		if got := set.Apply(ctx); got != ter.TecLIMIT_EXCEEDED {
			t.Fatalf("Apply() = %v, want tecLIMIT_EXCEEDED", got)
		}
	})

	t.Run("rounded zero removes default field", func(t *testing.T) {
		set, ctx, view := build(t, "")
		if got := set.Apply(ctx); got != ter.TesSUCCESS {
			t.Fatalf("Apply() = %v, want tesSUCCESS", got)
		}
		updated, err := readVault(view, vaultKey)
		if err != nil {
			t.Fatalf("read updated vault: %v", err)
		}
		if updated.AssetsMaximum != "" {
			t.Fatalf("AssetsMaximum = %q, want default removal", updated.AssetsMaximum)
		}
	})
}

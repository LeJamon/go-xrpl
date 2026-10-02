package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

func TestCurrentCloseTimePreservesExplicitZero(t *testing.T) {
	cfg := EngineConfig{
		ParentCloseTime:         123,
		ApplicationCloseTime:    0,
		ApplicationCloseTimeSet: true,
	}
	if got := cfg.CurrentCloseTime(); got != 0 {
		t.Fatalf("CurrentCloseTime() = %d, want explicit zero", got)
	}

	cfg.ApplicationCloseTimeSet = false
	if got := cfg.CurrentCloseTime(); got != cfg.ParentCloseTime {
		t.Fatalf("CurrentCloseTime() = %d, want parent %d", got, cfg.ParentCloseTime)
	}
}

func TestNumberContextForRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rules *amendment.Rules
		want  state.MantissaScale
	}{
		{name: "no ledger rules", want: state.MantissaScaleLarge330},
		{name: "no large-number amendment", rules: amendment.EmptyRules(), want: state.MantissaScaleSmall},
		{
			name:  "vault before cleanup fix",
			rules: amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault}),
			want:  state.MantissaScaleLargeLegacy,
		},
		{
			name: "lending after cleanup fix",
			rules: amendment.NewRules([][32]byte{
				amendment.FeatureLendingProtocol,
				amendment.FeatureFixCleanup3_2_0,
			}),
			want: state.MantissaScaleLarge320,
		},
		{
			name: "vault after 3.3 cleanup fix",
			rules: amendment.NewRules([][32]byte{
				amendment.FeatureSingleAssetVault,
				amendment.FeatureFixCleanup3_3_0,
			}),
			want: state.MantissaScaleLarge330,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := NumberContextForRules(test.rules).Scale(); got != test.want {
				t.Fatalf("NumberContextForRules().Scale() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestNumberContextForRulesSelectsMPTokensV2(t *testing.T) {
	disabled := NumberContextForRules(amendment.EmptyRules())
	if disabled.MPTokensV2Enabled() {
		t.Fatal("MPTokensV2 must be disabled when the amendment is absent")
	}

	enabled := NumberContextForRules(amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2}))
	if !enabled.MPTokensV2Enabled() {
		t.Fatal("MPTokensV2 must follow the explicit rules set")
	}

	outsideLedger := NumberContextForRules(nil)
	if outsideLedger.MPTokensV2Enabled() {
		t.Fatal("MPTokensV2 must remain disabled without transaction rules")
	}
}

func TestApplyContextNumberContextUsesConfigRules(t *testing.T) {
	t.Parallel()

	rules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_2_0,
	})
	ctx := ApplyContext{Config: EngineConfig{Rules: rules}}
	if got := ctx.NumberContext().Scale(); got != state.MantissaScaleLarge320 {
		t.Fatalf("ApplyContext.NumberContext().Scale() = %d, want %d", got, state.MantissaScaleLarge320)
	}
}

func TestEngineConfigNumberContextOverride(t *testing.T) {
	t.Parallel()

	override := state.NewNumberContext(state.MantissaScaleSmall, false)
	cfg := EngineConfig{
		Rules:                 amendment.AllSupportedRules(),
		NumberContextOverride: &override,
	}
	if got := cfg.NumberContext().Scale(); got != state.MantissaScaleSmall {
		t.Fatalf("NumberContext().Scale() = %d, want %d", got, state.MantissaScaleSmall)
	}
}

func TestEngineConfigNumberContextOverrideInheritsMPTokensV2(t *testing.T) {
	override := state.NewNumberContext(state.MantissaScaleSmall, false)
	cfg := EngineConfig{
		Rules:                 amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2}),
		NumberContextOverride: &override,
	}
	if !cfg.NumberContext().MPTokensV2Enabled() {
		t.Fatal("NumberContext override masked enabled MPTokensV2 rules")
	}

	cfg.Rules = amendment.EmptyRules()
	if cfg.NumberContext().MPTokensV2Enabled() {
		t.Fatal("NumberContext override enabled MPTokensV2 without rules")
	}
}

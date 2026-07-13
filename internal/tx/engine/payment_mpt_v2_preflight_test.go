package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const validPreflightMPTID = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"

func mptPaymentRules(v2 bool) *amendment.Rules {
	b := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported)
	if v2 {
		b.Enable(amendment.FeatureMPTokensV2)
	} else {
		b.Disable(amendment.FeatureMPTokensV2)
	}
	return b.Build()
}

func validMPTPreflightPayment(value int64) *payment.Payment {
	amount := state.NewMPTAmountWithIssuanceID(value, precedenceGenesisAddr, validPreflightMPTID)
	return preflightPayment(precedenceGenesisAddr, amount)
}

func TestPaymentMPTFlagSurfaceByVersion(t *testing.T) {
	flags := []struct {
		name string
		flag uint32
	}{
		{name: "tfNoRippleDirect", flag: payment.PaymentFlagNoDirectRipple},
		{name: "tfLimitQuality", flag: payment.PaymentFlagLimitQuality},
	}

	for _, tc := range flags {
		t.Run("MPTokensV1/"+tc.name, func(t *testing.T) {
			p := validMPTPreflightPayment(100)
			p.SetFlags(tc.flag)
			if got := preflightEngine(mptPaymentRules(false)).preflight(p); got != ter.TemINVALID_FLAG {
				t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
			}
		})

		t.Run("MPTokensV2/"+tc.name, func(t *testing.T) {
			p := validMPTPreflightPayment(100)
			p.SetFlags(tc.flag)
			if got := preflightEngine(mptPaymentRules(true)).preflight(p); got != ter.TesSUCCESS {
				t.Fatalf("preflight = %v, want TesSUCCESS", got)
			}
		})
	}

	t.Run("MPTokensV2 rejects undefined bit", func(t *testing.T) {
		p := validMPTPreflightPayment(100)
		p.SetFlags(0x00000001)
		if got := preflightEngine(mptPaymentRules(true)).preflight(p); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
		}
	})
}

func TestPaymentMPTokensV1DisabledPrecedence(t *testing.T) {
	rules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureMPTokensV1).
		Disable(amendment.FeatureMPTokensV2).
		Build()

	t.Run("invalid mask beats bad fee and disabled gate", func(t *testing.T) {
		p := validMPTPreflightPayment(100)
		p.Fee = "-10"
		p.SetFlags(payment.PaymentFlagLimitQuality)
		if got := preflightEngine(rules).preflight(p); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
		}
	})

	t.Run("bad fee beats disabled gate", func(t *testing.T) {
		p := validMPTPreflightPayment(100)
		p.Fee = "-10"
		if got := preflightEngine(rules).preflight(p); got != ter.TemBAD_FEE {
			t.Fatalf("preflight = %v, want TemBAD_FEE", got)
		}
	})

	t.Run("valid common fields reach disabled gate", func(t *testing.T) {
		if got := preflightEngine(rules).preflight(validMPTPreflightPayment(100)); got != ter.TemDISABLED {
			t.Fatalf("preflight = %v, want TemDISABLED", got)
		}
	})

	bodyErrors := []struct {
		name   string
		mutate func(*payment.Payment)
	}{
		{
			name: "paths",
			mutate: func(p *payment.Payment) {
				p.Paths = [][]payment.PathStep{{{Account: precedenceSourceAddr}}}
			},
		},
		{
			name: "cross-asset SendMax",
			mutate: func(p *payment.Payment) {
				sendMax := state.NewXRPAmountFromInt(100)
				p.SendMax = &sendMax
			},
		},
		{
			name: "zero Amount",
			mutate: func(p *payment.Payment) {
				p.Amount = state.NewMPTAmountWithIssuanceID(0, precedenceGenesisAddr, validPreflightMPTID)
			},
		},
	}

	for _, tc := range bodyErrors {
		t.Run("disabled gate beats "+tc.name, func(t *testing.T) {
			p := validMPTPreflightPayment(100)
			tc.mutate(p)
			if got := preflightEngine(rules).preflight(p); got != ter.TemDISABLED {
				t.Fatalf("preflight = %v, want TemDISABLED", got)
			}
		})
	}
}

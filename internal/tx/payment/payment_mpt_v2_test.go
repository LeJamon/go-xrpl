package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const (
	paymentMPTIDA        = "00000000000000000000000000000000000000000000000A"
	paymentMPTIDB        = "00000000000000000000000000000000000000000000000B"
	paymentZeroIssuerMPT = "000000010000000000000000000000000000000000000000"
)

func paymentMPTAmount(value int64, id string) tx.Amount {
	return state.NewMPTAmountWithIssuanceID(value, "rIssuer", id)
}

func paymentMPTV1Rules() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureMPTokensV1})
}

func paymentMPTV2Rules() *amendment.Rules {
	return amendment.NewRules([][32]byte{
		amendment.FeatureMPTokensV1,
		amendment.FeatureMPTokensV2,
	})
}

func requirePaymentResultError(t *testing.T, err error, want ter.Result) {
	t.Helper()
	resultErr, ok := ter.AsResultError(err)
	if !ok || resultErr.Code != want {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestPaymentMPTV2FlagsMaskUsesAmountAsset(t *testing.T) {
	mptAmount := paymentMPTAmount(100, paymentMPTIDA)
	mptSendMax := paymentMPTAmount(110, paymentMPTIDA)

	tests := []struct {
		name    string
		payment *Payment
		rules   *amendment.Rules
		want    uint32
	}{
		{
			name:    "MPT amount under MPTokensV1",
			payment: NewPayment("rAlice", "rBob", mptAmount),
			rules:   paymentMPTV1Rules(),
			want:    tfMPTPaymentMask,
		},
		{
			name:    "MPT amount under MPTokensV2",
			payment: NewPayment("rAlice", "rBob", mptAmount),
			rules:   paymentMPTV2Rules(),
			want:    tfPaymentMask,
		},
		{
			name: "non-MPT amount with MPT SendMax under MPTokensV1",
			payment: func() *Payment {
				p := NewPayment("rAlice", "rBob", xrpAmount("100"))
				p.SendMax = &mptSendMax
				return p
			}(),
			rules: paymentMPTV1Rules(),
			want:  tfPaymentMask,
		},
		{
			name: "non-MPT amount with MPT SendMax under MPTokensV2",
			payment: func() *Payment {
				p := NewPayment("rAlice", "rBob", xrpAmount("100"))
				p.SendMax = &mptSendMax
				return p
			}(),
			rules: paymentMPTV2Rules(),
			want:  tfPaymentMask,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.payment.GetFlagsMask(test.rules); got != test.want {
				t.Fatalf("GetFlagsMask() = 0x%08X, want 0x%08X", got, test.want)
			}
		})
	}
}

func TestPaymentMPTV2RulesAwareValidation(t *testing.T) {
	newMPTPayment := func() *Payment {
		return NewPayment("rAlice", "rBob", paymentMPTAmount(100, paymentMPTIDA))
	}

	tests := []struct {
		name   string
		build  func() *Payment
		v1Code ter.Result
	}{
		{
			name: "MPT path",
			build: func() *Payment {
				p := newMPTPayment()
				p.Paths = [][]PathStep{{{MPTIssuanceID: paymentMPTIDB}}}
				return p
			},
			v1Code: ter.TemMALFORMED,
		},
		{
			name: "MPT destination with XRP SendMax",
			build: func() *Payment {
				p := newMPTPayment()
				sendMax := xrpAmount("110")
				p.SendMax = &sendMax
				return p
			},
			v1Code: ter.TemMALFORMED,
		},
		{
			name: "IOU destination with MPT SendMax",
			build: func() *Payment {
				p := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rIssuer"))
				sendMax := paymentMPTAmount(110, paymentMPTIDA)
				p.SendMax = &sendMax
				return p
			},
			v1Code: ter.TemMALFORMED,
		},
		{
			name: "limit quality",
			build: func() *Payment {
				p := newMPTPayment()
				p.SetFlags(PaymentFlagLimitQuality)
				return p
			},
			v1Code: ter.TemBAD_SEND_XRP_LIMIT,
		},
		{
			name: "no direct ripple",
			build: func() *Payment {
				p := newMPTPayment()
				p.SetFlags(PaymentFlagNoDirectRipple)
				return p
			},
			v1Code: ter.TemBAD_SEND_XRP_NO_DIRECT,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.build().Validate(); err != nil {
				t.Fatalf("amendment-neutral Validate() returned %v", err)
			}

			requirePaymentResultError(t, test.build().PreflightWithRules(paymentMPTV1Rules()), test.v1Code)

			if err := test.build().PreflightWithRules(paymentMPTV2Rules()); err != nil {
				t.Fatalf("MPTokensV2 preflight returned %v", err)
			}
		})
	}
}

func TestPaymentMPTV2SelfPaymentUsesIssuanceIdentity(t *testing.T) {
	amountA := paymentMPTAmount(100, paymentMPTIDA)
	sameIssue := paymentMPTAmount(110, paymentMPTIDA)
	differentIssue := paymentMPTAmount(110, paymentMPTIDB)

	if !equalTokens(amountA, sameIssue) {
		t.Fatal("equalTokens() rejected the same MPT issuance")
	}
	if equalTokens(amountA, differentIssue) {
		t.Fatal("equalTokens() accepted different MPT issuances")
	}

	same := NewPayment("rAlice", "rAlice", amountA)
	same.SendMax = &sameIssue
	requirePaymentResultError(t, same.PreflightWithRules(paymentMPTV2Rules()), ter.TemREDUNDANT)

	different := NewPayment("rAlice", "rAlice", amountA)
	different.SendMax = &differentIssue
	if err := different.PreflightWithRules(paymentMPTV2Rules()); err != nil {
		t.Fatalf("different-issuance self-payment returned %v", err)
	}
}

func TestPaymentMPTV2RejectsZeroIssuerAsset(t *testing.T) {
	amount := paymentMPTAmount(100, paymentZeroIssuerMPT)
	p := NewPayment("rAlice", "rBob", amount)

	if err := p.Validate(); err != nil {
		t.Fatalf("amendment-neutral Validate() returned %v", err)
	}
	if err := p.PreflightWithRules(paymentMPTV1Rules()); err != nil {
		t.Fatalf("MPTokensV1 preflight returned %v", err)
	}
	requirePaymentResultError(t, p.PreflightWithRules(paymentMPTV2Rules()), ter.TemBAD_CURRENCY)
}

func TestPaymentMPTPathStepValidationAndFlatten(t *testing.T) {
	p := NewPayment("rAlice", "rBob", paymentMPTAmount(100, paymentMPTIDA))
	p.Paths = [][]PathStep{{{MPTIssuanceID: paymentMPTIDB}}}

	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() treated an MPT-only path step as empty: %v", err)
	}
	if err := p.PreflightWithRules(paymentMPTV2Rules()); err != nil {
		t.Fatalf("MPTokensV2 preflight treated an MPT-only path step as empty: %v", err)
	}

	flat, err := p.Flatten()
	if err != nil {
		t.Fatalf("Flatten() returned %v", err)
	}
	paths, ok := flat["Paths"].([]any)
	if !ok || len(paths) != 1 {
		t.Fatalf("Paths = %#v, want one path", flat["Paths"])
	}
	steps, ok := paths[0].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("path = %#v, want one step", paths[0])
	}
	step, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("step = %#v, want map[string]any", steps[0])
	}
	if got := step["mpt_issuance_id"]; got != paymentMPTIDB {
		t.Fatalf("mpt_issuance_id = %v, want %s", got, paymentMPTIDB)
	}
}

package payment

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// Synthetic steps exceed the ledger's supply and request limits to exercise
// both aggregate safeguards independently of the book and funding limits.
type aggregateTestStep struct {
	fakeStep
	in, out EitherAmount
	write   func(*PaymentSandbox)
}

func TestFlowContinuesAfterBookAggregateOverflow(t *testing.T) {
	for _, makeAmount := range []func(int64) EitherAmount{
		NewXRPEitherAmount,
		func(n int64) EitherAmount { return NewMPTEitherAmount(n, [24]byte{1}) },
	} {
		view := newPaymentMockLedgerView()
		marker := keylet.Keylet{Key: [32]byte{42}}
		view.data[marker.Key] = []byte{0}
		base := NewPaymentSandbox(view)
		attempts := 0
		bad := &aggregateTestStep{write: func(sb *PaymentSandbox) {
			attempts++
			require.NoError(t, sb.Update(marker, []byte{1}))
			var book amountMultiset
			book.insert(makeAmount(math.MaxInt64))
			book.insert(makeAmount(1))
			book.sum(makeAmount(0), state.NewNumberContext(state.MantissaScaleSmall, false))
		}}
		good := &aggregateTestStep{
			in: makeAmount(1), out: makeAmount(1), write: func(*PaymentSandbox) {},
		}
		result := Flow(base, []Strand{{bad}, {good}}, makeAmount(1), false, nil, nil, nil, false)
		require.Equal(t, 1, attempts)
		require.Equal(t, ter.TesSUCCESS, result.Result)
		require.Equal(t, makeAmount(1), result.In)
		require.Equal(t, makeAmount(1), result.Out)
		require.NotNil(t, result.Sandbox)
		require.NoError(t, result.Sandbox.Apply(base))
		require.NoError(t, base.ApplyToView(view))
		require.Equal(t, []byte{0}, view.data[marker.Key])
	}
}

func (s *aggregateTestStep) Rev(sb, _ *PaymentSandbox, _ map[[32]byte]bool, _ EitherAmount) (EitherAmount, EitherAmount) {
	s.write(sb)
	return s.in, s.out
}

func (s *aggregateTestStep) Fwd(sb, _ *PaymentSandbox, _ map[[32]byte]bool, _ EitherAmount) (EitherAmount, EitherAmount) {
	s.write(sb)
	return s.in, s.out
}

func (s *aggregateTestStep) CachedIn() *EitherAmount                  { return &s.in }
func (s *aggregateTestStep) CachedOut() *EitherAmount                 { return &s.out }
func (s *aggregateTestStep) EqualIn(EitherAmount, EitherAmount) bool  { return true }
func (s *aggregateTestStep) EqualOut(EitherAmount, EitherAmount) bool { return true }

func TestFlowAggregateOverflow(t *testing.T) {
	assets := []struct {
		name string
		make func(int64) EitherAmount
	}{
		{"XRP", NewXRPEitherAmount},
		{"MPT", func(n int64) EitherAmount { return NewMPTEitherAmount(n, [24]byte{1}) }},
		{"IOU", func(n int64) EitherAmount { return NewIOUEitherAmount(tx.NewIssuedAmount(n, 0, "USD", "issuer")) }},
	}
	for _, inAsset := range assets {
		for _, outAsset := range assets {
			for _, side := range []string{"input", "output"} {
				if (side == "input" && inAsset.name == "IOU") || (side == "output" && outAsset.name == "IOU") {
					continue
				}
				for _, hasSendMax := range []bool{false, true} {
					for _, partial := range []bool{false, true} {
						name := inAsset.name + "_" + outAsset.name + "/" + side
						if hasSendMax {
							name += "/send_max"
						}
						if partial {
							name += "/partial"
						}
						t.Run(name, func(t *testing.T) {
							view := newPaymentMockLedgerView()
							marker := keylet.Keylet{Key: [32]byte{42}}
							view.data[marker.Key] = []byte{0}
							base := NewPaymentSandbox(view)
							writes := byte(0)
							step := &aggregateTestStep{
								in: inAsset.make(1), out: outAsset.make(1),
								write: func(sb *PaymentSandbox) {
									writes++
									require.NoError(t, sb.Update(marker, []byte{writes}))
								},
							}
							requested := outAsset.make(2)
							if side == "input" {
								step.in = inAsset.make(math.MaxInt64 - 1)
							} else {
								step.out = outAsset.make(math.MaxInt64 - 1)
								requested = outAsset.make(math.MaxInt64)
							}
							var sendMax *EitherAmount
							if hasSendMax {
								limit := inAsset.make(math.MaxInt64)
								sendMax = &limit
							}
							result := Flow(base, []Strand{{step}}, requested, partial, nil, sendMax, nil, false)
							require.Equal(t, ter.TecPATH_DRY, result.Result)
							require.Equal(t, inAsset.make(0), result.In)
							require.Equal(t, outAsset.make(0), result.Out)
							require.Nil(t, result.Sandbox)
							require.GreaterOrEqual(t, writes, byte(2))
							value, err := base.Read(marker)
							require.NoError(t, err)
							require.Equal(t, []byte{0}, value)
							require.Equal(t, []byte{0}, view.data[marker.Key])
						})
					}
				}
			}
		}
	}
}

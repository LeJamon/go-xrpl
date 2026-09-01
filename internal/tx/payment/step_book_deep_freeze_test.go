package payment

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type deepFreezeReadErrorView struct {
	*paymentMockLedgerView
	key     [32]byte
	err     error
	failAll bool
	reads   int
}

func (v *deepFreezeReadErrorView) Read(k keylet.Keylet) ([]byte, error) {
	if v.failAll || k.Key == v.key {
		v.reads++
		return nil, v.err
	}
	return v.paymentMockLedgerView.Read(k)
}

func TestBookStepIsDeepFrozen(t *testing.T) {
	var account, issuer [20]byte
	account[19] = 1
	issuer[19] = 2
	step := &BookStep{}

	t.Run("XRP short-circuits without a read", func(t *testing.T) {
		view := &deepFreezeReadErrorView{
			paymentMockLedgerView: newPaymentMockLedgerView(),
			err:                   errors.New("unexpected read"),
			failAll:               true,
		}
		frozen, err := step.isDeepFrozen(NewPaymentSandbox(view), account, "XRP", issuer)
		require.NoError(t, err)
		require.False(t, frozen)
		require.Zero(t, view.reads)
	})

	t.Run("issuer self short-circuits without a read", func(t *testing.T) {
		view := &deepFreezeReadErrorView{
			paymentMockLedgerView: newPaymentMockLedgerView(),
			err:                   errors.New("unexpected read"),
			failAll:               true,
		}
		frozen, err := step.isDeepFrozen(NewPaymentSandbox(view), issuer, "USD", issuer)
		require.NoError(t, err)
		require.False(t, frozen)
		require.Zero(t, view.reads)
	})

	t.Run("missing trust line is not frozen", func(t *testing.T) {
		frozen, err := step.isDeepFrozen(NewPaymentSandbox(newPaymentMockLedgerView()), account, "USD", issuer)
		require.NoError(t, err)
		require.False(t, frozen)
	})

	t.Run("read error is preserved", func(t *testing.T) {
		sentinel := errors.New("trust line read failed")
		view := &deepFreezeReadErrorView{
			paymentMockLedgerView: newPaymentMockLedgerView(),
			key:                   keylet.Line(account, issuer, "USD").Key,
			err:                   sentinel,
		}
		frozen, err := step.isDeepFrozen(NewPaymentSandbox(view), account, "USD", issuer)
		require.False(t, frozen)
		require.ErrorIs(t, err, sentinel)
		require.ErrorContains(t, err, "read deep-freeze trust line")
	})

	t.Run("malformed trust line returns an error", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		view.createTrustLine(account, issuer, "USD", 10, 100, 100)
		lineKey := keylet.Line(account, issuer, "USD").Key
		view.data[lineKey] = view.data[lineKey][:3]
		frozen, err := step.isDeepFrozen(NewPaymentSandbox(view), account, "USD", issuer)
		require.False(t, frozen)
		require.ErrorContains(t, err, "parse deep-freeze trust line")
	})

	for _, tc := range []struct {
		name   string
		flags  uint32
		frozen bool
	}{
		{name: "no flags"},
		{name: "high flag", flags: state.LsfHighDeepFreeze, frozen: true},
		{name: "low flag", flags: state.LsfLowDeepFreeze, frozen: true},
		{name: "both flags", flags: state.LsfHighDeepFreeze | state.LsfLowDeepFreeze, frozen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			view.createTrustLine(account, issuer, "USD", 10, 100, 100)
			lineKey := keylet.Line(account, issuer, "USD")
			line, err := state.ParseRippleState(view.data[lineKey.Key])
			require.NoError(t, err)
			line.Flags = tc.flags
			view.data[lineKey.Key], err = state.SerializeRippleState(line)
			require.NoError(t, err)

			frozen, err := step.isDeepFrozen(NewPaymentSandbox(view), account, "USD", issuer)
			require.NoError(t, err)
			require.Equal(t, tc.frozen, frozen)
		})
	}
}

func TestBookStepDeepFreezeLookupErrorsAbortTraversal(t *testing.T) {
	readSentinel := errors.New("trust line read failed")
	for _, tc := range []struct {
		name      string
		output    bool
		configure func(*paymentMockLedgerView, [32]byte) tx.LedgerView
		checkErr  func(*testing.T, error)
	}{
		{
			name: "input read error",
			configure: func(view *paymentMockLedgerView, lineKey [32]byte) tx.LedgerView {
				return &deepFreezeReadErrorView{paymentMockLedgerView: view, key: lineKey, err: readSentinel}
			},
			checkErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, readSentinel)
				require.ErrorContains(t, err, "read deep-freeze trust line")
			},
		},
		{
			name: "input parse error",
			configure: func(view *paymentMockLedgerView, lineKey [32]byte) tx.LedgerView {
				view.data[lineKey] = view.data[lineKey][:3]
				return view
			},
			checkErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "parse deep-freeze trust line")
			},
		},
		{
			name:   "output funding read error",
			output: true,
			configure: func(view *paymentMockLedgerView, lineKey [32]byte) tx.LedgerView {
				return &deepFreezeReadErrorView{paymentMockLedgerView: view, key: lineKey, err: readSentinel}
			},
			checkErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, readSentinel)
				require.ErrorContains(t, err, "read deep-freeze trust line")
			},
		},
		{
			name:   "output funding parse error",
			output: true,
			configure: func(view *paymentMockLedgerView, lineKey [32]byte) tx.LedgerView {
				view.data[lineKey] = view.data[lineKey][:3]
				return view
			},
			checkErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "parse deep-freeze trust line")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issuer, owner, source, destination [20]byte
			issuer[19] = 1
			owner[19] = 2
			source[19] = 3
			destination[19] = 4

			view := newPaymentMockLedgerView()
			view.createAccount(owner, 1_000_000_000, 1)
			view.createTrustLine(owner, issuer, "USD", 10, 100, 100)
			issuerString := state.EncodeAccountIDSafe(issuer)
			inIssue := Issue{Currency: "USD", Issuer: issuer}
			outIssue := Issue{Currency: "XRP"}
			requestedOut := NewXRPEitherAmount(1)
			if tc.output {
				inIssue, outIssue = outIssue, inIssue
				requestedOut = NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(1, "USD", issuerString))
			}
			step := NewBookStep(inIssue, outIssue, source, destination, nil, false)
			directoryKey := step.bookBaseKey()
			binary.BigEndian.PutUint64(directoryKey[24:], 0x5500000000000000)
			var offerKey [32]byte
			if tc.output {
				offer := &state.LedgerOffer{
					Account:       state.EncodeAccountIDSafe(owner),
					Sequence:      1,
					TakerPays:     tx.NewXRPAmount(100_000_000),
					TakerGets:     tx.NewIssuedAmountFromFloat64(10, "USD", issuerString),
					BookDirectory: directoryKey,
				}
				offerData, err := state.SerializeLedgerOffer(offer)
				require.NoError(t, err)
				offerKey = keylet.Offer(owner, 1).Key
				view.data[offerKey] = offerData
			} else {
				offerKey = insertBookOffer(t, view, owner, issuerString, 1, 0, directoryKey)
			}
			originalOffer := append([]byte(nil), view.data[offerKey]...)
			directory := &state.DirectoryNode{
				RootIndex: directoryKey,
				Indexes:   [][32]byte{offerKey},
			}
			if tc.output {
				directory.TakerGetsCurrency = keylet.CurrencyBytes("USD")
				directory.TakerGetsIssuer = issuer
			} else {
				directory.TakerPaysCurrency = keylet.CurrencyBytes("USD")
				directory.TakerPaysIssuer = issuer
			}
			var err error
			view.data[directoryKey], err = state.SerializeDirectoryNode(directory, true)
			require.NoError(t, err)

			ledgerView := tc.configure(view, keylet.Line(owner, issuer, "USD").Key)
			afView := NewPaymentSandbox(ledgerView)
			sb := NewChildSandbox(afView)
			offersToRemove := make(map[[32]byte]bool)
			offer, gotKey, err := step.getNextOfferSkipVisited(
				sb,
				afView,
				offersToRemove,
				make(map[[32]byte]bool),
				true,
			)
			tc.checkErr(t, err)
			require.Nil(t, offer)
			require.Zero(t, gotKey)
			require.NotContains(t, offersToRemove, offerKey)
			require.NotContains(t, step.PermRemovals(), offerKey)
			require.Equal(t, originalOffer, view.data[offerKey])
			sandboxOffer, err := sb.Read(keylet.Keylet{Key: offerKey})
			require.NoError(t, err)
			require.Equal(t, originalOffer, sandboxOffer)
			quality, err := step.firstCrossableTipQuality(sb, nil, nil)
			tc.checkErr(t, err)
			require.Nil(t, quality)
			flowErr := recoverFlowError(t, func() {
				step.Rev(sb, afView, make(map[[32]byte]bool), requestedOut)
			})
			require.Equal(t, ter.TefINTERNAL, flowErr.ter)
			sandboxOffer, err = sb.Read(keylet.Keylet{Key: offerKey})
			require.NoError(t, err)
			require.Equal(t, originalOffer, sandboxOffer)

			result := ExecuteStrand(
				afView,
				Strand{step},
				nil,
				requestedOut,
			)
			require.False(t, result.Success)
			require.Nil(t, result.Sandbox)
			require.True(t, result.In.IsZero())
			require.True(t, result.Out.IsZero())
			require.NotContains(t, result.OffsToRm, offerKey)
			require.Equal(t, originalOffer, view.data[offerKey])
		})
	}
}

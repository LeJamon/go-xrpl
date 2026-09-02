package payment

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type offerAuthorizationFaultView struct {
	*paymentMockLedgerView
	key       [32]byte
	failAt    int
	reads     int
	err       error
	faultData []byte
}

func (v *offerAuthorizationFaultView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.key {
		v.reads++
		if v.reads == v.failAt {
			return v.faultData, v.err
		}
	}
	return v.paymentMockLedgerView.Read(k)
}

func putRequireAuthIssuer(t *testing.T, view *paymentMockLedgerView, issuer [20]byte) {
	t.Helper()
	view.createAccount(issuer, 1_000_000_000, 0)
	issuerKey := keylet.Account(issuer).Key
	account, err := state.ParseAccountRoot(view.data[issuerKey])
	require.NoError(t, err)
	account.Flags |= state.LsfRequireAuth
	view.data[issuerKey], err = state.SerializeAccountRoot(account)
	require.NoError(t, err)
}

func putAuthorizedTrustLine(t *testing.T, view *paymentMockLedgerView, owner, issuer [20]byte) {
	t.Helper()
	view.createTrustLine(owner, issuer, "USD", 10, 100, 100)
	lineKey := keylet.Line(owner, issuer, "USD").Key
	line, err := state.ParseRippleState(view.data[lineKey])
	require.NoError(t, err)
	line.Flags |= state.LsfHighAuth
	view.data[lineKey], err = state.SerializeRippleState(line)
	require.NoError(t, err)
}

func TestBookStepIsOfferOwnerAuthorizedPreservesLookupErrors(t *testing.T) {
	owner := [20]byte{1}
	issuer := [20]byte{2}
	issuerKey := keylet.Account(issuer).Key
	lineKey := keylet.Line(owner, issuer, "USD").Key
	readSentinel := errors.New("authorization lookup failed")
	step := &BookStep{}

	t.Run("missing issuer does not require authorization", func(t *testing.T) {
		authorized, err := step.isOfferOwnerAuthorized(
			NewPaymentSandbox(newPaymentMockLedgerView()), owner, issuer, "USD",
		)
		require.NoError(t, err)
		require.True(t, authorized)
	})

	t.Run("issuer read error", func(t *testing.T) {
		view := &offerAuthorizationFaultView{
			paymentMockLedgerView: newPaymentMockLedgerView(),
			key:                   issuerKey,
			failAt:                1,
			err:                   readSentinel,
		}
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.False(t, authorized)
		require.ErrorIs(t, err, readSentinel)
		require.ErrorContains(t, err, "read offer issuer account")
	})

	t.Run("issuer parse error", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		putRequireAuthIssuer(t, view, issuer)
		view.data[issuerKey] = view.data[issuerKey][:3]
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.False(t, authorized)
		require.ErrorContains(t, err, "parse offer issuer account")
	})

	t.Run("issuer without require auth", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		view.createAccount(issuer, 1_000_000_000, 0)
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.NoError(t, err)
		require.True(t, authorized)
	})

	t.Run("missing trust line is unauthorized", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		putRequireAuthIssuer(t, view, issuer)
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.NoError(t, err)
		require.False(t, authorized)
	})

	t.Run("trust line read error", func(t *testing.T) {
		base := newPaymentMockLedgerView()
		putRequireAuthIssuer(t, base, issuer)
		view := &offerAuthorizationFaultView{
			paymentMockLedgerView: base,
			key:                   lineKey,
			failAt:                1,
			err:                   readSentinel,
		}
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.False(t, authorized)
		require.ErrorIs(t, err, readSentinel)
		require.ErrorContains(t, err, "read offer owner trust line")
	})

	t.Run("trust line parse error", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		putRequireAuthIssuer(t, view, issuer)
		putAuthorizedTrustLine(t, view, owner, issuer)
		view.data[lineKey] = view.data[lineKey][:3]
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.False(t, authorized)
		require.ErrorContains(t, err, "parse offer owner trust line")
	})

	t.Run("authorized trust line", func(t *testing.T) {
		view := newPaymentMockLedgerView()
		putRequireAuthIssuer(t, view, issuer)
		putAuthorizedTrustLine(t, view, owner, issuer)
		authorized, err := step.isOfferOwnerAuthorized(NewPaymentSandbox(view), owner, issuer, "USD")
		require.NoError(t, err)
		require.True(t, authorized)
	})
}

type offerAuthorizationTraversalFixture struct {
	base          *paymentMockLedgerView
	view          *offerAuthorizationFaultView
	afView        *PaymentSandbox
	step          *BookStep
	offerKey      [32]byte
	originalOffer []byte
	owner         [20]byte
	issuer        [20]byte
}

func newOfferAuthorizationTraversalFixture(t *testing.T) *offerAuthorizationTraversalFixture {
	t.Helper()
	owner := [20]byte{1}
	issuer := [20]byte{2}
	source := [20]byte{3}
	destination := [20]byte{4}
	base := newPaymentMockLedgerView()
	base.createAccount(owner, 1_000_000_000, 1)
	putRequireAuthIssuer(t, base, issuer)
	putAuthorizedTrustLine(t, base, owner, issuer)

	step := NewBookStep(
		Issue{Currency: "USD", Issuer: issuer},
		Issue{Currency: "XRP"},
		source,
		destination,
		nil,
		false,
	)
	bookDir := step.bookBaseKey()
	binary.BigEndian.PutUint64(bookDir[24:], 0x5500000000000000)
	offerKey := insertBookOffer(t, base, owner, state.EncodeAccountIDSafe(issuer), 1, 0, bookDir)
	directory := &state.DirectoryNode{
		RootIndex:         bookDir,
		Indexes:           [][32]byte{offerKey},
		TakerPaysCurrency: keylet.CurrencyBytes("USD"),
		TakerPaysIssuer:   issuer,
	}
	directoryData, err := state.SerializeDirectoryNode(directory, true)
	require.NoError(t, err)
	base.data[bookDir] = directoryData

	view := &offerAuthorizationFaultView{paymentMockLedgerView: base}
	return &offerAuthorizationTraversalFixture{
		base:          base,
		view:          view,
		afView:        NewPaymentSandbox(view),
		step:          step,
		offerKey:      offerKey,
		originalOffer: append([]byte(nil), base.data[offerKey]...),
		owner:         owner,
		issuer:        issuer,
	}
}

func TestBookStepAuthorizationLookupErrorsAbortTraversal(t *testing.T) {
	readSentinel := errors.New("authorization lookup failed")
	tests := []struct {
		name      string
		configure func(*testing.T, *offerAuthorizationTraversalFixture)
	}{
		{
			name: "issuer read error",
			configure: func(_ *testing.T, f *offerAuthorizationTraversalFixture) {
				f.view.key = keylet.Account(f.issuer).Key
				f.view.failAt = 1
				f.view.err = readSentinel
			},
		},
		{
			name: "issuer parse error",
			configure: func(_ *testing.T, f *offerAuthorizationTraversalFixture) {
				key := keylet.Account(f.issuer).Key
				f.base.data[key] = f.base.data[key][:3]
			},
		},
		{
			name: "trust line read error",
			configure: func(_ *testing.T, f *offerAuthorizationTraversalFixture) {
				f.view.key = keylet.Line(f.owner, f.issuer, "USD").Key
				f.view.failAt = 2
				f.view.err = readSentinel
			},
		},
		{
			name: "trust line parse error",
			configure: func(_ *testing.T, f *offerAuthorizationTraversalFixture) {
				key := keylet.Line(f.owner, f.issuer, "USD").Key
				f.view.key = key
				f.view.failAt = 2
				f.view.faultData = f.base.data[key][:3]
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newOfferAuthorizationTraversalFixture(t)
			tc.configure(t, fixture)
			sb := NewChildSandbox(fixture.afView)
			removals := make(map[[32]byte]bool)
			requestedOut := NewXRPEitherAmount(1)

			flowErr := recoverFlowError(t, func() {
				fixture.step.Rev(sb, fixture.afView, removals, requestedOut)
			})
			require.Equal(t, ter.TefINTERNAL, flowErr.ter)
			require.NotContains(t, removals, fixture.offerKey)
			require.NotContains(t, fixture.step.PermRemovals(), fixture.offerKey)
			require.Empty(t, sb.Modifications())
			require.Empty(t, sb.Insertions())
			require.Empty(t, sb.Deletions())
			require.Equal(t, fixture.originalOffer, fixture.base.data[fixture.offerKey])

			fixture.view.reads = 0
			result := ExecuteStrand(
				fixture.afView,
				Strand{fixture.step},
				nil,
				requestedOut,
			)
			require.False(t, result.Success)
			require.Equal(t, ter.TesSUCCESS, result.FatalResult)
			require.Nil(t, result.Sandbox)
			require.True(t, result.In.IsZero())
			require.True(t, result.Out.IsZero())
			require.NotContains(t, result.OffsToRm, fixture.offerKey)
			require.Equal(t, fixture.originalOffer, fixture.base.data[fixture.offerKey])
		})
	}
}

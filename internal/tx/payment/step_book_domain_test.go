package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func setPresentZeroDomain(t *testing.T, f *expiredOfferFixture) {
	t.Helper()
	var offer ledgerfields.Offer
	require.NoError(t, offer.Decode(f.base.data[f.offerKey]))
	offer.SetDomainIDValue([32]byte{})
	raw, err := offer.Encode()
	require.NoError(t, err)
	f.base.data[f.offerKey] = raw
}

func addDomainOwner(t *testing.T, f *expiredOfferFixture, domain [32]byte) {
	t.Helper()
	raw, err := state.SerializePermissionedDomain(
		&state.PermissionedDomainData{Owner: f.owner, Sequence: 1},
		state.EncodeAccountIDSafe(f.owner),
	)
	require.NoError(t, err)
	f.base.data[keylet.PermissionedDomainByID(domain).Key] = raw
}

func setHybridAdditionalBooks(t *testing.T, f *expiredOfferFixture, present bool, count int) {
	t.Helper()
	var offer ledgerfields.Offer
	require.NoError(t, offer.Decode(f.base.data[f.offerKey]))
	flags, err := offer.GetFlags()
	require.NoError(t, err)
	offer.SetFlagsValue(flags | ledgerfields.LsfHybrid)
	if present {
		books := make([]ledgerfields.BookValue, count)
		for i := range books {
			books[i].SetBookDirectoryValue([32]byte{byte(i + 7)})
			books[i].SetBookNodeValue(uint64(i + 1))
		}
		require.NoError(t, offer.SetAdditionalBooksValue(books))
	} else {
		offer.ClearAdditionalBooks()
	}
	raw, err := offer.Encode()
	require.NoError(t, err)
	f.base.data[f.offerKey] = raw
}

func TestBookStepOfferDomainIteration(t *testing.T) {
	tests := []struct {
		name                   string
		disable340, disable313 bool
		beyondLimit            bool
		presentZero, hybrid    bool
		hybridPresent          bool
		hybridCount            int
		yielded, wantPanic     bool
		mutate                 func(*testing.T, *expiredOfferFixture, [32]byte)
	}{
		{name: "expired before missing domain", mutate: func(_ *testing.T, f *expiredOfferFixture, _ [32]byte) {
			f.offer.Expiration, f.offer.DomainID = f.step.parentCloseTime, [32]byte{6}
		}},
		{name: "empty before mismatched domain", mutate: func(_ *testing.T, f *expiredOfferFixture, _ [32]byte) {
			f.offer.TakerGets, f.offer.DomainID = tx.NewXRPAmount(0), [32]byte{6}
		}},
		{name: "deep frozen before mismatched domain", mutate: func(t *testing.T, f *expiredOfferFixture, _ [32]byte) {
			f.offer.DomainID = [32]byte{6}
			f.base.createTrustLine(f.owner, [20]byte{2}, "USD", 0, 100, 100)
			lineKey := keylet.Line(f.owner, [20]byte{2}, "USD")
			line, err := state.ParseRippleState(f.base.data[lineKey.Key])
			require.NoError(t, err)
			line.Flags = state.LsfLowDeepFreeze | state.LsfHighDeepFreeze
			f.base.data[lineKey.Key], err = state.SerializeRippleState(line)
			require.NoError(t, err)
		}},
		{name: "missing domain", wantPanic: true},
		{name: "mismatched domain", wantPanic: true, mutate: func(_ *testing.T, f *expiredOfferFixture, _ [32]byte) {
			f.offer.DomainID = [32]byte{6}
		}},
		{name: "missing domain beyond quality limit", beyondLimit: true, wantPanic: true},
		{name: "mismatched domain beyond quality limit", beyondLimit: true, wantPanic: true, mutate: func(_ *testing.T, f *expiredOfferFixture, _ [32]byte) {
			f.offer.DomainID = [32]byte{6}
		}},
		{name: "present zero before cleanup340", disable340: true, presentZero: true},
		{name: "present zero with cleanup340", presentZero: true, wantPanic: true},
		{name: "foreign member before cleanup340", disable340: true, yielded: true, mutate: func(t *testing.T, f *expiredOfferFixture, _ [32]byte) {
			foreign := [32]byte{6}
			addDomainOwner(t, f, foreign)
			f.offer.DomainID = foreign
		}},
		{name: "indexed domain member", yielded: true, mutate: func(t *testing.T, f *expiredOfferFixture, domain [32]byte) {
			addDomainOwner(t, f, domain)
			f.offer.DomainID = domain
		}},
		{name: "hybrid multiple before cleanup313", disable313: true, hybrid: true, hybridPresent: true, hybridCount: 2, yielded: true, mutate: func(t *testing.T, f *expiredOfferFixture, domain [32]byte) {
			addDomainOwner(t, f, domain)
			f.offer.DomainID = domain
		}},
		{name: "hybrid multiple with cleanup313", hybrid: true, hybridPresent: true, hybridCount: 2, mutate: func(t *testing.T, f *expiredOfferFixture, domain [32]byte) {
			addDomainOwner(t, f, domain)
			f.offer.DomainID = domain
		}},
		{name: "hybrid additional books absent", hybrid: true, mutate: func(t *testing.T, f *expiredOfferFixture, domain [32]byte) {
			addDomainOwner(t, f, domain)
			f.offer.DomainID = domain
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newExpiredOfferFixture(t)
			domain := prepareDomainOffer(t, f, tc.disable340, tc.disable313)
			if tc.beyondLimit {
				f.step.qualityLimit = &Quality{Value: 1}
			}
			if tc.mutate != nil {
				tc.mutate(t, f, domain)
			}
			saveDomainOffer(t, f)
			if tc.presentZero {
				setPresentZeroDomain(t, f)
			}
			if tc.hybrid {
				setHybridAdditionalBooks(t, f, tc.hybridPresent, tc.hybridCount)
				offer, err := state.ParseLedgerOffer(f.base.data[f.offerKey])
				require.NoError(t, err)
				require.Equal(t, !tc.yielded, f.step.offerOutOfDomain(NewPaymentSandbox(f.view), offer))
				return
			}
			if tc.wantPanic {
				require.PanicsWithValue(t, flowError{ter: ter.TecINTERNAL}, func() { _, _ = f.walk() })
				requireExpiredOfferState(t, f, true, 1)
				return
			}
			removals, err := f.walk()
			require.NoError(t, err)
			if tc.yielded {
				require.Empty(t, removals)
				requireExpiredOfferState(t, f, true, 1)
				return
			}
			require.True(t, removals[f.offerKey])
			requireExpiredOfferState(t, f, false, 0)
		})
	}
}

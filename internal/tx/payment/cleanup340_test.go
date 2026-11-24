package payment

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"

	"github.com/stretchr/testify/require"
)

func prepareDomainOffer(t *testing.T, f *expiredOfferFixture, disableCleanup340, disableCleanup313 bool) [32]byte {
	t.Helper()
	rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if disableCleanup340 {
		rules.Disable(amendment.FeatureFixCleanup3_4_0)
	}
	if disableCleanup313 {
		rules.Disable(amendment.FeatureFixCleanup3_1_3)
	}
	f.base.rules = rules.Build()
	domain := [32]byte{3}
	f.step.domainID = &domain
	book := f.step.bookBaseKey()
	binary.BigEndian.PutUint64(book[24:], 0x5500000000000000)
	dir, err := state.ParseDirectoryNode(f.base.data[f.bookDir])
	require.NoError(t, err)
	dir.RootIndex = book
	dir.DomainID = domain
	raw, err := state.SerializeDirectoryNode(dir, true)
	require.NoError(t, err)
	delete(f.base.data, f.bookDir)
	f.base.data[book] = raw
	f.bookDir = book
	f.offer.BookDirectory = book
	f.offer.Expiration = 0
	saveDomainOffer(t, f)
	return domain
}

func saveDomainOffer(t *testing.T, f *expiredOfferFixture) {
	t.Helper()
	raw, err := state.SerializeLedgerOffer(f.offer)
	require.NoError(t, err)
	f.base.data[f.offerKey] = raw
}

func TestBookCleanup340CorruptDomainMembership(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/missing=%t", enabled, missing), func(t *testing.T) {
				f := newExpiredOfferFixture(t)
				prepareDomainOffer(t, f, !enabled, false)
				f.offer.Expiration = 0
				if !missing {
					f.offer.DomainID = [32]byte{4}
				}
				saveDomainOffer(t, f)
				if enabled {
					require.PanicsWithValue(t, flowError{ter: ter.TecINTERNAL}, func() { _, _ = f.walk() })
				} else {
					require.NotPanics(t, func() { _, _ = f.walk() })
				}
				preserved, err := f.sandbox.Read(keylet.Offer(f.owner, 1))
				require.NoError(t, err)
				require.Equal(t, enabled || missing, preserved != nil)
			})
		}
	}
}

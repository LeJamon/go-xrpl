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

func TestBookCleanup340CorruptDomainMembership(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/missing=%t", enabled, missing), func(t *testing.T) {
				f := newExpiredOfferFixture(t)
				rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
				if enabled {
					rules.Enable(amendment.FeatureFixCleanup3_4_0)
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
				f.offer.Expiration = 0
				f.offer.BookDirectory = book
				if !missing {
					f.offer.DomainID = [32]byte{4}
				}
				raw, err = state.SerializeLedgerOffer(f.offer)
				require.NoError(t, err)
				f.base.data[f.offerKey] = raw
				if enabled {
					require.PanicsWithValue(t, flowError{ter: ter.TecINTERNAL}, func() { _, _ = f.walk() })
				} else {
					require.NotPanics(t, func() { _, _ = f.walk() })
				}
				preserved, err := f.sandbox.Read(keylet.Offer(f.owner, 1))
				require.NoError(t, err)
				require.NotNil(t, preserved)
			})
		}
	}
}

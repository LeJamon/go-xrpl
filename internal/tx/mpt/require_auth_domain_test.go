package mpt

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Clearing lsfMPTRequireAuth via MutableFlags is rejected when the issuance
// already carries a DomainID, because a DomainID requires RequireAuth to stay
// active. Reachable only under DynamicMPT (MutableFlags present).
func TestMPTokenIssuanceSet_ClearRequireAuthWithDomain(t *testing.T) {
	clearRA := TmfMPTClearRequireAuth
	domain := "00000000000000000000000000000000000000000000000000000000ABCDABCD"

	newIssuance := func(withDomain bool) *state.MPTokenIssuanceData {
		iss := &state.MPTokenIssuanceData{
			Flags:        entry.LsfMPTRequireAuth,
			MutableFlags: entry.LsmfMPTCanMutateRequireAuth,
		}
		if withDomain {
			d := domain
			iss.DomainID = &d
		}
		return iss
	}

	t.Run("domain set → tecNO_PERMISSION", func(t *testing.T) {
		m := &MPTokenIssuanceSet{MutableFlags: &clearRA}
		if got := m.checkMutablePermissions(newIssuance(true)); got != ter.TecNO_PERMISSION {
			t.Fatalf("got %v, want TecNO_PERMISSION", got)
		}
	})

	t.Run("no domain → permitted", func(t *testing.T) {
		m := &MPTokenIssuanceSet{MutableFlags: &clearRA}
		if got := m.checkMutablePermissions(newIssuance(false)); got != ter.TesSUCCESS {
			t.Fatalf("got %v, want TesSUCCESS", got)
		}
	})

	t.Run("setting RequireAuth with domain is fine", func(t *testing.T) {
		setRA := TmfMPTSetRequireAuth
		m := &MPTokenIssuanceSet{MutableFlags: &setRA}
		if got := m.checkMutablePermissions(newIssuance(true)); got != ter.TesSUCCESS {
			t.Fatalf("got %v, want TesSUCCESS", got)
		}
	})
}

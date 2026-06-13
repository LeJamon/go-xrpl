package escrow

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// mapView is a minimal map-backed tx.LedgerView for unit-testing the escrow
// owner-count write-back path in isolation.
type mapView struct {
	data map[[32]byte][]byte
}

func newMapView() *mapView { return &mapView{data: make(map[[32]byte][]byte)} }

func (m *mapView) Read(k keylet.Keylet) ([]byte, error)      { return m.data[k.Key], nil }
func (m *mapView) Exists(k keylet.Keylet) (bool, error)      { _, ok := m.data[k.Key]; return ok, nil }
func (m *mapView) Insert(k keylet.Keylet, data []byte) error { m.data[k.Key] = data; return nil }
func (m *mapView) Update(k keylet.Keylet, data []byte) error { m.data[k.Key] = data; return nil }
func (m *mapView) Erase(k keylet.Keylet) error               { delete(m.data, k.Key); return nil }
func (m *mapView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (m *mapView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}
func (m *mapView) Succ([32]byte) ([32]byte, []byte, bool, error) { return [32]byte{}, nil, false, nil }
func (m *mapView) TxExists([32]byte) bool                        { return false }
func (m *mapView) Rules() *amendment.Rules                       { return nil }
func (m *mapView) LedgerSeq() uint32                             { return 0 }

// TestAdjustOwnerCountViaView_PreservesPresentZeroAccountTxnID is a regression
// for the #741 account_hash fork. A token-escrow finish that creates the
// destination's trust line / MPToken bumps the destination's OwnerCount via
// adjustOwnerCountViaView, which re-serializes its AccountRoot. The previous
// hand-rolled serializer emitted sfAccountTxnID only when non-zero, so a
// present-but-zero AccountTxnID (asfAccountTxnID enabled, account not used
// since — rippled makeFieldPresent semantics) was silently dropped, forking
// account_hash from rippled, which keeps every present field. The fix delegates
// to the canonical tx.AdjustOwnerCount / state.SerializeAccountRoot, which key
// on presence.
func TestAdjustOwnerCountViaView_PreservesPresentZeroAccountTxnID(t *testing.T) {
	view := newMapView()
	var dest [20]byte
	dest[0], dest[19] = 0xab, 0xcd
	addr, err := addresscodec.EncodeAccountIDToClassicAddress(dest[:])
	require.NoError(t, err)
	key := keylet.Account(dest)

	acct := &state.AccountRoot{
		Account:         addr,
		Balance:         100_000_000,
		Sequence:        7,
		OwnerCount:      2,
		HasAccountTxnID: true,       // present...
		AccountTxnID:    [32]byte{}, // ...but still zero (rippled makeFieldPresent)
	}
	blob, err := state.SerializeAccountRoot(acct)
	require.NoError(t, err)
	require.NoError(t, view.Insert(key, blob))

	// The token-escrow finish trust-line / MPToken create path.
	adjustOwnerCountViaView(view, dest, 1)

	out, err := view.Read(key)
	require.NoError(t, err)
	got, err := state.ParseAccountRoot(out)
	require.NoError(t, err)

	require.Equal(t, uint32(3), got.OwnerCount, "owner count must increment")
	require.True(t, got.HasAccountTxnID,
		"present-zero sfAccountTxnID must survive the owner-count write-back (#741 account_hash fork)")
	require.Equal(t, [32]byte{}, got.AccountTxnID, "the preserved value stays zero")
}

// seedAccountForEscrow inserts a minimal AccountRoot into the view and returns
// its classic address.
func seedAccountForEscrow(t *testing.T, v *mapView, id [20]byte, ownerCount uint32) string {
	t.Helper()
	addr, err := addresscodec.EncodeAccountIDToClassicAddress(id[:])
	require.NoError(t, err)
	blob, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:    addr,
		Balance:    1_000_000_000,
		Sequence:   1,
		OwnerCount: ownerCount,
	})
	require.NoError(t, err)
	require.NoError(t, v.Insert(keylet.Account(id), blob))
	return addr
}

func ownerCountOf(t *testing.T, v *mapView, id [20]byte) uint32 {
	t.Helper()
	out, err := v.Read(keylet.Account(id))
	require.NoError(t, err)
	got, err := state.ParseAccountRoot(out)
	require.NoError(t, err)
	return got.OwnerCount
}

// TestEscrowTokenCreate_OwnerCountBumpGate is a regression for the EscrowCancel
// account_hash fork. When a token escrow is finished, the destination account
// gains the re-created trust line / MPToken and rippled bumps its OwnerCount
// (escrowUnlockApplyHelper receives the destination's AccountRoot as sleDest).
// When a token escrow is cancelled, rippled instead passes the soon-erased
// escrow SLE as sleDest, so the bump lands on a throwaway object and the
// creator's OwnerCount is NOT charged for the re-created line. go-xrpl models
// this with bumpOwnerCount: finish=true, cancel=false. Bumping the creator on
// cancel forks account_hash from rippled.
func TestEscrowTokenCreate_OwnerCountBumpGate(t *testing.T) {
	var holder [20]byte
	holder[0], holder[19] = 0x12, 0x34
	var issuer [20]byte
	issuer[0], issuer[19] = 0x56, 0x78

	const mptHexID = "0000000100000000000000000000000000000000000000AB" // 24 bytes

	t.Run("MPToken finish bumps the destination OwnerCount", func(t *testing.T) {
		v := newMapView()
		seedAccountForEscrow(t, v, holder, 5)
		issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
		require.NoError(t, err)

		require.Equal(t, tx.TesSUCCESS,
			createMPTokenForEscrow(v, issuanceKey, mptHexID, holder, holder, true))

		require.Equal(t, uint32(6), ownerCountOf(t, v, holder),
			"finish bumps the destination's OwnerCount for the new MPToken")
		exists, _ := v.Exists(keylet.MPToken(issuanceKey.Key, holder))
		require.True(t, exists, "the MPToken is created")
	})

	t.Run("MPToken cancel does not bump the creator OwnerCount", func(t *testing.T) {
		v := newMapView()
		seedAccountForEscrow(t, v, holder, 5)
		issuanceKey, err := mptIssuanceKeyFromHex(mptHexID)
		require.NoError(t, err)

		require.Equal(t, tx.TesSUCCESS,
			createMPTokenForEscrow(v, issuanceKey, mptHexID, holder, holder, false))

		require.Equal(t, uint32(5), ownerCountOf(t, v, holder),
			"cancel must not charge the creator's OwnerCount (rippled bumps the erased escrow SLE)")
		exists, _ := v.Exists(keylet.MPToken(issuanceKey.Key, holder))
		require.True(t, exists, "the MPToken is still created on cancel")
	})

	t.Run("trust line finish bumps vs cancel does not", func(t *testing.T) {
		recvLow := state.CompareAccountIDsForLine(holder, issuer) < 0

		// Finish: bump the receiver.
		vf := newMapView()
		seedAccountForEscrow(t, vf, holder, 5)
		seedAccountForEscrow(t, vf, issuer, 0)
		require.Equal(t, tx.TesSUCCESS,
			createTrustLineForEscrow(vf, issuer, holder, "USD", holder, recvLow, true))
		require.Equal(t, uint32(6), ownerCountOf(t, vf, holder),
			"finish bumps the destination's OwnerCount for the new trust line")

		// Cancel: do not bump the creator.
		vc := newMapView()
		seedAccountForEscrow(t, vc, holder, 5)
		seedAccountForEscrow(t, vc, issuer, 0)
		require.Equal(t, tx.TesSUCCESS,
			createTrustLineForEscrow(vc, issuer, holder, "USD", holder, recvLow, false))
		require.Equal(t, uint32(5), ownerCountOf(t, vc, holder),
			"cancel must not charge the creator's OwnerCount for the re-created trust line")
	})
}

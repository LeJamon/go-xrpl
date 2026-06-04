package escrow

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
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

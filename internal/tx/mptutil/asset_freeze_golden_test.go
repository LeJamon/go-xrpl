package mptutil

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type fullDiffView struct{ *mptTestView }

func (fullDiffView) ForEach(func([32]byte, []byte) bool) error { return nil }
func (fullDiffView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (fullDiffView) TxExists([32]byte) (bool, error) { return false, nil }
func (fullDiffView) LedgerSeq() uint32               { return 0 }

// TestAssetFreezeGolden freezes rippled's Asset-level checkFrozen,
// checkIndividualFrozen and checkDeepFrozen for IOU and MPT holdings,
// including a vault share whose underlying asset is locked. An amendment that
// changes freeze or lock semantics must update the golden deliberately
// (go test -run TestAssetFreezeGolden -update).
func TestAssetFreezeGolden(t *testing.T) {
	var b strings.Builder
	line := func(view *mptTestView, name string, account [20]byte, asset tx.Asset) {
		got := CheckFrozen(view, account, asset)
		unreachable := strings.Contains(name, "vault-share") ||
			(strings.Contains(name, "account=50") && !strings.Contains(name, "holding=missing"))
		if legacy := tx.AssetFrozen(fullDiffView{view}, account, asset); legacy != got && !unreachable {
			t.Errorf("%s: tx.AssetFrozen=%s CheckFrozen=%s", name, legacy, got)
		}
		fmt.Fprintf(&b, "%s frozen=%s individual=%s deep=%s\n", name, got,
			CheckIndividualFrozen(view, account, asset), CheckDeepFrozen(view, account, asset))
	}

	for _, sc := range iouScenarios(t) {
		view := newMPTTestView()
		view.data = sc.data
		line(view, "iou "+sc.name, sc.holder, tx.Asset{Currency: sc.currency, Issuer: state.EncodeAccountIDSafe(sc.issuer)})
	}

	var issuer, holder [20]byte
	issuer[0], holder[0] = 0x50, 0x10
	id := testMPTID(1, issuer)
	optional := []struct {
		name    string
		present bool
		flags   uint32
	}{{"missing", false, 0}, {"open", true, 0}, {"locked", true, entry.LsfMPTLocked}}
	for _, rs := range []struct {
		name  string
		rules *amendment.Rules
	}{{"all", amendment.AllSupportedRules()}, {"genesis", amendment.GenesisRules()}} {
		for _, issuance := range optional {
			for _, holding := range optional {
				for _, account := range [][20]byte{holder, issuer} {
					view := newMPTTestView()
					view.rules = rs.rules
					if issuance.present {
						putTestIssuance(t, view, id, issuance.flags, nil)
					}
					if holding.present {
						putTestHolding(t, view, id, account, holding.flags)
					}
					line(view, fmt.Sprintf("mpt %s issuance=%s holding=%s account=%02x", rs.name, issuance.name, holding.name, account[0]),
						account, tx.Asset{MPTIssuanceID: EncodeID(id)})
				}
			}
		}

		for _, underlying := range optional[1:] {
			view := newMPTTestView()
			view.rules = rs.rules
			var owner, pseudo [20]byte
			owner[0], pseudo[0] = 0x30, 0x70
			vaultID := [32]byte{9}
			assetID := testMPTID(2, issuer)
			shareID := testMPTID(1, pseudo)
			putTestAccount(t, view, pseudo, 0, vaultID)
			putTestVault(t, view, vaultID, owner, pseudo, shareID, assetID)
			putTestIssuance(t, view, assetID, underlying.flags, nil)
			putTestIssuance(t, view, shareID, 0, nil)
			putTestHolding(t, view, shareID, holder, 0)
			line(view, fmt.Sprintf("mpt %s vault-share underlying=%s", rs.name, underlying.name),
				holder, tx.Asset{MPTIssuanceID: EncodeID(shareID)})
		}
	}
	checkGolden(t, "asset_freeze.golden", b.String())
}

func testMPTID(seq byte, issuer [20]byte) [24]byte {
	var id [24]byte
	id[3] = seq
	copy(id[4:], issuer[:])
	return id
}

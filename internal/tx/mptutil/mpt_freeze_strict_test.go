package mptutil

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestCheckMPTFrozenIncludesVaultBacking(t *testing.T) {
	view := newMPTTestView()
	view.rules = amendment.AllSupportedRules()

	var holder, owner, pseudo, underlyingIssuer [20]byte
	holder[0], owner[0], pseudo[0], underlyingIssuer[0] = 0x10, 0x30, 0x70, 0x50
	vaultID := [32]byte{9}
	shareID := testMPTID(1, pseudo)
	underlyingID := testMPTID(2, underlyingIssuer)

	putTestAccount(t, view, pseudo, 0, vaultID)
	putTestVault(t, view, vaultID, owner, pseudo, shareID, underlyingID)
	putTestIssuance(t, view, underlyingID, entry.LsfMPTLocked, nil)
	putTestIssuance(t, view, shareID, entry.LsfMPTCanEscrow|entry.LsfMPTCanTransfer, nil)
	putTestHolding(t, view, shareID, holder, 0)

	issuance, _, result := ReadIssuance(view, shareID)
	if result != ter.TesSUCCESS {
		t.Fatalf("ReadIssuance() = %s, want %s", result, ter.TesSUCCESS)
	}
	if got := CheckMPTFrozen(view, shareID, issuance, holder); got != ter.TecLOCKED {
		t.Fatalf("CheckMPTFrozen() = %s, want %s", got, ter.TecLOCKED)
	}

	view.readErrors[keylet.VaultByID(vaultID).Key] = errors.New("vault read failed")
	if got := CheckMPTFrozen(view, shareID, issuance, holder); got != ter.TesSUCCESS {
		t.Fatalf("CheckMPTFrozen() with backing read failure = %s, want %s", got, ter.TesSUCCESS)
	}
}

func TestCheckMPTFrozenPreservesHoldingReadFailure(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder [20]byte
	issuer[0], holder[0] = 0x50, 0x10
	id := testMPTID(1, issuer)
	putTestIssuance(t, view, id, 0, nil)
	view.readErrors[keylet.MPTokenByID(id, holder).Key] = errors.New("holding read failed")

	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		t.Fatalf("ReadIssuance() = %s, want %s", result, ter.TesSUCCESS)
	}
	if got := CheckMPTFrozen(view, id, issuance, holder); got != ter.TefINTERNAL {
		t.Fatalf("CheckMPTFrozen() = %s, want %s", got, ter.TefINTERNAL)
	}
}

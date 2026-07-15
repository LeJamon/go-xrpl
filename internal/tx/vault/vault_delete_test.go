package vault

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type vaultDeleteFixture struct {
	view       *mptArmsView
	ctx        *tx.ApplyContext
	txn        *VaultDelete
	vaultID    [32]byte
	vaultKey   keylet.Keylet
	ownerID    [20]byte
	pseudoID   [20]byte
	assetMPTID [24]byte
	shareMPTID [24]byte
}

func newVaultDeleteFixture(t *testing.T) *vaultDeleteFixture {
	t.Helper()

	ownerID := deleteTestID(0x11)
	pseudoID := deleteTestID(0x22)
	assetIssuerID := deleteTestID(0x33)
	assetMPTID := keylet.MakeMPTID(7, assetIssuerID)
	shareMPTID := keylet.MakeMPTID(8, pseudoID)
	var vaultID [32]byte
	for i := range vaultID {
		vaultID[i] = 0x44
	}
	vaultKey := keylet.VaultByID(vaultID)
	view := newMPTArmsView()

	vaultDir, err := state.DirInsert(view, keylet.OwnerDir(ownerID), vaultKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ownerID
	})
	if err != nil {
		t.Fatalf("insert vault directory entry: %v", err)
	}
	assetTokenKey := keylet.MPTokenByID(assetMPTID, pseudoID)
	assetDir, err := state.DirInsert(view, keylet.OwnerDir(pseudoID), assetTokenKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = pseudoID
	})
	if err != nil {
		t.Fatalf("insert asset holding directory entry: %v", err)
	}
	shareKey := keylet.MPTIssuance(shareMPTID)
	shareDir, err := state.DirInsert(view, keylet.OwnerDir(pseudoID), shareKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = pseudoID
	})
	if err != nil {
		t.Fatalf("insert share issuance directory entry: %v", err)
	}

	ownerAddr := deleteTestAddress(t, ownerID)
	pseudoAddr := deleteTestAddress(t, pseudoID)
	owner := &state.AccountRoot{Account: ownerAddr, Balance: 1_000_000_000, Sequence: 1, OwnerCount: 2}
	pseudo := &state.AccountRoot{
		Account: pseudoAddr, Sequence: 0, OwnerCount: 2, VaultID: vaultID,
	}
	deleteTestPutAccount(t, view, ownerID, owner)
	deleteTestPutAccount(t, view, pseudoID, pseudo)

	assetToken := &state.MPTokenData{
		Account: pseudoID, MPTokenIssuanceID: assetMPTID, OwnerNode: assetDir.Page,
	}
	assetTokenData, err := state.SerializeMPToken(assetToken)
	if err != nil {
		t.Fatalf("serialize asset MPToken: %v", err)
	}
	view.data[assetTokenKey.Key] = assetTokenData

	shareIssuance := &state.MPTokenIssuanceData{
		Issuer: pseudoID, Sequence: 8, OwnerNode: shareDir.Page,
	}
	shareData, err := state.SerializeMPTokenIssuance(shareIssuance)
	if err != nil {
		t.Fatalf("serialize share issuance: %v", err)
	}
	view.data[shareKey.Key] = shareData

	vaultData, err := serializeVault(&vaultData{
		Owner:            ownerID,
		Account:          pseudoID,
		Sequence:         1,
		OwnerNode:        vaultDir.Page,
		ShareMPTID:       shareMPTID,
		AssetIsMPT:       true,
		AssetMPTID:       assetMPTID,
		WithdrawalPolicy: VaultStrategyFirstComeFirstServe,
	})
	if err != nil {
		t.Fatalf("serialize vault: %v", err)
	}
	view.data[vaultKey.Key] = vaultData

	ctx := &tx.ApplyContext{
		View:      view,
		Account:   owner,
		AccountID: ownerID,
		Config:    tx.EngineConfig{Rules: rulesWithFix(true)},
		Metadata:  &tx.Metadata{},
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}

	return &vaultDeleteFixture{
		view:       view,
		ctx:        ctx,
		txn:        NewVaultDelete(ownerAddr, hex.EncodeToString(vaultID[:])),
		vaultID:    vaultID,
		vaultKey:   vaultKey,
		ownerID:    ownerID,
		pseudoID:   pseudoID,
		assetMPTID: assetMPTID,
		shareMPTID: shareMPTID,
	}
}

func (f *vaultDeleteFixture) addOwnerShare(t *testing.T, locked uint64) {
	t.Helper()
	tokenKey := keylet.MPTokenByID(f.shareMPTID, f.ownerID)
	dir, err := state.DirInsert(f.view, keylet.OwnerDir(f.ownerID), tokenKey.Key, false, func(node *state.DirectoryNode) {
		node.Owner = f.ownerID
	})
	if err != nil {
		t.Fatalf("insert owner share directory entry: %v", err)
	}
	token := &state.MPTokenData{
		Account: f.ownerID, MPTokenIssuanceID: f.shareMPTID, OwnerNode: dir.Page,
	}
	if locked != 0 {
		token.LockedAmount = &locked
	}
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("serialize owner share token: %v", err)
	}
	f.view.data[tokenKey.Key] = data
	f.ctx.Account.OwnerCount++
	deleteTestPutAccount(t, f.view, f.ownerID, f.ctx.Account)
}

func (f *vaultDeleteFixture) setPseudo(t *testing.T, mutate func(*state.AccountRoot)) {
	t.Helper()
	pseudo, err := tx.ReadAccountRoot(f.view, f.pseudoID)
	if err != nil || pseudo == nil {
		t.Fatalf("read pseudo account: %v", err)
	}
	mutate(pseudo)
	deleteTestPutAccount(t, f.view, f.pseudoID, pseudo)
}

func TestVaultDeleteMPTBackedVault(t *testing.T) {
	f := newVaultDeleteFixture(t)
	if got := f.txn.Apply(f.ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply() = %v, want tesSUCCESS", got)
	}
	for name, k := range map[string]keylet.Keylet{
		"vault":                f.vaultKey,
		"asset holding":        keylet.MPTokenByID(f.assetMPTID, f.pseudoID),
		"share issuance":       keylet.MPTIssuance(f.shareMPTID),
		"pseudo account":       keylet.Account(f.pseudoID),
		"pseudo owner dir":     keylet.OwnerDir(f.pseudoID),
		"vault owner dir root": keylet.OwnerDir(f.ownerID),
	} {
		if _, ok := f.view.data[k.Key]; ok {
			t.Errorf("%s was not erased", name)
		}
	}
	if f.ctx.Account.OwnerCount != 0 {
		t.Fatalf("owner count = %d, want 0", f.ctx.Account.OwnerCount)
	}
}

func TestVaultDeletePropagatesLockedOwnerShareObligation(t *testing.T) {
	f := newVaultDeleteFixture(t)
	f.addOwnerShare(t, 5)
	if got := f.txn.Apply(f.ctx); got != ter.TecHAS_OBLIGATIONS {
		t.Fatalf("Apply() = %v, want tecHAS_OBLIGATIONS", got)
	}
	if _, ok := f.view.data[keylet.MPTokenByID(f.shareMPTID, f.ownerID).Key]; !ok {
		t.Fatal("locked owner share MPToken was erased")
	}
}

func TestVaultDeleteCorruptOwnerShareDirectoryClaimsFee(t *testing.T) {
	f := newVaultDeleteFixture(t)
	f.addOwnerShare(t, 0)
	tokenKey := keylet.MPTokenByID(f.shareMPTID, f.ownerID)
	token, err := readMPToken(f.view, tokenKey)
	if err != nil || token == nil {
		t.Fatalf("read owner share token: %v", err)
	}
	token.OwnerNode++
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatalf("serialize owner share token: %v", err)
	}
	f.view.data[tokenKey.Key] = data

	if got := f.txn.Apply(f.ctx); got != ter.TecINTERNAL {
		t.Fatalf("Apply() = %v, want tecINTERNAL", got)
	}
}

func TestVaultDeleteShareIssuerPrecedesOutstandingAmount(t *testing.T) {
	f := newVaultDeleteFixture(t)
	wrongIssuer := deleteTestID(0x77)
	data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: wrongIssuer, Sequence: 8, OutstandingAmount: 9,
	})
	if err != nil {
		t.Fatalf("serialize malformed share issuance: %v", err)
	}
	f.view.data[keylet.MPTIssuance(f.shareMPTID).Key] = data
	if got := f.txn.Preclaim(f.view, f.ctx.Config); got != ter.TecNO_PERMISSION {
		t.Fatalf("Preclaim() = %v, want tecNO_PERMISSION", got)
	}
}

func TestVaultDeletePseudoIntegrityPrecedence(t *testing.T) {
	t.Run("vault id before balance", func(t *testing.T) {
		f := newVaultDeleteFixture(t)
		f.setPseudo(t, func(pseudo *state.AccountRoot) {
			pseudo.VaultID[0] ^= 0xff
			pseudo.Balance = 1
		})
		if got := f.txn.Apply(f.ctx); got != ter.TefBAD_LEDGER {
			t.Fatalf("Apply() = %v, want tefBAD_LEDGER", got)
		}
	})

	t.Run("owner directory before vault id", func(t *testing.T) {
		f := newVaultDeleteFixture(t)
		var extra [32]byte
		extra[0] = 0x99
		if _, err := state.DirInsert(f.view, keylet.OwnerDir(f.pseudoID), extra, false, func(node *state.DirectoryNode) {
			node.Owner = f.pseudoID
		}); err != nil {
			t.Fatalf("insert extra pseudo obligation: %v", err)
		}
		f.setPseudo(t, func(pseudo *state.AccountRoot) {
			pseudo.OwnerCount++
			pseudo.VaultID[0] ^= 0xff
		})
		if got := f.txn.Apply(f.ctx); got != ter.TecHAS_OBLIGATIONS {
			t.Fatalf("Apply() = %v, want tecHAS_OBLIGATIONS", got)
		}
	})
}

func deleteTestID(b byte) [20]byte {
	var id [20]byte
	for i := range id {
		id[i] = b
	}
	return id
}

func deleteTestAddress(t *testing.T, id [20]byte) string {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	return address
}

func deleteTestPutAccount(t *testing.T, view *mptArmsView, id [20]byte, account *state.AccountRoot) {
	t.Helper()
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatalf("serialize account: %v", err)
	}
	view.data[keylet.Account(id).Key] = data
}

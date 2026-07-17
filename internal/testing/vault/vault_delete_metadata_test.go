package vault_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

const (
	vaultDeleteIOUMetadataSHA256 = "1BACA50A02B428E51CEE967477384BF6124C28214966C950A9CAF26224908E0C"
	vaultDeleteIOUStateRoot      = "ABA2266EB9DC0FF1ED790C5A81CB5C21DDA3317EAA1C2E47AE57FE6A27DF1089"
)

func TestVaultDeleteIOUMetadataTransitions(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	env.Fund(issuer, owner)

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultKey := keylet.Vault(owner.AccountID(), vaultSeq)
	info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	if err != nil || info == nil {
		t.Fatalf("read Vault: %v", err)
	}

	lineKey := keylet.Line(info.Account, issuer.ID, "USD")
	lineData, err := env.LedgerEntry(lineKey)
	if err != nil {
		t.Fatalf("read vault trust line: %v", err)
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		t.Fatalf("parse vault trust line: %v", err)
	}
	if !keylet.IsLowAccount(info.Account, issuer.ID) {
		t.Fatal("fixture must cover the low-account reserve transition")
	}
	reserveFlag := state.LsfLowReserve
	if line.Flags&reserveFlag == 0 {
		t.Fatalf("vault trust line flags = %#x, missing reserve flag %#x", line.Flags, reserveFlag)
	}

	result := env.Submit(vault.NewVaultDelete(owner.Address, strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))))
	jtx.RequireTxSuccess(t, result)
	assertVaultDeletedTransition(t, result.Metadata, lineKey, "RippleState", "Flags", line.Flags&^reserveFlag, line.Flags)

	blob, err := tx.SerializeMetadata(result.Metadata)
	if err != nil {
		t.Fatalf("serialize metadata: %v", err)
	}
	root, err := env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("state map hash: %v", err)
	}
	metadataHash := sha256.Sum256(blob)
	if got := strings.ToUpper(hex.EncodeToString(metadataHash[:])); got != vaultDeleteIOUMetadataSHA256 {
		t.Errorf("full metadata blob SHA-256 = %s, want %s", got, vaultDeleteIOUMetadataSHA256)
	}
	if got := strings.ToUpper(hex.EncodeToString(root[:])); got != vaultDeleteIOUStateRoot {
		t.Errorf("state root = %s, want %s", got, vaultDeleteIOUStateRoot)
	}
}

func TestVaultDeleteIOUIssuerOwnerCount(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: owner.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultKey := keylet.Vault(owner.AccountID(), vaultSeq)
	info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	if err != nil || info == nil {
		t.Fatalf("read Vault: %v", err)
	}
	pseudoAddress, err := state.EncodeAccountID(info.Account)
	if err != nil {
		t.Fatalf("encode vault pseudo-account: %v", err)
	}
	missing := tx.NewIssuedAmountFromFloat64(1, "EUR", pseudoAddress)
	jtx.RequireTxClaimed(t, env.Submit(trustset.NewTrustSet(owner.Address, missing)), jtx.TecNO_PERMISSION)
	limit := tx.NewIssuedAmountFromFloat64(1, "USD", pseudoAddress)
	jtx.RequireTxSuccess(t, env.Submit(trustset.NewTrustSet(owner.Address, limit)))
	if got := env.OwnerCount(owner); got != 4 {
		t.Fatalf("owner count before delete = %d, want 4", got)
	}

	result := env.Submit(vault.NewVaultDelete(owner.Address, strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))))
	jtx.RequireTxSuccess(t, result)
	assertVaultModifiedTransition(t, result.Metadata, keylet.Account(owner.AccountID()), "AccountRoot", "OwnerCount", uint32(0), uint32(4))
	if got := env.OwnerCount(owner); got != 0 {
		t.Errorf("owner count = %d, want 0", got)
	}
}

func assertVaultModifiedTransition(
	t *testing.T,
	meta *tx.Metadata,
	key keylet.Keylet,
	entryType, field string,
	wantFinal, wantPrevious uint32,
) {
	t.Helper()
	wantIndex := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for _, node := range meta.AffectedNodes {
		if node.NodeType != "ModifiedNode" || node.LedgerEntryType != entryType || node.LedgerIndex != wantIndex {
			continue
		}
		if got, ok := node.FinalFields[field].(uint32); !ok || got != wantFinal {
			t.Errorf("%s FinalFields.%s = %#v, want %d", entryType, field, node.FinalFields[field], wantFinal)
		}
		if got, ok := node.PreviousFields[field].(uint32); !ok || got != wantPrevious {
			t.Errorf("%s PreviousFields.%s = %#v, want %d", entryType, field, node.PreviousFields[field], wantPrevious)
		}
		return
	}
	t.Errorf("modified %s %s not found in metadata", entryType, wantIndex)
}

func assertVaultDeletedTransition(
	t *testing.T,
	meta *tx.Metadata,
	key keylet.Keylet,
	entryType, field string,
	wantFinal, wantPrevious uint32,
) {
	t.Helper()
	wantIndex := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for _, node := range meta.AffectedNodes {
		if node.NodeType != "DeletedNode" || node.LedgerEntryType != entryType || node.LedgerIndex != wantIndex {
			continue
		}
		if got, ok := node.FinalFields[field].(uint32); !ok || got != wantFinal {
			t.Errorf("%s FinalFields.%s = %#v, want %d", entryType, field, node.FinalFields[field], wantFinal)
		}
		if got, ok := node.PreviousFields[field].(uint32); !ok || got != wantPrevious {
			t.Errorf("%s PreviousFields.%s = %#v, want %d", entryType, field, node.PreviousFields[field], wantPrevious)
		}
		return
	}
	t.Errorf("deleted %s %s not found in metadata", entryType, wantIndex)
}

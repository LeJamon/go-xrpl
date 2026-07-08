// Package vault_test contains integration tests for Single Asset Vault
// (XLS-65) transaction behavior. Ported from rippled's Vault_test.cpp.
package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

const createFee = "50000000" // one reserve increment

// vaultID computes the ledger ID of the vault an account creates at seq.
func vaultID(acc *jtx.Account, seq uint32) string {
	k := keylet.Vault(acc.AccountID(), seq)
	return strings.ToUpper(hex.EncodeToString(k.Key[:]))
}

func newVaultEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.Close()
	return env
}

// TestVault_XRPLifecycle exercises create → deposit → withdraw → delete.
func TestVault_XRPLifecycle(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	depositor := jtx.NewAccount("depositor")
	env.Fund(owner, depositor)

	// Create an XRP vault.
	createSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate: got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, createSeq)

	// Deposit 100 XRP.
	dep := vault.NewVaultDeposit(depositor.Address, id, tx.NewXRPAmount(100_000_000))
	depBefore := env.Balance(depositor)
	if res := env.Submit(dep); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDeposit: got %s, want tesSUCCESS", res.Code)
	}
	if got := env.Balance(depositor); got >= depBefore-100_000_000 {
		t.Fatalf("depositor balance not debited: before=%d after=%d", depBefore, got)
	}

	// Withdraw the deposited assets back to the depositor.
	wd := vault.NewVaultWithdraw(depositor.Address, id, tx.NewXRPAmount(100_000_000))
	if res := env.Submit(wd); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultWithdraw: got %s, want tesSUCCESS", res.Code)
	}

	// Delete the now-empty vault.
	del := vault.NewVaultDelete(owner.Address, id)
	if res := env.Submit(del); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDelete: got %s, want tesSUCCESS", res.Code)
	}
	if env.VaultExists(id) {
		t.Fatalf("vault still exists after delete")
	}
}

// TestVault_CreateAmendmentDisabled asserts SingleAssetVault gating.
func TestVault_CreateAmendmentDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.Close()
	owner := jtx.NewAccount("owner")
	env.Fund(owner)

	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "temDISABLED" {
		t.Fatalf("VaultCreate (disabled): got %s, want temDISABLED", res.Code)
	}
}

// TestVault_CreateScaleForbiddenForXRP asserts Scale is IOU-only.
func TestVault_CreateScaleForbiddenForXRP(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)

	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	scale := uint8(6)
	create.Scale = &scale
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "temMALFORMED" {
		t.Fatalf("VaultCreate (XRP + Scale): got %s, want temMALFORMED", res.Code)
	}
}

// TestVault_DepositNoEntry asserts a deposit to a missing vault is rejected.
func TestVault_DepositNoEntry(t *testing.T) {
	env := newVaultEnv(t)
	depositor := jtx.NewAccount("depositor")
	env.Fund(depositor)

	missing := strings.Repeat("A", 64)
	dep := vault.NewVaultDeposit(depositor.Address, missing, tx.NewXRPAmount(1_000_000))
	if res := env.Submit(dep); res.Code != "tecNO_ENTRY" {
		t.Fatalf("VaultDeposit (missing vault): got %s, want tecNO_ENTRY", res.Code)
	}
}

// TestVault_SetWrongOwner asserts only the owner may modify a vault.
func TestVault_SetWrongOwner(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	other := jtx.NewAccount("other")
	env.Fund(owner, other)

	createSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate: got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, createSeq)

	set := vault.NewVaultSet(other.Address, id)
	data := "AABBCC"
	set.Data = data
	if res := env.Submit(set); res.Code != "tecNO_PERMISSION" {
		t.Fatalf("VaultSet (wrong owner): got %s, want tecNO_PERMISSION", res.Code)
	}
}

// TestVault_DeleteWrongOwner asserts only the owner may delete a vault.
func TestVault_DeleteWrongOwner(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	other := jtx.NewAccount("other")
	env.Fund(owner, other)

	createSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate: got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, createSeq)

	del := vault.NewVaultDelete(other.Address, id)
	if res := env.Submit(del); res.Code != "tecNO_PERMISSION" {
		t.Fatalf("VaultDelete (wrong owner): got %s, want tecNO_PERMISSION", res.Code)
	}
}

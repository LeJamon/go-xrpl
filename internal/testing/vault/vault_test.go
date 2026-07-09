// Package vault_test contains integration tests for Single Asset Vault
// (XLS-65) transaction behavior. Ported from rippled's Vault_test.cpp.
package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
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

// iouLineBalance returns the RippleState balance between a and b for currency,
// expressed in a's perspective, and whether the trust line exists.
func iouLineBalance(t *testing.T, env *jtx.TestEnv, a, b [20]byte, currency string) (float64, bool) {
	t.Helper()
	key := keylet.Line(a, b, currency)
	if !env.LedgerEntryExists(key) {
		return 0, false
	}
	data, err := env.LedgerEntry(key)
	if err != nil {
		t.Fatalf("read trust line: %v", err)
	}
	rs, err := state.ParseRippleState(data)
	if err != nil {
		t.Fatalf("parse trust line: %v", err)
	}
	bal := rs.Balance
	if !keylet.IsLowAccount(a, b) {
		bal = bal.Negate()
	}
	return bal.Float64(), true
}

func approxEq(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

// TestVault_IOULifecycle exercises create → deposit → withdraw → clawback →
// delete for an IOU vault, asserting that every asset movement ripples through
// the issuer: it settles on the holder↔issuer and issuer↔pseudo trust lines and
// never creates a direct holder↔pseudo line.
func TestVault_IOULifecycle(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	depositor := jtx.NewAccount("depositor")

	// The issuer must permit clawback before it owns any objects.
	env.Fund(issuer)
	if res := env.Submit(accountset.AccountSet(issuer).AllowClawback().Build()); res.Code != "tesSUCCESS" {
		t.Fatalf("AccountSet AllowClawback: got %s", res.Code)
	}
	env.Fund(owner, depositor)

	const cur = "USD"
	env.Trust(depositor, tx.NewIssuedAmountFromFloat64(10000, cur, issuer.Address))
	env.PayIOU(issuer, depositor, issuer, cur, 1000)

	issuerID := issuer.ID
	depID := depositor.ID

	// Create the IOU vault.
	createSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: cur, Issuer: issuer.Address})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate(IOU): got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, createSeq)

	rawID, _ := hex.DecodeString(id)
	var vkey [32]byte
	copy(vkey[:], rawID)
	vinfo, verr := vault.ReadVaultInfo(env.Ledger(), keylet.VaultByID(vkey))
	if verr != nil || vinfo == nil {
		t.Fatalf("ReadVaultInfo: %v", verr)
	}
	pseudoID := vinfo.Account

	// The pseudo-account holds the asset via an issuer trust line, created at
	// VaultCreate.
	if _, ok := iouLineBalance(t, env, pseudoID, issuerID, cur); !ok {
		t.Fatalf("pseudo<->issuer trust line missing after create")
	}
	depOwnerBefore := env.AccountInfo(depositor).OwnerCount

	// Deposit 100 USD.
	dep := vault.NewVaultDeposit(depositor.Address, id, tx.NewIssuedAmountFromFloat64(100, cur, issuer.Address))
	if res := env.Submit(dep); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDeposit: got %s, want tesSUCCESS", res.Code)
	}

	if env.LedgerEntryExists(keylet.Line(depID, pseudoID, cur)) {
		t.Fatalf("direct depositor<->pseudo trust line created on deposit (fork bug)")
	}
	if bal, ok := iouLineBalance(t, env, depID, issuerID, cur); !ok || !approxEq(bal, 900) {
		t.Fatalf("depositor<->issuer balance = %v (exists=%v), want 900", bal, ok)
	}
	if bal, ok := iouLineBalance(t, env, pseudoID, issuerID, cur); !ok || !approxEq(bal, 100) {
		t.Fatalf("pseudo<->issuer balance = %v (exists=%v), want 100", bal, ok)
	}
	if got := env.AccountInfo(depositor).OwnerCount; got != depOwnerBefore+1 {
		t.Fatalf("depositor owner count after deposit = %d, want %d", got, depOwnerBefore+1)
	}

	// Withdraw 40 USD back to the depositor.
	wd := vault.NewVaultWithdraw(depositor.Address, id, tx.NewIssuedAmountFromFloat64(40, cur, issuer.Address))
	if res := env.Submit(wd); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultWithdraw: got %s, want tesSUCCESS", res.Code)
	}
	if env.LedgerEntryExists(keylet.Line(depID, pseudoID, cur)) {
		t.Fatalf("direct depositor<->pseudo trust line created on withdraw (fork bug)")
	}
	if bal, ok := iouLineBalance(t, env, depID, issuerID, cur); !ok || !approxEq(bal, 940) {
		t.Fatalf("depositor<->issuer balance = %v, want 940", bal)
	}
	if bal, ok := iouLineBalance(t, env, pseudoID, issuerID, cur); !ok || !approxEq(bal, 60) {
		t.Fatalf("pseudo<->issuer balance = %v, want 60", bal)
	}

	// Issuer claws back the depositor's remaining shares: the 60 USD still held
	// by the vault is redeemed off the pseudo<->issuer line.
	claw := vault.NewVaultClawback(issuer.Address, id, depositor.Address)
	if res := env.Submit(claw); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultClawback: got %s, want tesSUCCESS", res.Code)
	}
	if bal, ok := iouLineBalance(t, env, pseudoID, issuerID, cur); !ok || !approxEq(bal, 0) {
		t.Fatalf("pseudo<->issuer balance after clawback = %v (exists=%v), want 0", bal, ok)
	}
	if bal, _ := iouLineBalance(t, env, depID, issuerID, cur); !approxEq(bal, 940) {
		t.Fatalf("depositor<->issuer balance after clawback = %v, want 940 (unchanged)", bal)
	}
	if got := env.AccountInfo(depositor).OwnerCount; got != depOwnerBefore {
		t.Fatalf("depositor owner count after clawback = %d, want %d", got, depOwnerBefore)
	}

	// Delete the now-empty vault; the pseudo's issuer trust line is removed.
	del := vault.NewVaultDelete(owner.Address, id)
	if res := env.Submit(del); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDelete: got %s, want tesSUCCESS", res.Code)
	}
	if env.VaultExists(id) {
		t.Fatalf("vault still exists after delete")
	}
	if env.LedgerEntryExists(keylet.Line(pseudoID, issuerID, cur)) {
		t.Fatalf("pseudo<->issuer trust line not removed on delete")
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

// readShareReference returns the share issuance's ReferenceHolding for the vault
// created by owner at createSeq (nil when unset).
func readShareReference(t *testing.T, env *jtx.TestEnv, id string) *string {
	t.Helper()
	rawID, _ := hex.DecodeString(id)
	var vkey [32]byte
	copy(vkey[:], rawID)
	vinfo, err := vault.ReadVaultInfo(env.Ledger(), keylet.VaultByID(vkey))
	if err != nil || vinfo == nil {
		t.Fatalf("ReadVaultInfo: %v", err)
	}
	data, lerr := env.LedgerEntry(keylet.MPTIssuance(vinfo.ShareMPTID))
	if lerr != nil {
		t.Fatalf("read share issuance: %v", lerr)
	}
	iss, perr := state.ParseMPTokenIssuance(data)
	if perr != nil {
		t.Fatalf("parse share issuance: %v", perr)
	}
	return iss.ReferenceHolding
}

// TestVaultCreate_ReferenceHolding covers the fixCleanup3_2_0 sfReferenceHolding
// site: an IOU/MPT vault's share issuance points to the pseudo-account's holding
// of the underlying; XRP vaults and the pre-amendment path leave it unset.
func TestVaultCreate_ReferenceHolding(t *testing.T) {
	t.Run("IOU sets reference to the pseudo trust line", func(t *testing.T) {
		env := newVaultEnv(t)
		issuer := jtx.NewAccount("issuer")
		owner := jtx.NewAccount("owner")
		env.Fund(issuer, owner)
		const cur = "USD"

		seq := env.Seq(owner)
		create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: cur, Issuer: issuer.Address})
		create.Common.Fee = createFee
		if res := env.Submit(create); res.Code != "tesSUCCESS" {
			t.Fatalf("VaultCreate(IOU): got %s", res.Code)
		}
		id := vaultID(owner, seq)

		rawID, _ := hex.DecodeString(id)
		var vkey [32]byte
		copy(vkey[:], rawID)
		vinfo, _ := vault.ReadVaultInfo(env.Ledger(), keylet.VaultByID(vkey))
		wantKey := keylet.Line(vinfo.Account, issuer.ID, cur).Key
		want := strings.ToUpper(hex.EncodeToString(wantKey[:]))

		got := readShareReference(t, env, id)
		if got == nil {
			t.Fatalf("ReferenceHolding unset for IOU vault")
		}
		if !strings.EqualFold(*got, want) {
			t.Fatalf("ReferenceHolding = %s, want %s (pseudo trust line)", *got, want)
		}
	})

	t.Run("XRP leaves reference unset", func(t *testing.T) {
		env := newVaultEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)

		seq := env.Seq(owner)
		create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
		create.Common.Fee = createFee
		if res := env.Submit(create); res.Code != "tesSUCCESS" {
			t.Fatalf("VaultCreate(XRP): got %s", res.Code)
		}
		if got := readShareReference(t, env, vaultID(owner, seq)); got != nil {
			t.Fatalf("ReferenceHolding set for XRP vault: %s", *got)
		}
	})

	t.Run("pre-amendment leaves reference unset", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("SingleAssetVault")
		env.DisableFeature("fixCleanup3_2_0")
		env.Close()
		issuer := jtx.NewAccount("issuer")
		owner := jtx.NewAccount("owner")
		env.Fund(issuer, owner)

		seq := env.Seq(owner)
		create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
		create.Common.Fee = createFee
		if res := env.Submit(create); res.Code != "tesSUCCESS" {
			t.Fatalf("VaultCreate(IOU, pre-amendment): got %s", res.Code)
		}
		if got := readShareReference(t, env, vaultID(owner, seq)); got != nil {
			t.Fatalf("ReferenceHolding set pre-amendment: %s", *got)
		}
	})
}

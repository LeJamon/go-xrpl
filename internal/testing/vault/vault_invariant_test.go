package vault_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

// TestVault_IOULifecycleInvariants exercises the ValidVault invariant across an
// IOU vault's create → deposit → withdraw → delete. IOU is the branch that needs
// scale-aware rounding of balance deltas (the trust-line balances carry an
// exponent), so a clean tesSUCCESS through the whole lifecycle proves the
// invariant does not false-positive on the rounded delta reconciliation.
func TestVault_IOULifecycleInvariants(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	depositor := jtx.NewAccount("depositor")
	env.Fund(issuer, owner, depositor)

	// The depositor trusts the issuer and receives 1000 USD.
	env.Trust(depositor, tx.NewIssuedAmountFromFloat64(10000, "USD", issuer.Address))
	env.PayIOU(issuer, depositor, issuer, "USD", 1000)

	// Owner creates a USD vault.
	createSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
	create.Common.Fee = createFee
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate IOU: got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, createSeq)

	// Deposit 100 USD, trading assets for freshly minted shares.
	dep := vault.NewVaultDeposit(depositor.Address, id, tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address))
	if res := env.Submit(dep); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDeposit IOU: got %s, want tesSUCCESS", res.Code)
	}

	// Withdraw the deposited assets back, redeeming shares.
	wd := vault.NewVaultWithdraw(depositor.Address, id, tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address))
	if res := env.Submit(wd); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultWithdraw IOU: got %s, want tesSUCCESS", res.Code)
	}

	// The now-empty vault can be deleted.
	del := vault.NewVaultDelete(owner.Address, id)
	if res := env.Submit(del); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDelete IOU: got %s, want tesSUCCESS", res.Code)
	}
	if env.VaultExists(id) {
		t.Fatalf("IOU vault still exists after delete")
	}
}

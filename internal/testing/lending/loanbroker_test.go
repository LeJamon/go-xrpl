// Package lending_test contains integration tests for the XLS-66 Lending
// Protocol transactors, ported from rippled's LoanBroker_test.cpp / Loan_test.cpp.
package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

const reserveIncrement = "50000000" // one owner reserve increment in drops

// newLendingEnv returns an env with the lending amendment stack enabled.
func newLendingEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	env.Close()
	return env
}

func vaultID(acc *jtx.Account, seq uint32) string {
	k := keylet.Vault(acc.AccountID(), seq)
	return strings.ToUpper(hex.EncodeToString(k.Key[:]))
}

func brokerID(acc *jtx.Account, seq uint32) string {
	k := keylet.LoanBroker(acc.AccountID(), seq)
	return strings.ToUpper(hex.EncodeToString(k.Key[:]))
}

// setupXRPVault creates an XRP vault owned by owner and deposits `deposit` drops
// from owner. Returns the vault ID.
func setupXRPVault(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, deposit uint64) string {
	t.Helper()
	seq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = reserveIncrement
	if res := env.Submit(create); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultCreate: got %s, want tesSUCCESS", res.Code)
	}
	id := vaultID(owner, seq)
	dep := vault.NewVaultDeposit(owner.Address, id, tx.NewXRPAmount(int64(deposit)))
	if res := env.Submit(dep); res.Code != "tesSUCCESS" {
		t.Fatalf("VaultDeposit: got %s, want tesSUCCESS", res.Code)
	}
	return id
}

// TestLoanBroker_Lifecycle exercises create → cover deposit → cover withdraw →
// delete for an XRP-vault loan broker.
func TestLoanBroker_Lifecycle(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000) // 10k XRP for reserves
	vid := setupXRPVault(t, env, owner, 2_000_000_000)

	// Create the loan broker.
	brokerSeq := env.Seq(owner)
	set := lending.NewLoanBrokerSet(owner.Address, vid)
	rate := uint16(1000) // 1% management fee
	set.ManagementFeeRate = &rate
	if res := env.Submit(set); res.Code != "tesSUCCESS" {
		t.Fatalf("LoanBrokerSet: got %s, want tesSUCCESS", res.Code)
	}
	bid := brokerID(owner, brokerSeq)
	bidBytes, _ := hex.DecodeString(bid)
	var bk [32]byte
	copy(bk[:], bidBytes)
	if !env.LedgerEntryExists(keylet.LoanBrokerByID(bk)) {
		t.Fatalf("LoanBroker entry not created")
	}

	// Deposit first-loss cover.
	dep := lending.NewLoanBrokerCoverDeposit(owner.Address, bid, tx.NewXRPAmount(500_000_000))
	if res := env.Submit(dep); res.Code != "tesSUCCESS" {
		t.Fatalf("CoverDeposit: got %s, want tesSUCCESS", res.Code)
	}

	// Withdraw part of the cover back to the owner. An XRP pseudo-account keeps
	// its base reserve, so only balance-minus-reserve is spendable via withdraw;
	// the remainder is returned on delete.
	wd := lending.NewLoanBrokerCoverWithdraw(owner.Address, bid, tx.NewXRPAmount(200_000_000))
	if res := env.Submit(wd); res.Code != "tesSUCCESS" {
		t.Fatalf("CoverWithdraw: got %s, want tesSUCCESS", res.Code)
	}

	// Delete the broker; the remaining cover returns to the owner.
	del := lending.NewLoanBrokerDelete(owner.Address, bid)
	if res := env.Submit(del); res.Code != "tesSUCCESS" {
		t.Fatalf("LoanBrokerDelete: got %s, want tesSUCCESS", res.Code)
	}
	if env.LedgerEntryExists(keylet.LoanBrokerByID(bk)) {
		t.Fatalf("LoanBroker entry still exists after delete")
	}
}

// TestLoanBroker_AmendmentDisabled asserts LendingProtocol gating.
func TestLoanBroker_AmendmentDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.DisableFeature("LendingProtocol")
	env.Close()
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)

	set := lending.NewLoanBrokerSet(owner.Address, strings.Repeat("A", 64))
	if res := env.Submit(set); res.Code != "temDISABLED" {
		t.Fatalf("LoanBrokerSet (disabled): got %s, want temDISABLED", res.Code)
	}
}

// TestLoanBroker_SetNoVault asserts a broker on a missing vault is rejected.
func TestLoanBroker_SetNoVault(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)

	set := lending.NewLoanBrokerSet(owner.Address, strings.Repeat("A", 64))
	if res := env.Submit(set); res.Code != "tecNO_ENTRY" {
		t.Fatalf("LoanBrokerSet (no vault): got %s, want tecNO_ENTRY", res.Code)
	}
}

// TestLoanBroker_SetWrongOwner asserts only the vault owner may open a broker.
func TestLoanBroker_SetWrongOwner(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	other := jtx.NewAccount("other")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(other, 10_000_000_000)
	vid := setupXRPVault(t, env, owner, 1_000_000_000)

	set := lending.NewLoanBrokerSet(other.Address, vid)
	if res := env.Submit(set); res.Code != "tecNO_PERMISSION" {
		t.Fatalf("LoanBrokerSet (wrong owner): got %s, want tecNO_PERMISSION", res.Code)
	}
}

// TestLoanBroker_CoverDepositInsufficientFunds asserts the funds check.
func TestLoanBroker_CoverDepositInsufficientFunds(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)
	vid := setupXRPVault(t, env, owner, 1_000_000_000)

	brokerSeq := env.Seq(owner)
	set := lending.NewLoanBrokerSet(owner.Address, vid)
	if res := env.Submit(set); res.Code != "tesSUCCESS" {
		t.Fatalf("LoanBrokerSet: got %s, want tesSUCCESS", res.Code)
	}
	bid := brokerID(owner, brokerSeq)

	// Deposit more than the owner can spend.
	dep := lending.NewLoanBrokerCoverDeposit(owner.Address, bid, tx.NewXRPAmount(1_000_000_000_000))
	if res := env.Submit(dep); res.Code != "tecINSUFFICIENT_FUNDS" {
		t.Fatalf("CoverDeposit (insufficient): got %s, want tecINSUFFICIENT_FUNDS", res.Code)
	}
}

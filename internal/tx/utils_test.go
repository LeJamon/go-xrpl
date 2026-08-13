package tx

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountFundsNoFreezeStrictRejectsReserveOverflow(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 1
	account, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:    account,
		Balance:    1_000_000,
		OwnerCount: math.MaxUint32,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	view.data[keylet.Account(accountID).Key] = data

	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), 0, math.MaxUint64); err == nil {
		t.Fatal("AccountFundsNoFreezeStrict multiplication overflow error = nil")
	}

	data, err = state.SerializeAccountRoot(&state.AccountRoot{
		Account:    account,
		Balance:    1_000_000,
		OwnerCount: 1,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view.data[keylet.Account(accountID).Key] = data
	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), math.MaxUint64, 1); err == nil {
		t.Fatal("AccountFundsNoFreezeStrict addition overflow error = nil")
	}
}

func TestXRPLiquidUsesSponsoredReserveCounts(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 2
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:                accountAddress,
		Balance:                30,
		OwnerCount:             5,
		SponsoredOwnerCount:    2,
		SponsoringOwnerCount:   1,
		SponsoringAccountCount: 0,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	view.rules = amendment.NewRules([][32]byte{amendment.FeatureSponsor})
	view.data[keylet.Account(accountID).Key] = data

	if got := XRPLiquid(view, accountID, 0, 10, 2).Drops(); got != 12 {
		t.Fatalf("XRPLiquid = %d, want 12", got)
	}
	if got := XRPLiquid(view, accountID, 1, 10, 2).Drops(); got != 10 {
		t.Fatalf("XRPLiquid(+1) = %d, want 10", got)
	}
}

// TestLPTokenFrozenForIssuer_AMMUnresolvable covers the corrupt-ledger arm: an
// issuer AccountRoot carrying sfAMMID whose referenced AMM SLE is absent. rippled
// returns tecINTERNAL from checkFreeze here (StepChecks.h:71-72, LCOV_EXCL_LINE)
// and zeroes funds in accountHolds (View.cpp:429-431). The status must therefore
// be reported distinctly so the two call sites can diverge.
func TestLPTokenFrozenForIssuer_AMMUnresolvable(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0xAA
	var holder [20]byte
	holder[0] = 0xBB

	issuerAddr, err := state.EncodeAccountID(issuer)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}

	var ammID [32]byte
	ammID[0] = 0xCC // non-zero so HasAMMID() is true

	acct := &state.AccountRoot{
		Account: issuerAddr,
		Balance: 1_000_000,
		AMMID:   ammID,
	}
	acctData, err := state.SerializeAccountRoot(acct)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}

	view := newMockBaseView()
	view.data[keylet.Account(issuer).Key] = acctData
	// Deliberately do NOT store keylet.AMMByID(ammID): the AMM SLE is missing.

	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenAMMUnresolvable {
		t.Fatalf("LPTokenFrozenForIssuer with missing AMM SLE = %v, want LPTokenAMMUnresolvable", got)
	}
}

// TestLPTokenFrozenForIssuer_NotAMM confirms a plain (non-AMM) issuer reports
// LPTokenIssuerNotAMM, leaving the freeze fast-path untouched.
func TestLPTokenFrozenForIssuer_NotAMM(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0x11
	var holder [20]byte
	holder[0] = 0x22

	issuerAddr, err := state.EncodeAccountID(issuer)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}

	acct := &state.AccountRoot{
		Account: issuerAddr,
		Balance: 1_000_000,
	}
	acctData, err := state.SerializeAccountRoot(acct)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}

	view := newMockBaseView()
	view.data[keylet.Account(issuer).Key] = acctData

	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenIssuerNotAMM {
		t.Fatalf("LPTokenFrozenForIssuer for non-AMM issuer = %v, want LPTokenIssuerNotAMM", got)
	}
}

// TestLPTokenFrozenForIssuer_MissingIssuer confirms a missing issuer AccountRoot
// reports LPTokenIssuerNotAMM (rippled's `!sleIssuer` / no sleDst path).
func TestLPTokenFrozenForIssuer_MissingIssuer(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0x33
	var holder [20]byte
	holder[0] = 0x44

	view := newMockBaseView()
	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenIssuerNotAMM {
		t.Fatalf("LPTokenFrozenForIssuer for missing issuer = %v, want LPTokenIssuerNotAMM", got)
	}
}

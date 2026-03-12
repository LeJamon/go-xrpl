package testing

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/tx/offer"
)

// EnableDepositAuth enables the DepositAuth flag on an account.
// When enabled, the account can only receive payments from preauthorized accounts.
func (e *TestEnv) EnableDepositAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.EnableDepositAuth()
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable DepositAuth for account %s: %s", acc.Name, result.Code)
	}
}

// DisableDepositAuth disables the DepositAuth flag on an account.
func (e *TestEnv) DisableDepositAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDepositAuth
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable DepositAuth for account %s: %s", acc.Name, result.Code)
	}
}

// EnableGlobalFreeze enables the GlobalFreeze flag on an account.
// When enabled, all trust lines for this account's issued currencies are frozen.
func (e *TestEnv) EnableGlobalFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagGlobalFreeze
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable GlobalFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// DisableGlobalFreeze disables the GlobalFreeze flag on an account.
// Note: Cannot be cleared if NoFreeze is set.
func (e *TestEnv) DisableGlobalFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagGlobalFreeze
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable GlobalFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// EnableNoFreeze enables the NoFreeze flag on an account.
// Once set, this flag cannot be cleared. It prevents the account from freezing trust lines.
func (e *TestEnv) EnableNoFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagNoFreeze
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable NoFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// SetTransferRate sets the transfer rate for an account.
// Rate is specified as a multiplier * 1e9 (1e9 = 100%, 1.1e9 = 110% means 10% fee).
// Use 0 to clear the transfer rate (sets it back to 1e9 / 100%).
func (e *TestEnv) SetTransferRate(acc *Account, rate uint32) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.TransferRate = &rate
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to set transfer rate for account %s: %s", acc.Name, result.Code)
	}
}

// EnableRequireAuth enables the RequireAuth flag on an account.
// When enabled, trust lines to this account require authorization.
func (e *TestEnv) EnableRequireAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireAuth
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable RequireAuth for account %s: %s", acc.Name, result.Code)
	}
}

// DisableRequireAuth disables the RequireAuth flag on an account.
func (e *TestEnv) DisableRequireAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireAuth
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable RequireAuth for account %s: %s", acc.Name, result.Code)
	}
}

// EnableRequireDest enables the RequireDest flag on an account.
// When enabled, the account requires a destination tag on incoming payments.
func (e *TestEnv) EnableRequireDest(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireDest
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable RequireDest for account %s: %s", acc.Name, result.Code)
	}
}

// DisableRequireDest disables the RequireDest flag on an account.
func (e *TestEnv) DisableRequireDest(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireDest
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable RequireDest for account %s: %s", acc.Name, result.Code)
	}
}

// Preauthorize allows owner to preauthorize authorized for deposits.
// This creates a DepositPreauth ledger entry.
func (e *TestEnv) Preauthorize(owner, authorized *Account) {
	e.t.Helper()

	preauth := depositpreauth.NewDepositPreauth(owner.Address)
	preauth.SetAuthorize(authorized.Address)
	preauth.Fee = formatUint64(e.baseFee)
	seq := e.Seq(owner)
	preauth.Sequence = &seq

	result := e.Submit(preauth)
	if !result.Success {
		e.t.Fatalf("Failed to preauthorize %s for %s: %s", authorized.Name, owner.Name, result.Code)
	}
}

// Unauthorize removes preauthorization for authorized from owner.
func (e *TestEnv) Unauthorize(owner, authorized *Account) {
	e.t.Helper()

	preauth := depositpreauth.NewDepositPreauth(owner.Address)
	preauth.SetUnauthorize(authorized.Address)
	preauth.Fee = formatUint64(e.baseFee)
	seq := e.Seq(owner)
	preauth.Sequence = &seq

	result := e.Submit(preauth)
	if !result.Success {
		e.t.Fatalf("Failed to unauthorize %s for %s: %s", authorized.Name, owner.Name, result.Code)
	}
}

// CreateOffer creates an offer on the DEX.
// takerGets is what the offer creator will receive (what the taker pays).
// takerPays is what the offer creator will pay (what the taker gets).
func (e *TestEnv) CreateOffer(acc *Account, takerGets, takerPays tx.Amount) TxResult {
	e.t.Helper()

	offerTx := offer.NewOfferCreate(acc.Address, takerGets, takerPays)
	offerTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	offerTx.Sequence = &seq

	return e.Submit(offerTx)
}

// CreatePassiveOffer creates a passive offer on the DEX.
// Passive offers don't immediately consume offers at an equal or better rate.
func (e *TestEnv) CreatePassiveOffer(acc *Account, takerGets, takerPays tx.Amount) TxResult {
	e.t.Helper()

	offerTx := offer.NewOfferCreate(acc.Address, takerGets, takerPays)
	offerTx.SetPassive()
	offerTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	offerTx.Sequence = &seq

	return e.Submit(offerTx)
}

// DisableMasterKey disables the master key on an account using AccountSet.
// The account must have a regular key or signer list set first.
func (e *TestEnv) DisableMasterKey(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisableMaster
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable master key for %s: %s", acc.Name, result.Code)
	}
}

// Noop submits a no-op AccountSet to bump an account's sequence number.
// Reference: rippled's noop(account) in noop.h
func (e *TestEnv) Noop(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed noop for %s: %s", acc.Name, result.Code)
	}
}

// NoopWithFee submits a no-op AccountSet with a custom fee.
// Reference: rippled's env(noop(account), fee(f))
func (e *TestEnv) NoopWithFee(acc *Account, fee uint64) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.Fee = formatUint64(fee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed noop for %s: %s", acc.Name, result.Code)
	}
}

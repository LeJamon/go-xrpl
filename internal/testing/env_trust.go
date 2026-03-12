package testing

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/delegate"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/trustset"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Trust creates a trust line and refunds the fee from master.
// Reference: rippled's Env::trust(amount, account) in Env.h
func (e *TestEnv) Trust(acc *Account, amount tx.Amount) {
	e.t.Helper()

	ts := trustset.NewTrustSet(acc.Address, amount)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to set trust line for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingCheck enables the DisallowIncomingCheck flag on an account.
func (e *TestEnv) EnableDisallowIncomingCheck(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingCheck
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingCheck for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingCheck disables the DisallowIncomingCheck flag on an account.
func (e *TestEnv) DisableDisallowIncomingCheck(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingCheck
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingCheck for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingPayChan enables the DisallowIncomingPayChan flag on an account.
func (e *TestEnv) EnableDisallowIncomingPayChan(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingPayChan
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingPayChan for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingPayChan disables the DisallowIncomingPayChan flag on an account.
func (e *TestEnv) DisableDisallowIncomingPayChan(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingPayChan
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingPayChan for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingNFTokenOffer enables the DisallowIncomingNFTokenOffer flag on an account.
func (e *TestEnv) EnableDisallowIncomingNFTokenOffer(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingNFTokenOffer
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingNFTokenOffer for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingNFTokenOffer disables the DisallowIncomingNFTokenOffer flag on an account.
func (e *TestEnv) DisableDisallowIncomingNFTokenOffer(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingNFTokenOffer
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingNFTokenOffer for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingTrustline enables the DisallowIncomingTrustline flag on an account.
func (e *TestEnv) EnableDisallowIncomingTrustline(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingTrustline
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingTrustline for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingTrustline disables the DisallowIncomingTrustline flag on an account.
func (e *TestEnv) DisableDisallowIncomingTrustline(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingTrustline
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingTrustline for %s: %s", acc.Name, result.Code)
	}
}

// FreezeTrustLine freezes a specific trust line (issuer-side freeze).
// Reference: rippled's trust(account, amount, peer, tfSetFreeze)
func (e *TestEnv) FreezeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	// The issuer sets a trust line with the Freeze flag
	amount := state.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFreeze()
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to freeze trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// UnfreezeTrustLine unfreezes a specific trust line.
func (e *TestEnv) UnfreezeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	amount := state.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFlags(ts.GetFlags() | trustset.TrustSetFlagClearFreeze)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to unfreeze trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// AuthorizeTrustLine authorizes a trust line (when RequireAuth is set on the issuer).
// Reference: rippled's trust(account, amount, tfSetfAuth)
func (e *TestEnv) AuthorizeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	amount := state.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFlags(ts.GetFlags() | trustset.TrustSetFlagSetfAuth)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to authorize trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// ClearTrustLineAuth clears the authorization flags on a trust line between two accounts.
// This directly modifies ledger state, simulating rippled's rawInsert for tests
// that require unauthorized but funded trust lines.
func (e *TestEnv) ClearTrustLineAuth(acc1, acc2 *Account, currency string) {
	e.t.Helper()

	lineKey := keylet.Line(acc1.ID, acc2.ID, currency)
	data, err := e.ledger.Read(lineKey)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: trust line not found: %v", err)
		return
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to parse trust line: %v", err)
		return
	}

	rs.Flags &^= state.LsfLowAuth | state.LsfHighAuth

	updated, err := state.SerializeRippleState(rs)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to serialize: %v", err)
		return
	}

	if err := e.ledger.Update(lineKey, updated); err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to update: %v", err)
	}
}

// PayIOU sends an IOU payment from sender to receiver.
// The issuer is the gateway that issued the currency.
// Reference: rippled's env(pay(sender, receiver, amount))
func (e *TestEnv) PayIOU(sender, receiver *Account, issuer *Account, currency string, amount float64) {
	e.t.Helper()

	amt := tx.NewIssuedAmountFromFloat64(amount, currency, issuer.Address)
	payTx := payment.NewPayment(sender.Address, receiver.Address, amt)
	payTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(sender)
	payTx.Sequence = &seq

	result := e.Submit(payTx)
	if !result.Success {
		e.t.Fatalf("Failed to pay %f %s from %s to %s: %s", amount, currency, sender.Name, receiver.Name, result.Code)
	}
}

// PayIOUWithSendMax sends an IOU payment with a SendMax limit.
// Reference: rippled's env(pay(sender, receiver, amount), sendmax(max))
func (e *TestEnv) PayIOUWithSendMax(sender, receiver *Account, issuer *Account, currency string, amount, sendMax float64) {
	e.t.Helper()

	amt := tx.NewIssuedAmountFromFloat64(amount, currency, issuer.Address)
	maxAmt := tx.NewIssuedAmountFromFloat64(sendMax, currency, issuer.Address)
	payTx := payment.NewPayment(sender.Address, receiver.Address, amt)
	payTx.SendMax = &maxAmt
	payTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(sender)
	payTx.Sequence = &seq

	result := e.Submit(payTx)
	if !result.Success {
		e.t.Fatalf("Failed to pay %f %s (sendmax %f) from %s to %s: %s",
			amount, currency, sendMax, sender.Name, receiver.Name, result.Code)
	}
}

// SetTransferRateDirect modifies the TransferRate directly in ledger state.
// This bypasses transaction validation, allowing out-of-bounds rates for testing
// legacy MainNet accounts.
// Reference: rippled AccountSet_test.cpp lines 446-460 (env.app().openLedger().modify())
func (e *TestEnv) SetTransferRateDirect(acc *Account, rate uint32) {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to read account: %v", err)
	}

	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to parse account: %v", err)
	}

	accountRoot.TransferRate = rate

	updated, err := state.SerializeAccountRoot(accountRoot)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to serialize: %v", err)
	}

	if err := e.ledger.Update(accountKey, updated); err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to update: %v", err)
	}
}

// SetMintedNFTokensDirect directly modifies an account's MintedNFTokens field
// in the ledger, bypassing normal transaction validation.
// This is used to test boundary conditions (e.g., near 0xFFFFFFFF) without
// actually minting billions of tokens.
// Reference: rippled NFToken_test.cpp testMintMaxTokens (env.app().openLedger().modify())
func (e *TestEnv) SetMintedNFTokensDirect(acc *Account, count uint32) {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("SetMintedNFTokensDirect: failed to read account: %v", err)
	}

	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		e.t.Fatalf("SetMintedNFTokensDirect: failed to parse account: %v", err)
	}

	accountRoot.MintedNFTokens = count

	updated, err := state.SerializeAccountRoot(accountRoot)
	if err != nil {
		e.t.Fatalf("SetMintedNFTokensDirect: failed to serialize: %v", err)
	}

	if err := e.ledger.Update(accountKey, updated); err != nil {
		e.t.Fatalf("SetMintedNFTokensDirect: failed to update: %v", err)
	}
}

// SetFirstNFTokenSequenceDirect directly modifies an account's FirstNFTokenSequence
// field in the ledger, bypassing normal transaction validation.
// This is used to test boundary conditions with fixNFTokenRemint.
// Reference: rippled NFToken_test.cpp testMintMaxTokens (env.app().openLedger().modify())
func (e *TestEnv) SetFirstNFTokenSequenceDirect(acc *Account, seq uint32) {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("SetFirstNFTokenSequenceDirect: failed to read account: %v", err)
	}

	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		e.t.Fatalf("SetFirstNFTokenSequenceDirect: failed to parse account: %v", err)
	}

	accountRoot.FirstNFTokenSequence = seq
	accountRoot.HasFirstNFTSeq = true

	updated, err := state.SerializeAccountRoot(accountRoot)
	if err != nil {
		e.t.Fatalf("SetFirstNFTokenSequenceDirect: failed to serialize: %v", err)
	}

	if err := e.ledger.Update(accountKey, updated); err != nil {
		e.t.Fatalf("SetFirstNFTokenSequenceDirect: failed to update: %v", err)
	}
}

// SetDelegate creates a Delegate SLE that grants delegation permissions from one account to another.
// permissions is a list of permission names like "Payment", "AccountDomainSet", etc.
// Reference: rippled's delegate::set(account, authorize, permissions) in Delegate_test.cpp
func (e *TestEnv) SetDelegate(owner, authorized *Account, permissions []string) {
	e.t.Helper()

	ds := delegate.NewDelegateSet(owner.Address)
	ds.Authorize = authorized.Address
	for _, perm := range permissions {
		ds.Permissions = append(ds.Permissions, delegate.Permission{
			Permission: delegate.PermissionData{
				PermissionValue: perm,
			},
		})
	}

	result := e.Submit(ds)
	if !result.Success {
		e.t.Fatalf("SetDelegate(%s -> %s, %v): %s: %s", owner.Name, authorized.Name, permissions, result.Code, result.Message)
	}
}

// SetAmendments replaces the current amendment set with exactly the named amendments.
// This is used by the conformance runner to configure the exact amendment set from fixtures.
func (e *TestEnv) SetAmendments(names []string) {
	e.rulesBuilder = amendment.NewRulesBuilder()
	for _, name := range names {
		e.rulesBuilder.EnableByName(name)
	}
}

// ReimburseFeeDirect directly adds baseFee drops back to an account's balance
// in the ledger, without submitting a transaction. This matches rippled's test
// framework behavior where trust line setup reimburses the transaction fee so
// the account's XRP balance is unchanged.
func (e *TestEnv) ReimburseFeeDirect(acc *Account) {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("ReimburseFeeDirect: failed to read account %s: %v", acc.Name, err)
	}

	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		e.t.Fatalf("ReimburseFeeDirect: failed to parse account %s: %v", acc.Name, err)
	}

	accountRoot.Balance += e.baseFee

	updated, err := state.SerializeAccountRoot(accountRoot)
	if err != nil {
		e.t.Fatalf("ReimburseFeeDirect: failed to serialize account %s: %v", acc.Name, err)
	}

	if err := e.ledger.Update(accountKey, updated); err != nil {
		e.t.Fatalf("ReimburseFeeDirect: failed to update account %s: %v", acc.Name, err)
	}
}

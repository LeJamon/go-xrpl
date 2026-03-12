package testing

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/signerlist"
)

// ===========================================================================
// Regular Key helpers
// ===========================================================================

// SetRegularKey sets a regular key on an account.
// Reference: rippled's regkey(account, signer) in regkey.h
func (e *TestEnv) SetRegularKey(acc, regularKey *Account) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.SetKey(regularKey.Address)
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if !result.Success {
		e.t.Fatalf("Failed to set regular key for %s: %s", acc.Name, result.Code)
	}
}

// DisableRegularKey removes the regular key from an account.
// Reference: rippled's regkey(account, disabled) in regkey.h
func (e *TestEnv) DisableRegularKey(acc *Account) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if !result.Success {
		e.t.Fatalf("Failed to disable regular key for %s: %s", acc.Name, result.Code)
	}
}

// DisableRegularKeyExpect attempts to clear the regular key and expects a specific result.
func (e *TestEnv) DisableRegularKeyExpect(acc *Account, expectedCode string) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if result.Code != expectedCode {
		e.t.Fatalf("DisableRegularKeyExpect: expected %s, got %s", expectedCode, result.Code)
	}
}

// ===========================================================================
// SignerList helpers
// ===========================================================================

// TestSigner represents a signer entry for use in SetSignerList.
type TestSigner struct {
	Account *Account
	Weight  uint16
}

// SetSignerList sets a signer list on an account.
// Reference: rippled's signers(account, quorum, signerList) in multisign.h
func (e *TestEnv) SetSignerList(acc *Account, quorum uint32, signers []TestSigner) {
	e.t.Helper()

	sl := signerlist.NewSignerListSet(acc.Address, quorum)
	for _, s := range signers {
		sl.AddSigner(s.Account.Address, s.Weight)
	}
	sl.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	sl.Sequence = &seq

	result := e.Submit(sl)
	if !result.Success {
		e.t.Fatalf("Failed to set signer list for %s: %s", acc.Name, result.Code)
	}
}

// RemoveSignerList removes the signer list from an account.
// Reference: rippled's signers(account, none) in multisign.h
func (e *TestEnv) RemoveSignerList(acc *Account) {
	e.t.Helper()

	sl := signerlist.NewSignerListSet(acc.Address, 0)
	sl.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	sl.Sequence = &seq

	result := e.Submit(sl)
	if !result.Success {
		e.t.Fatalf("Failed to remove signer list for %s: %s", acc.Name, result.Code)
	}
}

// ===========================================================================
// Raw transaction helpers for multisign tests
// ===========================================================================

// NewSignerListSetTx creates a raw SignerListSet transaction without submitting.
// Use this when you need to submit via SubmitMultiSigned or SubmitSignedWith.
func NewSignerListSetTx(acc *Account, quorum uint32, signers []TestSigner) tx.Transaction {
	sl := signerlist.NewSignerListSet(acc.Address, quorum)
	for _, s := range signers {
		sl.AddSigner(s.Account.Address, s.Weight)
	}
	return sl
}

// NewRemoveSignerListTx creates a raw SignerListSet transaction that removes the signer list.
func NewRemoveSignerListTx(acc *Account) tx.Transaction {
	return signerlist.NewSignerListSet(acc.Address, 0)
}

// NewSetRegularKeyTx creates a raw SetRegularKey transaction that sets a regular key.
func NewSetRegularKeyTx(acc *Account, regularKey *Account) tx.Transaction {
	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.SetKey(regularKey.Address)
	return setKey
}

// NewDisableRegularKeyTx creates a raw SetRegularKey transaction that clears the regular key.
func NewDisableRegularKeyTx(acc *Account) tx.Transaction {
	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	return setKey
}

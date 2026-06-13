package tx

import (
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// ErrAccountNotFound is the sentinel returned by SignerListLookup.GetAccountInfo
// when the account is genuinely absent from the ledger (rippled's view.read()
// returning null). Callers distinguish it from a real storage/parse failure with
// errors.Is: a not-found account takes the phantom-signer branch, whereas any
// other error must surface as an internal failure rather than silently allowing
// the signer.
var ErrAccountNotFound = errors.New("account not found")

// signerAccountState is the resolved ledger state of a multi-sign signer's
// account, as consumed by the shared authorization decision. found=false marks
// a phantom account (absent from the ledger); when found, flags and regularKey
// carry the values needed for the master/regular-key decision.
type signerAccountState struct {
	found      bool
	flags      uint32
	regularKey string
}

// authorizeMultiSigner is the single source of truth for the multi-sign signer
// authorization decision table, shared by Transactor::checkMultiSign-style
// callers (preclaim's checkBatchMultiSign and the preflight-crypto
// VerifyMultiSignature). It decides whether one signer is authorized to sign,
// given the account ID derived from the signer's public key and the signer
// account's ledger state.
//
// Returns TesSUCCESS when the signer is authorized, or the matching TER code:
//   - TefMASTER_DISABLED when signing with a disabled master key
//   - TefBAD_SIGNATURE when the key matches neither a phantom/master nor the
//     account's regular key
//
// The three accepted relationships mirror rippled Transactor::checkMultiSign
// (Transactor.cpp:825-895):
//  1. Phantom — derivedAccount == signerAccount and the account is not in the
//     ledger: always allowed.
//  2. Master key — derivedAccount == signerAccount and the account exists with
//     the master key enabled.
//  3. Regular key — derivedAccount != signerAccount and matches the account's
//     RegularKey.
//
// Sortedness/duplicate/quorum and crypto verification are the callers'
// responsibility — this function only renders the per-signer authorization
// verdict.
func authorizeMultiSigner(signerAccount, derivedAccount string, acct signerAccountState) Result {
	if derivedAccount == signerAccount {
		// Phantom or Master key. Phantoms (absent account) always pass.
		if acct.found && acct.flags&state.LsfDisableMaster != 0 {
			return TefMASTER_DISABLED
		}
		return TesSUCCESS
	}
	// Regular key: the account must exist and its RegularKey must match.
	if !acct.found || acct.regularKey == "" || derivedAccount != acct.regularKey {
		return TefBAD_SIGNATURE
	}
	return TesSUCCESS
}

// engineSignerListLookup implements SignerListLookup using the engine's ledger view
type engineSignerListLookup struct {
	view LedgerView
}

// GetSignerList returns the signer list for an account
func (l *engineSignerListLookup) GetSignerList(account string) (*state.SignerListInfo, error) {
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return nil, err
	}

	// Look up the signer list (SignerListID is always 0 currently)
	signerListKey := keylet.SignerList(accountID)
	exists, err := l.view.Exists(signerListKey)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil // No signer list
	}

	// Read and parse the signer list
	signerListData, err := l.view.Read(signerListKey)
	if err != nil {
		return nil, err
	}

	signerList, err := state.ParseSignerList(signerListData)
	if err != nil {
		return nil, err
	}

	return signerList, nil
}

// GetAccountInfo returns account information needed for signer validation
func (l *engineSignerListLookup) GetAccountInfo(account string) (flags uint32, regularKey string, err error) {
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return 0, "", err
	}

	accountKey := keylet.Account(accountID)
	accountData, err := l.view.Read(accountKey)
	if err != nil {
		return 0, "", err
	}
	if accountData == nil {
		return 0, "", ErrAccountNotFound
	}

	accountRoot, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return 0, "", err
	}

	return accountRoot.Flags, accountRoot.RegularKey, nil
}

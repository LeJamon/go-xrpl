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

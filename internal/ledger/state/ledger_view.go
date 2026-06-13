package state

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LedgerView provides read/write access to ledger state
type LedgerView interface {
	// Read reads a ledger entry
	Read(k keylet.Keylet) ([]byte, error)

	// Exists checks if an entry exists
	Exists(k keylet.Keylet) (bool, error)

	// Insert adds a new entry
	Insert(k keylet.Keylet, data []byte) error

	// Update modifies an existing entry
	Update(k keylet.Keylet, data []byte) error

	// Erase removes an entry
	Erase(k keylet.Keylet) error

	// Rules returns the amendment rules for this view, or nil when the view has
	// no rules attached (e.g. a bare *ledger.Ledger). nil has no single global
	// meaning: directory page-limit logic in DirInsert treats nil as
	// pre-amendment (enforces the legacy page cap), whereas open-ledger
	// application treats nil as "all amendments enabled". Callers that care about
	// amendment-gated behaviour must pass a non-nil Rules; the nil fallbacks are
	// only for contexts where the distinction cannot affect the result.
	Rules() *amendment.Rules
}

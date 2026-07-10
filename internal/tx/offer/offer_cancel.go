// Reference: rippled CreateOffer.cpp, CancelOffer.cpp
package offer

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// OfferCancel cancels an existing offer on the decentralized exchange.
type OfferCancel struct {
	tx.BaseTx

	// OfferSequence is the sequence number of the offer to cancel (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`
}

// NewOfferCancel creates a new OfferCancel transaction
func NewOfferCancel(account string, offerSequence uint32) *OfferCancel {
	return &OfferCancel{
		BaseTx:        *tx.NewBaseTx(tx.TypeOfferCancel, account),
		OfferSequence: offerSequence,
	}
}

func (o *OfferCancel) TxType() tx.Type {
	return tx.TypeOfferCancel
}

// GetFlagsMask returns the invalid-flags mask enforced by the engine at the
// preflight0 position. CancelOffer defines no type-specific flags, so it uses the
// universal mask (rippled's base Transactor::getFlagsMask).
func (o *OfferCancel) GetFlagsMask(*amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled CancelOffer.cpp preflight()
func (o *OfferCancel) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	if o.OfferSequence == 0 {
		return ter.Errorf(ter.TemBAD_SEQUENCE, "OfferSequence is required and cannot be zero")
	}

	return nil
}

func (o *OfferCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// Preclaim validates OfferCancel against ledger state before application: the
// account's stored sequence must be strictly greater than OfferSequence.
// Extracting this from Apply makes it visible to the preclaim-only paths (TxQ
// admission, simulate), matching rippled where it lives in OfferCancel::preclaim.
// The view holds the pre-transaction sequence — doApply consumes it later — so the
// comparison reads it directly, without the engine pre-increment undo it needed in
// Apply. Reference: rippled OfferCancel.cpp preclaim().
func (o *OfferCancel) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(o.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	account, readErr := tx.ReadAccountRoot(view, accountID)
	if readErr != nil {
		return ter.TefINTERNAL
	}
	if account == nil {
		return ter.TerNO_ACCOUNT
	}
	if account.Sequence <= o.OfferSequence {
		return ter.TemBAD_SEQUENCE
	}
	return ter.TesSUCCESS
}

// Reference: rippled OfferCancel.cpp doApply()
func (o *OfferCancel) Apply(ctx *tx.ApplyContext) ter.Result {
	// Find the offer
	accountID, _ := state.DecodeAccountID(ctx.Account.Account)
	offerKey := keylet.Offer(accountID, o.OfferSequence)

	exists, err := ctx.View.Exists(offerKey)
	if err != nil {
		return ter.TefINTERNAL
	}

	if !exists {
		// Offer doesn't exist - this is OK (maybe already filled/cancelled)
		// Reference: rippled CancelOffer.cpp lines 91-92
		return ter.TesSUCCESS
	}

	// Read the offer to get its details for metadata and directory removal
	offerData, err := ctx.View.Read(offerKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	ledgerOffer, err := state.ParseLedgerOffer(offerData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Remove from owner directory (keepRoot = false since owner dir should persist)
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerDirResult, err := state.DirRemove(ctx.View, ownerDirKey, ledgerOffer.OwnerNode, offerKey.Key, false)
	if err != nil {
		return ter.TefINTERNAL
	}
	if !ownerDirResult.Success {
		return ter.TefBAD_LEDGER
	}

	// Remove from book directory (keepRoot = false - delete directory if empty)
	bookDirKey := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: ledgerOffer.BookDirectory}
	bookDirResult, err := state.DirRemove(ctx.View, bookDirKey, ledgerOffer.BookNode, offerKey.Key, false)
	if err != nil {
		return ter.TefINTERNAL
	}
	if !bookDirResult.Success {
		return ter.TefBAD_LEDGER
	}

	if err := ctx.View.Erase(offerKey); err != nil {
		return ter.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return ter.TesSUCCESS
}

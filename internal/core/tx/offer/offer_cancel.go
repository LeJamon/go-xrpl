// Reference: rippled CreateOffer.cpp, CancelOffer.cpp
package offer

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// OfferCancel cancels an existing offer on the decentralized exchange.
type OfferCancel struct {
	tx.BaseTx

	// OfferSequence is the sequence number of the offer to cancel (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`
}

func init() {
	tx.Register(tx.TypeOfferCancel, func() tx.Transaction {
		return &OfferCancel{BaseTx: *tx.NewBaseTx(tx.TypeOfferCancel, "")}
	})
}

// NewOfferCancel creates a new OfferCancel transaction
func NewOfferCancel(account string, offerSequence uint32) *OfferCancel {
	return &OfferCancel{
		BaseTx:        *tx.NewBaseTx(tx.TypeOfferCancel, account),
		OfferSequence: offerSequence,
	}
}

// TxType returns the transaction type
func (o *OfferCancel) TxType() tx.Type {
	return tx.TypeOfferCancel
}

// Validate validates the OfferCancel transaction
// Reference: rippled CancelOffer.cpp preflight()
func (o *OfferCancel) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	if o.OfferSequence == 0 {
		return errors.New("temBAD_SEQUENCE: OfferSequence is required and cannot be zero")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (o *OfferCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// Apply applies an OfferCancel transaction to the ledger state.
// Reference: rippled CancelOffer.cpp preclaim() + doApply()
func (o *OfferCancel) Apply(ctx *tx.ApplyContext) tx.Result {
	// Preclaim: Account sequence must be strictly greater than OfferSequence
	// Reference: rippled CancelOffer.cpp preclaim() lines 59-72
	// Note: The engine has already incremented ctx.Account.Sequence by 1 for
	// non-ticket transactions, so we compare against (Sequence - 1) to get
	// the original stored sequence that rippled checks in preclaim.
	// For ticket transactions, ctx.Account.Sequence is unchanged.
	accountSeq := ctx.Account.Sequence
	common := o.GetCommon()
	if common.TicketSequence == nil {
		accountSeq-- // undo engine's pre-increment
	}
	if accountSeq <= o.OfferSequence {
		return tx.TemBAD_SEQUENCE
	}

	// Find the offer
	accountID, _ := sle.DecodeAccountID(ctx.Account.Account)
	offerKey := keylet.Offer(accountID, o.OfferSequence)

	exists, err := ctx.View.Exists(offerKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !exists {
		// Offer doesn't exist - this is OK (maybe already filled/cancelled)
		// Reference: rippled CancelOffer.cpp lines 91-92
		return tx.TesSUCCESS
	}

	// Read the offer to get its details for metadata and directory removal
	offerData, err := ctx.View.Read(offerKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	ledgerOffer, err := sle.ParseLedgerOffer(offerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Create SLE for the offer for metadata tracking
	sleOffer := sle.NewSLEOffer(offerKey.Key)
	sleOffer.LoadFromLedgerOffer(ledgerOffer)
	sleOffer.MarkAsDeleted()

	// Remove from owner directory (keepRoot = false since owner dir should persist)
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerDirResult, err := sle.DirRemove(ctx.View, ownerDirKey, ledgerOffer.OwnerNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !ownerDirResult.Success {
		return tx.TefBAD_LEDGER
	}

	// Remove from book directory (keepRoot = false - delete directory if empty)
	bookDirKey := keylet.Keylet{Type: 100, Key: ledgerOffer.BookDirectory} // DirectoryNode type
	bookDirResult, err := sle.DirRemove(ctx.View, bookDirKey, ledgerOffer.BookNode, offerKey.Key, false)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !bookDirResult.Success {
		return tx.TefBAD_LEDGER
	}

	// Delete the offer from ledger
	if err := ctx.View.Erase(offerKey); err != nil {
		return tx.TefINTERNAL
	}

	// Decrement owner count
	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

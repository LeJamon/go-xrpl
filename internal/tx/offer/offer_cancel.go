// Reference: rippled CreateOffer.cpp, CancelOffer.cpp
package offer

import (
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

// Reference: rippled CancelOffer.cpp preflight()
func (o *OfferCancel) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	if err := tx.CheckFlags(o.GetFlags(), tx.TfUniversalMask); err != nil {
		return ter.Errorf(ter.TemINVALID_FLAG, "invalid flags set")
	}

	if o.OfferSequence == 0 {
		return ter.Errorf(ter.TemBAD_SEQUENCE, "OfferSequence is required and cannot be zero")
	}

	return nil
}

func (o *OfferCancel) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// Reference: rippled CancelOffer.cpp preclaim() + doApply()
func (o *OfferCancel) Apply(ctx *tx.ApplyContext) ter.Result {
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
		return ter.TemBAD_SEQUENCE
	}

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

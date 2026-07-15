package ticket

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TicketCreate creates tickets for future transactions.
type TicketCreate struct {
	tx.BaseTx

	// TicketCount is the number of tickets to create (required, 1-250)
	TicketCount uint32 `json:"TicketCount" xrpl:"TicketCount"`
}

// NewTicketCreate creates a new TicketCreate transaction
func NewTicketCreate(account string, count uint32) *TicketCreate {
	return &TicketCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeTicketCreate, account),
		TicketCount: count,
	}
}

func (t *TicketCreate) TxType() tx.Type {
	return tx.TypeTicketCreate
}

func (t *TicketCreate) RequiredAmendments() [][32]byte {
	return nil
}

// GetFlagsMask adopts the engine FlagsMasker seam. CreateTicket defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
// Reference: rippled Transactor.cpp getFlagsMask() = tfUniversalMask.
func (t *TicketCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled CreateTicket.cpp preflight()
func (t *TicketCreate) Validate() error {
	if err := t.BaseTx.Validate(); err != nil {
		return err
	}

	// TicketCount must be between 1 and 250
	// Reference: rippled CreateTicket.cpp:39-40
	if t.TicketCount == 0 || t.TicketCount > 250 {
		return ter.Errorf(ter.TemINVALID_COUNT, "TicketCount must be 1-250, got %d", t.TicketCount)
	}

	return nil
}

func (t *TicketCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(t)
}

// Reference: rippled CreateTicket.cpp preclaim() + doApply()
// Preclaim runs TicketCreate's ledger-aware check: creating the requested tickets
// must not push the account over the 250-ticket threshold (tecDIR_FULL).
// Extracting it from Apply makes it visible to the preclaim-only paths (TxQ
// admission, simulate), matching rippled where it lives in CreateTicket::preclaim.
// Preclaim runs before the engine consumes this tx's own ticket, so the account's
// stored TicketCount is rippled's pre-consumption curTicketCount and the check
// uses rippled's exact formula curTicketCount + addedTickets - consumedTickets.
// Reference: rippled CreateTicket.cpp preclaim().
func (t *TicketCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(t.Account)
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
	common := t.GetCommon()
	consumed := int64(0)
	if common.TicketSequence != nil && (common.Sequence == nil || *common.Sequence == 0) {
		consumed = 1
	}
	if int64(account.TicketCount)+int64(t.TicketCount)-consumed > 250 {
		return ter.TecDIR_FULL
	}
	return ter.TesSUCCESS
}

// Reference: rippled CreateTicket.cpp doApply()
func (t *TicketCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("ticket create apply",
		"account", t.Account,
		"ticketCount", t.TicketCount,
	)

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)

	// Reserve check: compare the reserve against the prior balance (before the
	// actual fee was deducted), allowing the account to dip into the reserve to
	// pay fees.
	priorBalance := ctx.PriorBalance()
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + t.TicketCount)
	if priorBalance < reserve {
		ctx.Log.Warn("ticket create: insufficient reserve",
			"balance", priorBalance,
			"reserve", reserve,
		)
		return ter.TecINSUFFICIENT_RESERVE
	}

	for i := uint32(0); i < t.TicketCount; i++ {
		ticketSeq := ctx.Account.Sequence + i

		ticketKey := keylet.Ticket(ctx.AccountID, ticketSeq)

		// Insert into the owner directory first so each ticket's sfOwnerNode
		// records the page it actually landed on (a single TicketCreate can mint
		// up to 250 tickets, paginating the owner directory within one tx).
		// Reference: rippled CreateTicket.cpp:126-137.
		dirResult, err := state.DirInsert(ctx.View, ownerDirKey, ticketKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = ctx.AccountID
		})
		if err != nil {
			return ter.TecDIR_FULL
		}

		ticketData, err := state.SerializeTicket(ctx.AccountID, ticketSeq, dirResult.Page)
		if err != nil {
			return ter.TefINTERNAL
		}

		if err := ctx.View.Insert(ticketKey, ticketData); err != nil {
			return ter.TefINTERNAL
		}
	}

	firstSeq := ctx.Account.Sequence
	lastSeq := ctx.Account.Sequence + t.TicketCount - 1
	ctx.Log.Debug("ticket create: sequences reserved",
		"firstSequence", firstSeq,
		"lastSequence", lastSeq,
	)

	ctx.Account.OwnerCount += t.TicketCount
	ctx.Account.Sequence += t.TicketCount

	// Update TicketCount on the AccountRoot
	// Reference: rippled CreateTicket.cpp lines 142-144
	ctx.Account.TicketCount += t.TicketCount

	return ter.TesSUCCESS
}

package ticket

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
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

// RequiredAmendments returns amendments required for TicketCreate.
// Reference: rippled CreateTicket.cpp preflight() — temDISABLED if !featureTicketBatch
func (t *TicketCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureTicketBatch}
}

// Reference: rippled CreateTicket.cpp preflight()
func (t *TicketCreate) Validate() error {
	if err := t.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: rippled CreateTicket.cpp:36 — if (ctx.tx.getFlags() & tfUniversalMask)
	if err := tx.CheckFlags(t.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// TicketCount must be between 1 and 250
	// Reference: rippled CreateTicket.cpp:39-40
	if t.TicketCount == 0 || t.TicketCount > 250 {
		return tx.Errorf(tx.TemINVALID_COUNT, "TicketCount must be 1-250, got %d", t.TicketCount)
	}

	return nil
}

func (t *TicketCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(t)
}

// Reference: rippled CreateTicket.cpp preclaim() + doApply()
func (t *TicketCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("ticket create apply",
		"account", t.Account,
		"ticketCount", t.TicketCount,
	)

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)

	// --- Preclaim checks (done inside Apply since Go has no separate Preclaim) ---

	// Check 250 ticket threshold
	// Reference: rippled CreateTicket.cpp preclaim() lines 63-79
	//
	// In rippled, preclaim() reads sfTicketCount BEFORE ticket consumption.
	// In goXRPL, the engine consumes the ticket BEFORE calling Apply(),
	// so ctx.Account.TicketCount has already been decremented.
	// We use ctx.Account.TicketCount (post-consumption) and simply check
	// whether adding the new tickets would exceed the threshold, which is
	// equivalent to rippled's pre-consumption check:
	//   curTicketCount + addedTickets - consumed > 250
	//   = (postConsumptionCount + consumed) + addedTickets - consumed > 250
	//   = postConsumptionCount + addedTickets > 250
	currentTicketCount := ctx.Account.TicketCount

	// maxTicketThreshold = 250
	if currentTicketCount+t.TicketCount > 250 {
		ctx.Log.Warn("ticket create: would exceed 250 ticket limit",
			"currentCount", currentTicketCount,
			"requestedCount", t.TicketCount,
		)
		return tx.TecDIR_FULL
	}

	// --- doApply checks ---

	// Reserve check
	// Reference: rippled CreateTicket.cpp doApply() lines 97-102
	// mPriorBalance = account balance + fee (fee was already deducted by engine)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + t.TicketCount)
	if priorBalance < reserve {
		ctx.Log.Warn("ticket create: insufficient reserve",
			"balance", priorBalance,
			"reserve", reserve,
		)
		return tx.TecINSUFFICIENT_RESERVE
	}

	for i := uint32(0); i < t.TicketCount; i++ {
		ticketSeq := ctx.Account.Sequence + i

		ticketKey := keylet.Ticket(ctx.AccountID, ticketSeq)

		ticketData, err := state.SerializeTicket(ctx.AccountID, ticketSeq)
		if err != nil {
			return tx.TefINTERNAL
		}

		if err := ctx.View.Insert(ticketKey, ticketData); err != nil {
			return tx.TefINTERNAL
		}

		_, err = state.DirInsert(ctx.View, ownerDirKey, ticketKey.Key, func(dir *state.DirectoryNode) {
			dir.Owner = ctx.AccountID
		})
		if err != nil {
			return tx.TecDIR_FULL
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

	return tx.TesSUCCESS
}

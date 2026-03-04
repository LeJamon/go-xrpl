package ticket

import (
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeTicketCreate, func() tx.Transaction {
		return &TicketCreate{BaseTx: *tx.NewBaseTx(tx.TypeTicketCreate, "")}
	})
}

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

// TxType returns the transaction type
func (t *TicketCreate) TxType() tx.Type {
	return tx.TypeTicketCreate
}

// RequiredAmendments returns amendments required for TicketCreate.
// Reference: rippled CreateTicket.cpp preflight() — temDISABLED if !featureTicketBatch
func (t *TicketCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureTicketBatch}
}

// Validate validates the TicketCreate transaction
// Reference: rippled CreateTicket.cpp preflight()
func (t *TicketCreate) Validate() error {
	if err := t.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: rippled CreateTicket.cpp:36 — if (ctx.tx.getFlags() & tfUniversalMask)
	if t.Common.Flags != nil && *t.Common.Flags&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for TicketCreate")
	}

	// TicketCount must be between 1 and 250
	// Reference: rippled CreateTicket.cpp:39-40
	if t.TicketCount == 0 || t.TicketCount > 250 {
		return fmt.Errorf("temINVALID_COUNT: TicketCount must be 1-250, got %d", t.TicketCount)
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (t *TicketCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(t)
}

// Apply applies the TicketCreate transaction to ledger state.
// Reference: rippled CreateTicket.cpp preclaim() + doApply()
func (t *TicketCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)

	// --- Preclaim checks (done inside Apply since Go has no separate Preclaim) ---

	// Check 250 ticket threshold
	// Reference: rippled CreateTicket.cpp preclaim() lines 63-79
	// Count existing tickets in owner directory
	var currentTicketCount uint32
	_ = sle.DirForEach(ctx.View, ownerDirKey, func(itemKey [32]byte) error {
		entryKey := keylet.Keylet{Key: itemKey}
		data, readErr := ctx.View.Read(entryKey)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		entryType, typeErr := sle.GetLedgerEntryType(data)
		if typeErr != nil {
			return nil
		}
		// Ticket type = 0x0054
		if entryType == 0x0054 {
			currentTicketCount++
		}
		return nil
	})

	// If using a ticket to create tickets, one ticket is consumed
	var consumed uint32
	if t.Common.TicketSequence != nil {
		consumed = 1
	}

	// maxTicketThreshold = 250
	// Reference: rippled CreateTicket.cpp:75 — if (curTicketCount + addedTickets - consumed > 250)
	if currentTicketCount+t.TicketCount-consumed > 250 {
		return tx.TecDIR_FULL
	}

	// --- doApply checks ---

	// Reserve check
	// Reference: rippled CreateTicket.cpp doApply() lines 97-102
	// mPriorBalance = account balance + fee (fee was already deducted by engine)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + t.TicketCount)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Create tickets
	for i := uint32(0); i < t.TicketCount; i++ {
		ticketSeq := ctx.Account.Sequence + i

		ticketKey := keylet.Ticket(ctx.AccountID, ticketSeq)

		ticketData, err := sle.SerializeTicket(ctx.AccountID, ticketSeq)
		if err != nil {
			return tx.TefINTERNAL
		}

		if err := ctx.View.Insert(ticketKey, ticketData); err != nil {
			return tx.TefINTERNAL
		}

		// Add ticket to owner directory
		_, err = sle.DirInsert(ctx.View, ownerDirKey, ticketKey.Key, func(dir *sle.DirectoryNode) {
			dir.Owner = ctx.AccountID
		})
		if err != nil {
			return tx.TecDIR_FULL
		}
	}

	ctx.Account.OwnerCount += t.TicketCount
	ctx.Account.Sequence += t.TicketCount

	return tx.TesSUCCESS
}

package account

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/core/tx/oracle"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeAccountDelete, func() tx.Transaction {
		return &AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, "")}
	})

}

// AccountDelete deletes an account from the ledger.
type AccountDelete struct {
	tx.BaseTx

	// Destination is the account to receive remaining XRP (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
}

// NewAccountDelete creates a new AccountDelete transaction
func NewAccountDelete(account, destination string) *AccountDelete {
	return &AccountDelete{
		BaseTx:      *tx.NewBaseTx(tx.TypeAccountDelete, account),
		Destination: destination,
	}
}

// TxType returns the transaction type
func (a *AccountDelete) TxType() tx.Type {
	return tx.TypeAccountDelete
}

// Validate validates the AccountDelete transaction
func (a *AccountDelete) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	if a.Destination == "" {
		return errors.New("Destination is required")
	}

	// Cannot delete to self
	if a.Account == a.Destination {
		return errors.New("cannot delete account to self")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AccountDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// Apply applies the AccountDelete transaction to ledger state.
// Reference: rippled DeleteAccount.cpp DeleteAccount::preclaim() + doApply()
func (a *AccountDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check minimum ledger gap: account must have existed for at least 256 ledgers.
	// Use the transaction's Sequence (pre-increment value), matching rippled's preclaim().
	// Reference: rippled DeleteAccount.cpp accountDeleteMinLedgerGap = 256
	if a.Common.Sequence != nil {
		if ctx.Config.LedgerSequence-*a.Common.Sequence < 256 {
			return tx.TecTOO_SOON
		}
	}

	destID, err := sle.DecodeAccountID(a.Destination)
	if err != nil {
		return tx.TemINVALID
	}

	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}

	// --- Preclaim: destination checks ---
	// Reference: rippled DeleteAccount.cpp preclaim() lines 230-260
	destAccount, err := sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check if destination requires a destination tag
	if (destAccount.Flags&sle.LsfRequireDestTag) != 0 && a.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Check if destination requires deposit authorization
	if (destAccount.Flags & sle.LsfDepositAuth) != 0 {
		preauthKey := keylet.DepositPreauth(destID, ctx.AccountID)
		preauthExists, _ := ctx.View.Exists(preauthKey)
		if !preauthExists {
			return tx.TecNO_PERMISSION
		}
	}

	// --- Cascade-delete all non-obligation directory entries ---
	// Collect all keys first, then delete — avoids modifying directory during iteration.
	// Reference: rippled DeleteAccount.cpp nonObligationDeleter()
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	var entryKeys [][32]byte

	err = sle.DirForEach(ctx.View, ownerDirKey, func(itemKey [32]byte) error {
		entryKeys = append(entryKeys, itemKey)
		return nil
	})
	if err != nil {
		return tx.TefINTERNAL
	}

	for _, itemKey := range entryKeys {
		itemKeylet := keylet.Keylet{Key: itemKey}
		data, err := ctx.View.Read(itemKeylet)
		if err != nil || data == nil {
			continue
		}

		entryType, err := sle.GetLedgerEntryType(data)
		if err != nil {
			return tx.TecHAS_OBLIGATIONS
		}

		switch entry.Type(entryType) {
		case entry.TypeCredential:
			cred, err := credential.ParseCredentialEntry(data)
			if err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			if err := credential.DeleteSLE(ctx.View, itemKeylet, cred); err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
		case entry.TypeOracle:
			oracleData, err := sle.ParseOracle(data)
			if err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			// nil ownerCount — account is being deleted, no need to adjust
			if result := oracle.DeleteOracleFromView(ctx.View, itemKeylet, oracleData, ctx.AccountID, nil); result != tx.TesSUCCESS {
				return tx.TecHAS_OBLIGATIONS
			}
		case entry.TypeSignerList:
			// Signer lists are not obligations — cascade-delete them.
			// Reference: rippled DeleteAccount.cpp nonObligationDeleter case ltSIGNER_LIST
			sle.DirRemove(ctx.View, ownerDirKey, 0, itemKeylet.Key, true)
			if err := ctx.View.Erase(itemKeylet); err != nil {
				return tx.TecHAS_OBLIGATIONS
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		default:
			return tx.TecHAS_OBLIGATIONS
		}
	}

	// Erase any remaining empty owner directory root page
	if dirData, err := ctx.View.Read(ownerDirKey); err == nil && dirData != nil {
		ctx.View.Erase(ownerDirKey)
	}

	// Re-read destination in case it was modified during cascade deletions
	destData, err = ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err = sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	destAccount.Balance += ctx.Account.Balance

	destUpdatedData, err := sle.SerializeAccountRoot(destAccount)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
		return tx.TefINTERNAL
	}

	srcKey := keylet.Account(ctx.AccountID)
	if err := ctx.View.Erase(srcKey); err != nil {
		return tx.TefINTERNAL
	}

	ctx.Account.Balance = 0

	return tx.TesSUCCESS
}

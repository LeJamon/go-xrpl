// Package escrow implements EscrowCreate, EscrowFinish, and EscrowCancel transactions.
package escrow

import (
	"encoding/hex"
	"errors"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/internal/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeEscrowCreate, func() tx.Transaction {
		return &EscrowCreate{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCreate, "")}
	})
}

// EscrowCreate creates an escrow that holds XRP until certain conditions are met.
type EscrowCreate struct {
	tx.BaseTx

	// Amount is the amount of XRP to escrow (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the account to receive the XRP (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// CancelAfter is the time after which the escrow can be cancelled (optional)
	CancelAfter *uint32 `json:"CancelAfter,omitempty" xrpl:"CancelAfter,omitempty"`

	// FinishAfter is the time after which the escrow can be finished (optional)
	FinishAfter *uint32 `json:"FinishAfter,omitempty" xrpl:"FinishAfter,omitempty"`

	// Condition is the crypto-condition that must be fulfilled (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Condition *string `json:"Condition,omitempty" xrpl:"Condition,omitempty"`
}

// NewEscrowCreate creates a new EscrowCreate transaction
func NewEscrowCreate(account, destination string, amount tx.Amount) *EscrowCreate {
	return &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, account),
		Amount:      amount,
		Destination: destination,
	}
}

// TxType returns the transaction type
func (e *EscrowCreate) TxType() tx.Type {
	return tx.TypeEscrowCreate
}

// Validate validates the EscrowCreate transaction
// Reference: rippled Escrow.cpp EscrowCreate::preflight()
func (e *EscrowCreate) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled Escrow.cpp preflight() fix1543 flag check
	if e.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	if e.Destination == "" {
		return errors.New("temDST_NEEDED: Destination is required")
	}

	// Amount must be positive
	// Reference: rippled Escrow.cpp:146-147
	if e.Amount.IsZero() || e.Amount.IsNegative() {
		return errors.New("temBAD_AMOUNT: Amount must be positive")
	}

	// Must be XRP (unless featureTokenEscrow is enabled)
	// Reference: rippled Escrow.cpp:131-148
	if !e.Amount.IsNative() {
		return errors.New("temBAD_AMOUNT: escrow can only hold XRP")
	}

	// Must have at least one timeout value
	// Reference: rippled Escrow.cpp:151-152
	if e.CancelAfter == nil && e.FinishAfter == nil {
		return errors.New("temBAD_EXPIRATION: must specify CancelAfter or FinishAfter")
	}

	// If both times are specified, CancelAfter must be strictly after FinishAfter
	// Reference: rippled Escrow.cpp:156-158
	if e.CancelAfter != nil && e.FinishAfter != nil {
		if *e.CancelAfter <= *e.FinishAfter {
			return errors.New("temBAD_EXPIRATION: CancelAfter must be after FinishAfter")
		}
	}

	// NOTE: fix1571 check (FinishAfter or Condition required) is done in Apply()
	// where we have access to amendment rules. See EscrowCreate.Apply().

	// Validate condition format if present
	// Reference: rippled Escrow.cpp:170-190 condition deserialization
	if e.Condition != nil {
		if *e.Condition == "" {
			return errors.New("temMALFORMED: empty condition")
		}
		if err := ValidateConditionFormat(*e.Condition); err != nil {
			return errors.New("temMALFORMED: invalid condition")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (e *EscrowCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// Apply applies an EscrowCreate transaction
// Reference: rippled Escrow.cpp EscrowCreate::doApply()
func (ec *EscrowCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()
	closeTime := ctx.Config.ParentCloseTime

	// Amendment-gated preflight: fix1571 requires FinishAfter or Condition
	// Reference: rippled Escrow.cpp:160-167
	if rules.Enabled(amendment.FeatureFix1571) {
		if ec.FinishAfter == nil && (ec.Condition == nil || *ec.Condition == "") {
			return tx.TemMALFORMED
		}
	}

	// Time validation against parent close time
	// Reference: rippled Escrow.cpp:457-489
	if rules.Enabled(amendment.FeatureFix1571) {
		// fix1571: after() means strictly greater than
		if ec.CancelAfter != nil && closeTime > *ec.CancelAfter {
			return tx.TecNO_PERMISSION
		}
		if ec.FinishAfter != nil && closeTime > *ec.FinishAfter {
			return tx.TecNO_PERMISSION
		}
	} else {
		// pre-fix1571: >= comparison
		if ec.CancelAfter != nil && closeTime >= *ec.CancelAfter {
			return tx.TecNO_PERMISSION
		}
		if ec.FinishAfter != nil && closeTime >= *ec.FinishAfter {
			return tx.TecNO_PERMISSION
		}
	}

	// Get the amount to escrow
	amount := ec.Amount.Drops()
	if amount <= 0 {
		return tx.TemINVALID
	}

	// Verify destination exists
	// Reference: rippled Escrow.cpp:511-512
	destID, err := sle.DecodeAccountID(ec.Destination)
	if err != nil {
		return tx.TemINVALID
	}

	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}

	destAccount, err := sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Destination tag check
	// Reference: rippled Escrow.cpp:517-519
	if (destAccount.Flags&sle.LsfRequireDestTag) != 0 && ec.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// DisallowXRP check (only when DepositAuth amendment is NOT enabled)
	// Reference: rippled Escrow.cpp:523-525
	if !rules.Enabled(amendment.FeatureDepositAuth) {
		if (destAccount.Flags & sle.LsfDisallowXRP) != 0 {
			return tx.TecNO_TARGET
		}
	}

	// Reserve check
	// Reference: rippled Escrow.cpp:496-509
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}
	if ctx.Account.Balance < reserve+uint64(amount) {
		return tx.TecUNFUNDED
	}

	// Deduct the escrow amount from the account
	ctx.Account.Balance -= uint64(amount)

	// Create the escrow entry
	accountID, _ := sle.DecodeAccountID(ec.Account)
	sequence := ec.GetCommon().SeqProxy()

	escrowKey := keylet.Escrow(accountID, sequence)

	// Serialize escrow
	escrowData, err := serializeEscrow(ec, accountID, destID, sequence, uint64(amount))
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert escrow - creation tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(escrowKey, escrowData); err != nil {
		return tx.TefINTERNAL
	}

	// Owner directory: insert escrow into owner's directory
	// Reference: rippled Escrow.cpp:550-558
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err = sle.DirInsert(ctx.View, ownerDirKey, escrowKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	// If cross-account, insert into destination's owner directory and
	// increment destination's OwnerCount.
	// Reference: rippled Escrow.cpp:560-570 + adjustOwnerCount
	if destID != accountID {
		destDirKey := keylet.OwnerDir(destID)
		_, err = sle.DirInsert(ctx.View, destDirKey, escrowKey.Key, func(dir *sle.DirectoryNode) {
			dir.Owner = destID
		})
		if err != nil {
			return tx.TecDIR_FULL
		}

		// Increment destination's OwnerCount
		destAccount.OwnerCount++
		destUpdatedData2, err := sle.SerializeAccountRoot(destAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(destKey, destUpdatedData2); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Increase owner count for the escrow creator
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// serializeEscrow serializes an Escrow ledger entry
func serializeEscrow(txn *EscrowCreate, ownerID, destID [20]byte, sequence uint32, amount uint64) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Amount":          fmt.Sprintf("%d", amount),
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	if txn.FinishAfter != nil {
		jsonObj["FinishAfter"] = *txn.FinishAfter
	}

	if txn.CancelAfter != nil {
		jsonObj["CancelAfter"] = *txn.CancelAfter
	}

	if txn.Condition != nil && *txn.Condition != "" {
		jsonObj["Condition"] = *txn.Condition
	}

	// SourceTag from Common fields
	if txn.GetCommon().SourceTag != nil {
		jsonObj["SourceTag"] = *txn.GetCommon().SourceTag
	}

	if txn.DestinationTag != nil {
		jsonObj["DestinationTag"] = *txn.DestinationTag
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Escrow: %w", err)
	}

	return hex.DecodeString(hexStr)
}

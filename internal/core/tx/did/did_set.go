package did

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeDIDSet, func() tx.Transaction {
		return &DIDSet{BaseTx: *tx.NewBaseTx(tx.TypeDIDSet, "")}
	})
}

// DIDSet creates or updates a DID document.
type DIDSet struct {
	tx.BaseTx

	// Data is the public attestations (optional, hex-encoded)
	Data string `json:"Data,omitempty" xrpl:"Data,omitempty"`

	// DIDDocument is the DID document content (optional, hex-encoded)
	DIDDocument string `json:"DIDDocument,omitempty" xrpl:"DIDDocument,omitempty"`

	// URI is the URI for the DID document (optional, hex-encoded)
	URI string `json:"URI,omitempty" xrpl:"URI,omitempty"`
}

// NewDIDSet creates a new DIDSet transaction
func NewDIDSet(account string) *DIDSet {
	return &DIDSet{
		BaseTx: *tx.NewBaseTx(tx.TypeDIDSet, account),
	}
}

// TxType returns the transaction type
func (d *DIDSet) TxType() tx.Type {
	return tx.TypeDIDSet
}

// Validate validates the DIDSet transaction
// Reference: rippled DID.cpp DIDSet::preflight
func (d *DIDSet) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: DID.cpp line 51-52
	flags := d.GetFlags()
	if flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// Check if any field is present (even if empty)
	// Reference: DID.cpp line 57-59
	uriPresent := d.URI != "" || d.Common.HasField("URI")
	docPresent := d.DIDDocument != "" || d.Common.HasField("DIDDocument")
	dataPresent := d.Data != "" || d.Common.HasField("Data")

	// At least one field must be present
	if !uriPresent && !docPresent && !dataPresent {
		return ErrDIDEmpty
	}

	// If all present fields are empty, that's also an error
	// Reference: DID.cpp line 61-64
	if uriPresent && d.URI == "" &&
		docPresent && d.DIDDocument == "" &&
		dataPresent && d.Data == "" {
		return ErrDIDEmpty
	}

	// Check field lengths (after hex decode)
	// Reference: DID.cpp line 66-75
	if d.URI != "" {
		decoded, err := hex.DecodeString(d.URI)
		if err != nil {
			return ErrDIDInvalidHex
		}
		if len(decoded) > MaxDIDURILength {
			return ErrDIDURITooLong
		}
	}

	if d.DIDDocument != "" {
		decoded, err := hex.DecodeString(d.DIDDocument)
		if err != nil {
			return ErrDIDInvalidHex
		}
		if len(decoded) > MaxDIDDocumentLength {
			return ErrDIDDocTooLong
		}
	}

	if d.Data != "" {
		decoded, err := hex.DecodeString(d.Data)
		if err != nil {
			return ErrDIDInvalidHex
		}
		if len(decoded) > MaxDIDAttestationLength {
			return ErrDIDDataTooLong
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (d *DIDSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

// RequiredAmendments returns the amendments required for this transaction type
func (d *DIDSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDID}
}

// Apply applies a DIDSet transaction to the ledger state.
// Reference: rippled DID.cpp DIDSet::doApply
func (d *DIDSet) Apply(ctx *tx.ApplyContext) tx.Result {
	didKey := keylet.DID(ctx.AccountID)

	// Check if DID already exist
	existingData, err := ctx.View.Read(didKey)
	if err == nil && existingData != nil {
		// Update existing DID
		did, err := sle.ParseDID(existingData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Update fields based on what's provided in transaction
		if d.URI != "" {
			did.URI = d.URI
		} else if d.URI == "" && d.Common.HasField("URI") {
			did.URI = ""
		}

		if d.DIDDocument != "" {
			did.DIDDocument = d.DIDDocument
		} else if d.DIDDocument == "" && d.Common.HasField("DIDDocument") {
			did.DIDDocument = ""
		}

		if d.Data != "" {
			did.Data = d.Data
		} else if d.Data == "" && d.Common.HasField("Data") {
			did.Data = ""
		}

		// Check that at least one field remains after update
		if did.URI == "" && did.DIDDocument == "" && did.Data == "" {
			return tx.TecEMPTY_DID
		}

		// Serialize and update the DID - modification tracked automatically by ApplyStateTable
		updatedData, err := sle.SerializeDID(did, d.Account)
		if err != nil {
			return tx.TefINTERNAL
		}

		if err := ctx.View.Update(didKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}

		return tx.TesSUCCESS
	}

	// Create new DID
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	did := &sle.DIDData{
		Account:   ctx.AccountID,
		OwnerNode: 0,
	}

	if d.URI != "" {
		did.URI = d.URI
	}
	if d.DIDDocument != "" {
		did.DIDDocument = d.DIDDocument
	}
	if d.Data != "" {
		did.Data = d.Data
	}

	// Check that at least one field is set (only when fixEmptyDID is enabled)
	// Reference: rippled DID.cpp lines 163-169
	if ctx.Rules().Enabled(amendment.FeatureFixEmptyDID) &&
		did.URI == "" && did.DIDDocument == "" && did.Data == "" {
		return tx.TecEMPTY_DID
	}

	didData, err := sle.SerializeDID(did, d.Account)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert the DID into the ledger
	if err := ctx.View.Insert(didKey, didData); err != nil {
		return tx.TefINTERNAL
	}

	// Add to owner directory
	// Reference: rippled DID.cpp addSLE → dirInsert
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	sle.DirInsert(ctx.View, ownerDirKey, didKey.Key, nil)

	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

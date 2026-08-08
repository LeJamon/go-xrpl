package did

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func NewDIDSet(account string) *DIDSet {
	return &DIDSet{
		BaseTx: *tx.NewBaseTx(tx.TypeDIDSet, account),
	}
}

func (d *DIDSet) TxType() tx.Type {
	return tx.TypeDIDSet
}

// Reference: rippled DID.cpp DIDSet::preflight
// GetFlagsMask adopts the engine FlagsMasker seam. DIDSet defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (d *DIDSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (d *DIDSet) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Check if any field is present (even if empty)
	// Reference: DID.cpp line 57-59
	uriPresent := d.URI != "" || d.Common.HasField("URI")
	docPresent := d.DIDDocument != "" || d.Common.HasField("DIDDocument")
	dataPresent := d.Data != "" || d.Common.HasField("Data")

	// At least one field must be present
	if !uriPresent && !docPresent && !dataPresent {
		return errDIDEmpty
	}

	// If all present fields are empty, that's also an error
	// Reference: DID.cpp line 61-64
	if uriPresent && d.URI == "" &&
		docPresent && d.DIDDocument == "" &&
		dataPresent && d.Data == "" {
		return errDIDEmpty
	}

	for _, field := range []struct {
		value   string
		tooLong error
	}{
		{d.URI, errDIDURITooLong},
		{d.DIDDocument, errDIDDocTooLong},
		{d.Data, errDIDDataTooLong},
	} {
		if err := validateDIDField(field.value, field.tooLong); err != nil {
			return err
		}
	}

	return nil
}

func (d *DIDSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

func (d *DIDSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureDID}
}

// Reference: rippled DID.cpp DIDSet::doApply
func (d *DIDSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("did set apply",
		"account", d.Account,
		"uri", d.URI,
	)

	didKey := keylet.DID(ctx.AccountID)

	existingData, err := ctx.View.Read(didKey)
	if err != nil {
		return ctx.Internal("DIDSet.Read", err)
	}
	if existingData != nil {
		did, err := state.ParseDID(existingData)
		if err != nil {
			return ter.TefINTERNAL
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
			return ter.TecEMPTY_DID
		}

		// Serialize and update the DID - modification tracked automatically by ApplyStateTable
		updatedData, err := state.SerializeDID(did, d.Account)
		if err != nil {
			return ter.TefINTERNAL
		}

		if err := ctx.View.Update(didKey, updatedData); err != nil {
			return ter.TefINTERNAL
		}

		return ter.TesSUCCESS
	}

	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return ter.TecINSUFFICIENT_RESERVE
	}

	did := &state.DIDData{
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
		return ter.TecEMPTY_DID
	}

	// Add to owner directory first so sfOwnerNode records the actual page.
	// Reference: rippled DID.cpp:105-109 (dirInsert → sfOwnerNode = *page).
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, didKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("DIDSet.DirInsert", err)
	}
	did.OwnerNode = dirResult.Page

	didData, err := state.SerializeDID(did, d.Account)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Insert the DID into the ledger
	if err := ctx.View.Insert(didKey, didData); err != nil {
		return ter.TefINTERNAL
	}

	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}

func validateDIDField(value string, tooLong error) error {
	if value == "" {
		return nil
	}
	if len(value)%2 != 0 {
		value = "0" + value
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return errDIDInvalidHex
	}
	if len(decoded) > maxDIDFieldLength {
		return tooLong
	}
	return nil
}

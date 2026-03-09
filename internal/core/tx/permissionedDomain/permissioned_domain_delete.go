package permissioneddomain

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypePermissionedDomainDelete, func() tx.Transaction {
		return &PermissionedDomainDelete{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainDelete, "")}
	})
}

// PermissionedDomainDelete deletes a permissioned domain.
// Reference: rippled PermissionedDomainDelete.cpp
type PermissionedDomainDelete struct {
	tx.BaseTx

	// DomainID is the ID of the domain to delete (required)
	DomainID string `json:"DomainID" xrpl:"DomainID"`
}

// NewPermissionedDomainDelete creates a new PermissionedDomainDelete transaction
func NewPermissionedDomainDelete(account, domainID string) *PermissionedDomainDelete {
	return &PermissionedDomainDelete{
		BaseTx:   *tx.NewBaseTx(tx.TypePermissionedDomainDelete, account),
		DomainID: domainID,
	}
}

// TxType returns the transaction type
func (p *PermissionedDomainDelete) TxType() tx.Type {
	return tx.TypePermissionedDomainDelete
}

// Validate validates the PermissionedDomainDelete transaction
// Reference: rippled PermissionedDomainDelete.cpp preflight()
func (p *PermissionedDomainDelete) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: rippled PermissionedDomainDelete.cpp:36-40
	if p.Common.Flags != nil && *p.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// DomainID is required
	// Reference: rippled PermissionedDomainDelete.cpp:42-44
	if p.DomainID == "" {
		return ErrPermDomainIDRequired
	}

	// Validate DomainID is valid 256-bit hash and not zero
	domainBytes, err := hex.DecodeString(p.DomainID)
	if err != nil || len(domainBytes) != 32 {
		return errors.New("temMALFORMED: DomainID must be a valid 256-bit hash")
	}

	isZero := true
	for _, b := range domainBytes {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return ErrPermDomainDomainIDZero
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *PermissionedDomainDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

// RequiredAmendments returns the amendments required for this transaction type
func (p *PermissionedDomainDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionedDomains}
}

// Apply applies the PermissionedDomainDelete transaction to the ledger.
// Reference: rippled PermissionedDomainDelete.cpp preclaim() + doApply()
func (p *PermissionedDomainDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	domainBytes, err := hex.DecodeString(p.DomainID)
	if err != nil || len(domainBytes) != 32 {
		return tx.TemINVALID
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)
	domainKeylet := keylet.PermissionedDomainByID(domainID)

	// Preclaim: verify domain exists
	// Reference: rippled PermissionedDomainDelete.cpp preclaim() lines 50-55
	existingData, err := ctx.View.Read(domainKeylet)
	if err != nil || existingData == nil {
		return tx.TecNO_ENTRY
	}

	existing, err := sle.ParsePermissionedDomain(existingData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Preclaim: verify caller owns the domain
	// Reference: rippled PermissionedDomainDelete.cpp preclaim() lines 57-61
	if existing.Owner != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// Remove from owner directory
	// Reference: rippled PermissionedDomainDelete.cpp doApply()
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	if _, err := sle.DirRemove(ctx.View, ownerDirKey, existing.OwnerNode, domainKeylet.Key, false); err != nil {
		return tx.TefBAD_LEDGER
	}

	// Erase the domain from ledger
	if err := ctx.View.Erase(domainKeylet); err != nil {
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

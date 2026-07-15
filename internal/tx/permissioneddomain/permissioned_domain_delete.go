package permissioneddomain

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func (p *PermissionedDomainDelete) TxType() tx.Type {
	return tx.TypePermissionedDomainDelete
}

// Reference: rippled PermissionedDomainDelete.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. PermissionedDomainDelete
// defines no type-specific flags, so it uses the base universal mask, checked at
// preflight0.
func (p *PermissionedDomainDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (p *PermissionedDomainDelete) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// DomainID is required
	// Reference: rippled PermissionedDomainDelete.cpp:42-44
	if p.DomainID == "" {
		return ErrPermDomainIDRequired
	}

	// Validate DomainID is a valid non-zero 256-bit hash.
	if _, err := tx.ParseHash256NonZero(p.DomainID); err != nil {
		return err
	}

	return nil
}

func (p *PermissionedDomainDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

func (p *PermissionedDomainDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionedDomains}
}

// Preclaim runs PermissionedDomainDelete's ledger-aware checks: the domain must
// exist (tecNO_ENTRY) and the caller must own it (tecNO_PERMISSION). Extracting
// these from Apply makes them visible to the preclaim-only paths (TxQ admission,
// simulate), matching rippled where they live in PermissionedDomainDelete::preclaim.
// Reference: rippled PermissionedDomainDelete.cpp preclaim().
func (p *PermissionedDomainDelete) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	domainBytes, decErr := hex.DecodeString(p.DomainID)
	if decErr != nil || len(domainBytes) != 32 {
		return ter.TemINVALID
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)

	existingData, readErr := view.Read(keylet.PermissionedDomainByID(domainID))
	if readErr != nil || existingData == nil {
		return ter.TecNO_ENTRY
	}
	existing, parseErr := state.ParsePermissionedDomain(existingData)
	if parseErr != nil {
		return ter.TefINTERNAL
	}
	accountID, acctErr := state.DecodeAccountID(p.Account)
	if acctErr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if existing.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	return ter.TesSUCCESS
}

// Reference: rippled PermissionedDomainDelete.cpp doApply()
func (p *PermissionedDomainDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("permissioned domain delete apply",
		"account", p.Account,
		"domainID", p.DomainID,
	)

	domainBytes, err := hex.DecodeString(p.DomainID)
	if err != nil || len(domainBytes) != 32 {
		return ter.TemINVALID
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)
	domainKeylet := keylet.PermissionedDomainByID(domainID)

	// Preclaim: verify domain exists
	// Reference: rippled PermissionedDomainDelete.cpp preclaim() lines 50-55
	existingData, err := ctx.View.Read(domainKeylet)
	if err != nil || existingData == nil {
		ctx.Log.Warn("permissioned domain delete: domain not found",
			"domainID", p.DomainID,
		)
		return ter.TecNO_ENTRY
	}

	existing, err := state.ParsePermissionedDomain(existingData)
	if err != nil {
		ctx.Log.Error("permissioned domain delete: failed to parse domain", "error", err)
		return ter.TefINTERNAL
	}

	// Remove from owner directory
	// Reference: rippled PermissionedDomainDelete.cpp doApply()
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	if _, err := state.DirRemove(ctx.View, ownerDirKey, existing.OwnerNode, domainKeylet.Key, false); err != nil {
		ctx.Log.Error("permissioned domain delete: failed to remove from directory", "error", err)
		return ter.TefBAD_LEDGER
	}

	if err := ctx.View.Erase(domainKeylet); err != nil {
		ctx.Log.Error("permissioned domain delete: failed to erase domain", "error", err)
		return ter.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return ter.TesSUCCESS
}

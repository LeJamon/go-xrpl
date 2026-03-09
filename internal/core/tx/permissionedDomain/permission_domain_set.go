package permissioneddomain

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypePermissionedDomainSet, func() tx.Transaction {
		return &PermissionedDomainSet{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainSet, "")}
	})
}

// PermissionedDomainSet creates or modifies a permissioned domain.
// Reference: rippled PermissionedDomainSet.cpp
type PermissionedDomainSet struct {
	tx.BaseTx

	// DomainID is the ID of the domain (optional, omit for creation)
	DomainID string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// AcceptedCredentials defines the credentials accepted by this domain (required)
	AcceptedCredentials []AcceptedCredential `json:"AcceptedCredentials" xrpl:"AcceptedCredentials,omitempty"`
}

// NewPermissionedDomainSet creates a new PermissionedDomainSet transaction
func NewPermissionedDomainSet(account string) *PermissionedDomainSet {
	return &PermissionedDomainSet{
		BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainSet, account),
	}
}

// TxType returns the transaction type
func (p *PermissionedDomainSet) TxType() tx.Type {
	return tx.TypePermissionedDomainSet
}

// Validate validates the PermissionedDomainSet transaction
// Reference: rippled PermissionedDomainSet.cpp preflight()
func (p *PermissionedDomainSet) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: rippled PermissionedDomainSet.cpp:41-45
	if p.Common.Flags != nil && *p.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// If DomainID is present, it must not be zero
	// Reference: rippled PermissionedDomainSet.cpp:54-56
	if p.DomainID != "" {
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
	}

	// Validate AcceptedCredentials array
	// Reference: rippled PermissionedDomainSet.cpp checkArray()
	if len(p.AcceptedCredentials) == 0 {
		return ErrPermDomainEmptyCredentials
	}
	if len(p.AcceptedCredentials) > MaxPermissionedDomainCredentials {
		return ErrPermDomainTooManyCredentials
	}

	// Check for duplicates and validate each credential
	seen := make(map[string]bool)
	for _, cred := range p.AcceptedCredentials {
		data := cred.Credential

		if data.Issuer == "" {
			return ErrPermDomainNoIssuer
		}

		if data.CredentialType == "" {
			return ErrPermDomainEmptyCredType
		}

		credTypeBytes, err := hex.DecodeString(data.CredentialType)
		if err != nil {
			return errors.New("temMALFORMED: CredentialType must be valid hex string")
		}
		if len(credTypeBytes) == 0 {
			return ErrPermDomainEmptyCredType
		}
		if len(credTypeBytes) > credential.MaxCredentialTypeLength {
			return ErrPermDomainCredTypeTooLong
		}

		key := data.Issuer + ":" + data.CredentialType
		if seen[key] {
			return ErrPermDomainDuplicateCredential
		}
		seen[key] = true
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *PermissionedDomainSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

// AddAcceptedCredential adds an accepted credential
func (p *PermissionedDomainSet) AddAcceptedCredential(issuer, credentialType string) {
	p.AcceptedCredentials = append(p.AcceptedCredentials, AcceptedCredential{
		Credential: AcceptedCredentialData{
			Issuer:         issuer,
			CredentialType: credentialType,
		},
	})
}

// RequiredAmendments returns the amendments required for this transaction type
func (p *PermissionedDomainSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionedDomains, amendment.FeatureCredentials}
}

// Apply applies the PermissionedDomainSet transaction to the ledger.
// Reference: rippled PermissionedDomainSet.cpp preclaim() + doApply()
func (p *PermissionedDomainSet) Apply(ctx *tx.ApplyContext) tx.Result {
	// Preclaim: verify each issuer account exists
	// Reference: rippled PermissionedDomainSet.cpp preclaim() lines 70-85
	for _, cred := range p.AcceptedCredentials {
		issuerID, err := state.DecodeAccountID(cred.Credential.Issuer)
		if err != nil {
			return tx.TemINVALID
		}
		issuerData, err := ctx.View.Read(keylet.Account(issuerID))
		if err != nil || issuerData == nil {
			return tx.TecNO_ISSUER
		}
	}

	// Sort credentials by (Issuer bytes, CredentialType bytes) ascending
	// Reference: rippled PermissionedDomainSet.cpp makeSorted()
	sorted, err := sortedCredentials(p.AcceptedCredentials)
	if err != nil {
		return tx.TemINVALID
	}

	if p.DomainID != "" {
		// UPDATE existing domain
		return p.applyUpdate(ctx, sorted)
	}

	// CREATE new domain
	return p.applyCreate(ctx, sorted)
}

// applyCreate handles domain creation.
func (p *PermissionedDomainSet) applyCreate(ctx *tx.ApplyContext, sorted []state.PermissionedDomainCredential) tx.Result {
	// Check reserve
	// Reference: rippled PermissionedDomainSet.cpp doApply() lines 102-106
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Compute domain keylet from account + transaction sequence
	// Reference: rippled PermissionedDomainSet.cpp doApply() — uses ctx_.tx[sfSequence]
	txSeq := p.Common.SeqProxy()
	domainKeylet := keylet.PermissionedDomain(ctx.AccountID, txSeq)

	pd := &state.PermissionedDomainData{
		Owner:               ctx.AccountID,
		Sequence:            txSeq,
		OwnerNode:           0,
		AcceptedCredentials: sorted,
	}

	pdData, err := state.SerializePermissionedDomain(pd, p.Account)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(domainKeylet, pdData); err != nil {
		return tx.TefINTERNAL
	}

	// Add to owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	result, err := state.DirInsert(ctx.View, ownerDirKey, domainKeylet.Key, nil)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Update OwnerNode in the stored entry
	pd.OwnerNode = result.Page
	pdData, err = state.SerializePermissionedDomain(pd, p.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(domainKeylet, pdData); err != nil {
		return tx.TefINTERNAL
	}

	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// applyUpdate handles domain update.
func (p *PermissionedDomainSet) applyUpdate(ctx *tx.ApplyContext, sorted []state.PermissionedDomainCredential) tx.Result {
	domainBytes, err := hex.DecodeString(p.DomainID)
	if err != nil || len(domainBytes) != 32 {
		return tx.TemINVALID
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)
	domainKeylet := keylet.PermissionedDomainByID(domainID)

	existingData, err := ctx.View.Read(domainKeylet)
	if err != nil || existingData == nil {
		return tx.TecNO_ENTRY
	}

	existing, err := state.ParsePermissionedDomain(existingData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Verify caller is the owner
	// Reference: rippled PermissionedDomainSet.cpp preclaim() lines 88-95
	if existing.Owner != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// Replace credentials
	existing.AcceptedCredentials = sorted

	ownerAddress := p.Account
	updatedData, err := state.SerializePermissionedDomain(existing, ownerAddress)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(domainKeylet, updatedData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// sortedCredentials converts AcceptedCredential slice to sorted PermissionedDomainCredential slice.
// Sort order: (Issuer bytes, CredentialType bytes) ascending — matches rippled's makeSorted().
func sortedCredentials(creds []AcceptedCredential) ([]state.PermissionedDomainCredential, error) {
	type entry struct {
		issuer   [20]byte
		credType []byte
	}

	entries := make([]entry, 0, len(creds))
	for _, c := range creds {
		issuerID, err := state.DecodeAccountID(c.Credential.Issuer)
		if err != nil {
			return nil, err
		}
		credTypeBytes, err := hex.DecodeString(c.Credential.CredentialType)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{issuer: issuerID, credType: credTypeBytes})
	}

	sort.Slice(entries, func(i, j int) bool {
		cmp := bytes.Compare(entries[i].issuer[:], entries[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(entries[i].credType, entries[j].credType) < 0
	})

	result := make([]state.PermissionedDomainCredential, len(entries))
	for i, e := range entries {
		result[i] = state.PermissionedDomainCredential{
			Issuer:         e.issuer,
			CredentialType: e.credType,
		}
	}
	return result, nil
}

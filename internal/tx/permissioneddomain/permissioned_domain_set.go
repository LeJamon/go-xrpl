package permissioneddomain

import (
	"bytes"
	"encoding/hex"
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func (p *PermissionedDomainSet) TxType() tx.Type {
	return tx.TypePermissionedDomainSet
}

// GetFlagsMask adopts the engine FlagsMasker seam. PermissionedDomainSet defines
// no type-specific flags, so the base universal mask is checked at preflight0
// (before the credentials array checks), matching rippled's default
// Transactor::getFlagsMask.
func (p *PermissionedDomainSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled PermissionedDomainSet.cpp preflight()
func (p *PermissionedDomainSet) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// credentials::checkArray runs FIRST (rippled preflight order): array bounds,
	// then per-credential zero-issuer / credential-type / duplicate checks. Only
	// after it succeeds is the DomainID present-and-zero check evaluated.
	// Reference: rippled PermissionedDomainSet.cpp preflight() -> CredentialHelpers.cpp checkArray().
	if len(p.AcceptedCredentials) == 0 {
		return ErrPermDomainEmptyCredentials
	}
	if len(p.AcceptedCredentials) > MaxPermissionedDomainCredentials {
		return ErrPermDomainTooManyCredentials
	}

	seen := make(map[string]bool)
	for _, cred := range p.AcceptedCredentials {
		data := cred.Credential

		if data.Issuer == "" {
			return ErrPermDomainNoIssuer
		}
		// A zero (all-0x00) issuer AccountID is temINVALID_ACCOUNT_ID, and it
		// precedes the credential-type rules. A zero AccountID decodes to the
		// non-empty classic address, so the "" check above cannot catch it.
		// rippled rejects only the zero account (`!issuer`); a well-formed
		// non-zero account decodes here and is used to key the duplicate set.
		// Reference: CredentialHelpers.cpp checkArray -> `if (!issuer) return temINVALID_ACCOUNT_ID`.
		issuerID, decErr := state.DecodeAccountID(data.Issuer)
		if decErr == nil && issuerID == ([20]byte{}) {
			return ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "credential issuer account is invalid")
		}

		if data.CredentialType == "" {
			return ErrPermDomainEmptyCredType
		}

		credTypeBytes, err := hex.DecodeString(data.CredentialType)
		if err != nil {
			return ter.Errorf(ter.TemMALFORMED, "CredentialType must be valid hex string")
		}
		if len(credTypeBytes) == 0 {
			return ErrPermDomainEmptyCredType
		}
		if len(credTypeBytes) > credential.MaxCredentialTypeLength {
			return ErrPermDomainCredTypeTooLong
		}

		// Duplicates are detected on the decoded (issuer, credentialType) bytes so
		// two entries differing only in credential-type hex case still collide,
		// mirroring rippled's sha512Half(issuer, ct) dedup key. A non-decodable
		// placeholder issuer falls back to its raw string.
		var issuerKey string
		if decErr == nil {
			issuerKey = string(issuerID[:])
		} else {
			issuerKey = data.Issuer
		}
		key := issuerKey + "\x00" + string(credTypeBytes)
		if seen[key] {
			return ErrPermDomainDuplicateCredential
		}
		seen[key] = true
	}

	// If DomainID is present, it must be a valid non-zero 256-bit hash. Checked
	// after the credentials array. Reference: rippled PermissionedDomainSet.cpp preflight().
	if p.DomainID != "" {
		if _, err := tx.ParseHash256NonZero(p.DomainID); err != nil {
			return err
		}
	}

	return nil
}

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

func (p *PermissionedDomainSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePermissionedDomains, amendment.FeatureCredentials}
}

// Reference: rippled PermissionedDomainSet.cpp preclaim() + doApply()
// Preclaim runs the ledger-aware checks in rippled PermissionedDomainSet::preclaim
// order: every AcceptedCredentials issuer must exist (tecNO_ISSUER); when a
// DomainID is given, the domain must exist (tecNO_ENTRY) and be owned by the
// sender (tecNO_PERMISSION). The mutation stays in Apply (rippled doApply).
func (p *PermissionedDomainSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(p.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	for _, cred := range p.AcceptedCredentials {
		issuerID, derr := state.DecodeAccountID(cred.Credential.Issuer)
		if derr != nil {
			return ter.TemINVALID
		}
		if exists, _ := view.Exists(keylet.Account(issuerID)); !exists {
			return ter.TecNO_ISSUER
		}
	}

	if p.DomainID != "" {
		domainBytes, derr := hex.DecodeString(p.DomainID)
		if derr != nil || len(domainBytes) != 32 {
			return ter.TemINVALID
		}
		var domainID [32]byte
		copy(domainID[:], domainBytes)
		data, rerr := view.Read(keylet.PermissionedDomainByID(domainID))
		if rerr != nil || data == nil {
			return ter.TecNO_ENTRY
		}
		existing, perr := state.ParsePermissionedDomain(data)
		if perr != nil {
			return ter.TefINTERNAL
		}
		if existing.Owner != accountID {
			return ter.TecNO_PERMISSION
		}
	}
	return ter.TesSUCCESS
}

func (p *PermissionedDomainSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("permissioned domain set apply",
		"account", p.Account,
		"domainID", p.DomainID,
		"credentialCount", len(p.AcceptedCredentials),
	)

	// Sort credentials by (Issuer bytes, CredentialType bytes) ascending
	// Reference: rippled PermissionedDomainSet.cpp makeSorted()
	sorted, err := sortedCredentials(p.AcceptedCredentials)
	if err != nil {
		return ter.TemINVALID
	}

	if p.DomainID != "" {
		// UPDATE existing domain
		return p.applyUpdate(ctx, sorted)
	}

	// CREATE new domain
	return p.applyCreate(ctx, sorted)
}

// applyCreate handles domain creation.
func (p *PermissionedDomainSet) applyCreate(ctx *tx.ApplyContext, sorted []state.PermissionedDomainCredential) ter.Result {
	// Check reserve
	// Reference: rippled PermissionedDomainSet.cpp doApply() lines 102-106
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		ctx.Log.Warn("permissioned domain set: insufficient reserve",
			"balance", ctx.Account.Balance,
			"reserve", reserve,
		)
		return ter.TecINSUFFICIENT_RESERVE
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
		ctx.Log.Error("permissioned domain set: failed to serialize domain", "error", err)
		return ter.TefINTERNAL
	}

	if err := ctx.View.Insert(domainKeylet, pdData); err != nil {
		ctx.Log.Error("permissioned domain set: failed to insert domain", "error", err)
		return ter.TefINTERNAL
	}

	// Add to owner directory. The describe callback stamps sfOwner on a freshly
	// created owner-dir root/page (rippled describeOwnerDir, PermissionedDomainSet
	// .cpp:138-139); without it the SLE bytes (and CreatedNode NewFields) diverge.
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	result, err := state.DirInsert(ctx.View, ownerDirKey, domainKeylet.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		ctx.Log.Error("permissioned domain set: directory insert failed", "error", err)
		return ter.TecDIR_FULL
	}

	// Update OwnerNode in the stored entry
	pd.OwnerNode = result.Page
	pdData, err = state.SerializePermissionedDomain(pd, p.Account)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(domainKeylet, pdData); err != nil {
		return ter.TefINTERNAL
	}

	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}

// applyUpdate handles domain update.
func (p *PermissionedDomainSet) applyUpdate(ctx *tx.ApplyContext, sorted []state.PermissionedDomainCredential) ter.Result {
	domainBytes, err := hex.DecodeString(p.DomainID)
	if err != nil || len(domainBytes) != 32 {
		return ter.TemINVALID
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)
	domainKeylet := keylet.PermissionedDomainByID(domainID)

	existingData, err := ctx.View.Read(domainKeylet)
	if err != nil || existingData == nil {
		ctx.Log.Warn("permissioned domain set: domain not found",
			"domainID", p.DomainID,
		)
		return ter.TecNO_ENTRY
	}

	existing, err := state.ParsePermissionedDomain(existingData)
	if err != nil {
		ctx.Log.Error("permissioned domain set: failed to parse domain", "error", err)
		return ter.TefINTERNAL
	}

	// Verify caller is the owner
	// Reference: rippled PermissionedDomainSet.cpp preclaim() lines 88-95
	if existing.Owner != ctx.AccountID {
		ctx.Log.Warn("permissioned domain set: caller is not owner")
		return ter.TecNO_PERMISSION
	}

	// Replace credentials
	existing.AcceptedCredentials = sorted

	ownerAddress := p.Account
	updatedData, err := state.SerializePermissionedDomain(existing, ownerAddress)
	if err != nil {
		return ter.TefINTERNAL
	}

	if err := ctx.View.Update(domainKeylet, updatedData); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
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

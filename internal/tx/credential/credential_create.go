package credential

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// CredentialCreate creates a credential.
type CredentialCreate struct {
	tx.BaseTx

	// Subject is the account the credential is about (required)
	Subject string `json:"Subject" xrpl:"Subject"`

	// CredentialType is the type of credential (required, hex-encoded)
	CredentialType string `json:"CredentialType" xrpl:"CredentialType"`

	// Expiration is when the credential expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`

	// URI is the URI for credential details (optional)
	URI string `json:"URI,omitempty" xrpl:"URI,omitempty"`
}

// NewCredentialCreate creates a new CredentialCreate transaction
func NewCredentialCreate(account, subject, credentialType string) *CredentialCreate {
	return &CredentialCreate{
		BaseTx:         *tx.NewBaseTx(tx.TypeCredentialCreate, account),
		Subject:        subject,
		CredentialType: credentialType,
	}
}

func (c *CredentialCreate) TxType() tx.Type {
	return tx.TypeCredentialCreate
}

// GetFlagsMask reports the invalid-flag mask. rippled's
// CredentialCreate::getFlagsMask is `fixInvalidTxFlags ? tfUniversalMask : 0`,
// so with the amendment active any flag is rejected temINVALID_FLAG at
// preflight0 (before the field checks and signature verification); with it off
// the mask is zero and all flags pass.
func (c *CredentialCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.Enabled(amendment.FeatureFixInvalidTxFlags) {
		return tx.TfUniversalMask
	}
	return 0
}

// Reference: rippled Credentials.cpp CredentialCreate::preflight()
func (c *CredentialCreate) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Subject is required and must not be the zero account
	// Reference: rippled Credentials.cpp:73-77
	if c.Subject == "" {
		return ErrCredentialNoSubject
	}
	if subjectID, err := state.DecodeAccountID(c.Subject); err == nil {
		var zeroAccount [20]byte
		if subjectID == zeroAccount {
			return ErrCredentialNoSubject
		}
	}

	// Validate URI field length (optional but if present must be valid)
	// Reference: rippled Credentials.cpp:79-84
	// HasField("URI") detects binary-parsed "URI present but empty" vs "URI absent".
	if c.URI != "" || c.HasField("URI") {
		decoded, err := hex.DecodeString(c.URI)
		if err != nil {
			return ter.Errorf(ter.TemMALFORMED, "URI must be valid hex string")
		}
		if len(decoded) == 0 {
			return ErrCredentialURIEmpty
		}
		if len(decoded) > MaxCredentialURILength {
			return ErrCredentialURITooLong
		}
	}

	// Validate CredentialType field (required, max 64 bytes)
	// Reference: rippled Credentials.cpp:86-92
	if c.CredentialType == "" {
		return ErrCredentialTypeEmpty
	}
	decoded, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.Errorf(ter.TemMALFORMED, "CredentialType must be valid hex string")
	}
	if len(decoded) == 0 {
		return ErrCredentialTypeEmpty
	}
	if len(decoded) > MaxCredentialTypeLength {
		return ErrCredentialTypeTooLong
	}

	return nil
}

func (c *CredentialCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CredentialCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Reference: rippled Credentials.cpp CredentialCreate::doApply()
// Preclaim verifies the subject account exists (tecNO_TARGET) and no credential
// of this (subject, issuer, type) already exists (tecDUPLICATE), matching rippled
// CredentialCreate::preclaim. The Expiration-in-the-past check (tecEXPIRED) and
// the reserve check stay in Apply, mirroring rippled CredentialCreate::doApply.
func (c *CredentialCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	subjectID, err := state.DecodeAccountID(c.Subject)
	if err != nil {
		return ter.TecNO_TARGET
	}
	issuerID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}
	if exists, _ := view.Exists(keylet.Account(subjectID)); !exists {
		return ter.TecNO_TARGET
	}
	if exists, _ := view.Exists(keylet.Credential(subjectID, issuerID, credTypeBytes)); exists {
		return ter.TecDUPLICATE
	}
	return ter.TesSUCCESS
}

func (c *CredentialCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("credential create apply",
		"issuer", c.Account,
		"subject", c.Subject,
		"credentialType", c.CredentialType,
	)

	if c.Subject == "" || c.CredentialType == "" {
		return ter.TemINVALID
	}

	subjectID, err := state.DecodeAccountID(c.Subject)
	if err != nil {
		return ter.TecNO_TARGET
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	// where issuer = ctx.AccountID (the transaction sender)
	credKeylet := keylet.Credential(subjectID, ctx.AccountID, credTypeBytes)

	// Check expiration (if set, must be in the future)
	if c.Expiration != nil {
		closeTime := ctx.Config.ParentCloseTime
		if closeTime > *c.Expiration {
			return ter.TecEXPIRED
		}
	}

	// Check reserve for issuer (ctx.Account) using the prior balance (before the
	// actual fee was deducted), matching rippled's mPriorBalance comparison.
	if result := ctx.CheckReserveWithFee(ctx.Account.OwnerCount + 1); result != ter.TesSUCCESS {
		return result
	}

	cred := &CredentialEntry{
		Subject:        subjectID,
		Issuer:         ctx.AccountID,
		CredentialType: credTypeBytes,
	}

	if c.Expiration != nil {
		cred.Expiration = c.Expiration
	}

	if c.URI != "" {
		uriBytes, err := hex.DecodeString(c.URI)
		if err == nil {
			cred.URI = uriBytes
		}
	}

	// Self-issue: if subject == issuer, auto-accept
	if subjectID == ctx.AccountID {
		cred.SetAccepted()
	}

	// Insert into issuer's owner directory
	issuerDirKey := keylet.OwnerDir(ctx.AccountID)
	issuerDirResult, err := state.DirInsert(ctx.View, issuerDirKey, credKeylet.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ter.TefINTERNAL
	}
	cred.IssuerNode = issuerDirResult.Page

	// Insert into subject's owner directory (if different from issuer)
	if subjectID != ctx.AccountID {
		subjectDirKey := keylet.OwnerDir(subjectID)
		subjectDirResult, err := state.DirInsert(ctx.View, subjectDirKey, credKeylet.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = subjectID
		})
		if err != nil {
			if errors.Is(err, state.ErrDirFull) {
				return ter.TecDIR_FULL
			}
			return ter.TefINTERNAL
		}
		cred.SubjectNode = subjectDirResult.Page
		cred.HasSubjectNode = true
	}

	// Serialize the credential entry
	credData, err := serializeCredentialEntry(cred)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Insert the credential
	if err := ctx.View.Insert(credKeylet, credData); err != nil {
		return ter.TefINTERNAL
	}

	// Increase issuer's owner count
	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}

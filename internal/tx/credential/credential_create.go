package credential

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeCredentialCreate, func() tx.Transaction {
		return &CredentialCreate{BaseTx: *tx.NewBaseTx(tx.TypeCredentialCreate, "")}
	})
}

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

// TxType returns the transaction type
func (c *CredentialCreate) TxType() tx.Type {
	return tx.TypeCredentialCreate
}

// Validate validates the CredentialCreate transaction
// Reference: rippled Credentials.cpp CredentialCreate::preflight()
// Note: The fixInvalidTxFlags-gated flag check is done in Apply() because
// Validate() has no access to amendment rules.
func (c *CredentialCreate) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Flag check is deferred to Apply() where amendment rules are available.
	// Reference: rippled Credentials.cpp:66-71 — gated behind fixInvalidTxFlags.

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
			return tx.Errorf(tx.TemMALFORMED, "URI must be valid hex string")
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
		return tx.Errorf(tx.TemMALFORMED, "CredentialType must be valid hex string")
	}
	if len(decoded) == 0 {
		return ErrCredentialTypeEmpty
	}
	if len(decoded) > MaxCredentialTypeLength {
		return ErrCredentialTypeTooLong
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (c *CredentialCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments returns the amendments required for this transaction type
func (c *CredentialCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Apply applies the CredentialCreate transaction to ledger state.
// Reference: rippled Credentials.cpp CredentialCreate::doApply()
func (c *CredentialCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check for invalid flags, gated behind fixInvalidTxFlags
	// Reference: rippled Credentials.cpp:66-71
	if ctx.Rules().Enabled(amendment.FeatureFixInvalidTxFlags) {
		if c.GetFlags()&tx.TfUniversalMask != 0 {
			return tx.TemINVALID_FLAG
		}
	}

	if c.Subject == "" || c.CredentialType == "" {
		return tx.TemINVALID
	}

	subjectID, err := state.DecodeAccountID(c.Subject)
	if err != nil {
		return tx.TecNO_TARGET
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return tx.TemINVALID
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	// where issuer = ctx.AccountID (the transaction sender)
	credKeylet := keylet.Credential(subjectID, ctx.AccountID, credTypeBytes)

	// Preclaim check: verify subject account exists
	subjectAccountKeylet := keylet.Account(subjectID)
	subjectExists, err := ctx.View.Exists(subjectAccountKeylet)
	if err != nil || !subjectExists {
		return tx.TecNO_TARGET
	}

	// Preclaim check: verify credential doesn't already exist
	exists, err := ctx.View.Exists(credKeylet)
	if err != nil {
		return tx.TefINTERNAL
	}
	if exists {
		return tx.TecDUPLICATE
	}

	// Check expiration (if set, must be in the future)
	if c.Expiration != nil {
		closeTime := ctx.Config.ParentCloseTime
		if closeTime > *c.Expiration {
			return tx.TecEXPIRED
		}
	}

	// Check reserve for issuer (ctx.Account)
	// Use prior balance (before fee deduction) to match rippled's behavior
	// Reference: rippled Credentials.cpp line 154: if (mPriorBalance < reserve)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Create the credential entry
	cred := &CredentialEntry{
		Subject:        subjectID,
		Issuer:         ctx.AccountID,
		CredentialType: credTypeBytes,
	}

	// Set expiration if provided
	if c.Expiration != nil {
		cred.Expiration = c.Expiration
	}

	// Set URI if provided
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
	issuerDirResult, err := state.DirInsert(ctx.View, issuerDirKey, credKeylet.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	}, ctx.Rules())
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return tx.TecDIR_FULL
		}
		return tx.TefINTERNAL
	}
	cred.IssuerNode = issuerDirResult.Page

	// Insert into subject's owner directory (if different from issuer)
	if subjectID != ctx.AccountID {
		subjectDirKey := keylet.OwnerDir(subjectID)
		subjectDirResult, err := state.DirInsert(ctx.View, subjectDirKey, credKeylet.Key, func(dir *state.DirectoryNode) {
			dir.Owner = subjectID
		}, ctx.Rules())
		if err != nil {
			if errors.Is(err, state.ErrDirFull) {
				return tx.TecDIR_FULL
			}
			return tx.TefINTERNAL
		}
		cred.SubjectNode = subjectDirResult.Page
	}

	// Serialize the credential entry
	credData, err := serializeCredentialEntry(cred)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert the credential
	if err := ctx.View.Insert(credKeylet, credData); err != nil {
		return tx.TefINTERNAL
	}

	// Increase issuer's owner count
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

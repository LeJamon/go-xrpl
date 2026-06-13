package credential

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// CredentialDelete deletes a credential.
type CredentialDelete struct {
	tx.BaseTx

	// Subject is the account the credential is about (optional, defaults to Account)
	Subject string `json:"Subject,omitempty" xrpl:"Subject,omitempty"`

	// Issuer is the account that issued the credential (optional, defaults to Account)
	Issuer string `json:"Issuer,omitempty" xrpl:"Issuer,omitempty"`

	// CredentialType is the type of credential (required, hex-encoded)
	CredentialType string `json:"CredentialType" xrpl:"CredentialType"`
}

// NewCredentialDelete creates a new CredentialDelete transaction
func NewCredentialDelete(account, credentialType string) *CredentialDelete {
	return &CredentialDelete{
		BaseTx:         *tx.NewBaseTx(tx.TypeCredentialDelete, account),
		CredentialType: credentialType,
	}
}

func (c *CredentialDelete) TxType() tx.Type {
	return tx.TypeCredentialDelete
}

// Reference: rippled Credentials.cpp CredentialDelete::preflight()
// Note: The fixInvalidTxFlags-gated flag check is done in Apply() because
// Validate() has no access to amendment rules.
func (c *CredentialDelete) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Flag check is deferred to Apply() where amendment rules are available.
	// Reference: rippled Credentials.cpp:217-222 — gated behind fixInvalidTxFlags.

	// At least one of Subject or Issuer must be present
	// Reference: rippled Credentials.cpp:224-233
	if c.Subject == "" && c.Issuer == "" {
		// Check PresentFields: if both are absent from the parsed blob, that's malformed.
		// If either was present (even with value ""), it was explicitly set.
		if !c.HasField("Subject") && !c.HasField("Issuer") {
			return ErrCredentialNoFields
		}
	}

	// If present, Subject and Issuer must not be zero accounts
	// Reference: rippled Credentials.cpp:235-241
	if c.Subject != "" {
		if subjectID, err := state.DecodeAccountID(c.Subject); err == nil {
			var zeroAccount [20]byte
			if subjectID == zeroAccount {
				return ErrCredentialZeroAccount
			}
		}
	}
	if c.Issuer != "" {
		if issuerID, err := state.DecodeAccountID(c.Issuer); err == nil {
			var zeroAccount [20]byte
			if issuerID == zeroAccount {
				return ErrCredentialZeroAccount
			}
		}
	}

	// Validate CredentialType field (required, max 64 bytes)
	// Reference: rippled Credentials.cpp:243-249
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

func (c *CredentialDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CredentialDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Reference: rippled Credentials.cpp CredentialDelete::doApply()
func (c *CredentialDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check for invalid flags, gated behind fixInvalidTxFlags
	// Reference: rippled Credentials.cpp:217-222
	if ctx.Rules().Enabled(amendment.FeatureFixInvalidTxFlags) {
		if c.GetFlags()&tx.TfUniversalMask != 0 {
			return tx.TemINVALID_FLAG
		}
	}

	ctx.Log.Trace("credential delete apply",
		"account", c.Account,
		"subject", c.Subject,
		"issuer", c.Issuer,
		"credentialType", c.CredentialType,
	)

	if c.CredentialType == "" {
		return tx.TemINVALID
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return tx.TemINVALID
	}

	// Default subject/issuer to Account if not specified
	var subjectID, issuerID [20]byte

	if c.Subject != "" {
		subjectID, err = state.DecodeAccountID(c.Subject)
		if err != nil {
			return tx.TecNO_TARGET
		}
	} else {
		subjectID = ctx.AccountID
	}

	if c.Issuer != "" {
		issuerID, err = state.DecodeAccountID(c.Issuer)
		if err != nil {
			return tx.TecNO_TARGET
		}
	} else {
		issuerID = ctx.AccountID
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	credKeylet := keylet.Credential(subjectID, issuerID, credTypeBytes)

	// Preclaim check: verify credential exists
	credData, err := ctx.View.Read(credKeylet)
	if err != nil || credData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse the credential entry
	cred, err := ParseCredentialEntry(credData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Permission check: only subject or issuer can delete non-expired credentials
	// Anyone can delete expired credentials
	closeTime := ctx.Config.ParentCloseTime
	isExpired := CheckCredentialExpired(cred, closeTime)
	isSubject := subjectID == ctx.AccountID
	isIssuer := issuerID == ctx.AccountID

	if !isSubject && !isIssuer && !isExpired {
		ctx.Log.Trace("credential delete: can't delete non-expired credential")
		return tx.TecNO_PERMISSION
	}

	if result := DeleteSLE(ctx, credKeylet, cred); result != tx.TesSUCCESS {
		return result
	}

	// DeleteSLE adjusts owner counts through the view; when the sender owns the
	// credential, resync ctx.Account so the engine's writeback keeps the change.
	if isSubject || isIssuer {
		ctx.SyncSenderOwnerCount()
	}

	return tx.TesSUCCESS
}

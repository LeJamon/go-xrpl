package credential

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

// GetFlagsMask reports the invalid-flag mask. rippled's
// CredentialDelete::getFlagsMask is `fixInvalidTxFlags ? tfUniversalMask : 0`,
// so with the amendment active any flag is rejected temINVALID_FLAG at
// preflight0 (before the Subject/Issuer/CredentialType checks and signature
// verification); with it off the mask is zero and all flags pass.
func (c *CredentialDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.Enabled(amendment.FeatureFixInvalidTxFlags) {
		return tx.TfUniversalMask
	}
	return 0
}

// Reference: rippled Credentials.cpp CredentialDelete::preflight()
func (c *CredentialDelete) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// At least one of Subject or Issuer must be present
	// Reference: rippled Credentials.cpp:224-233
	if c.Subject == "" && c.Issuer == "" {
		// Check PresentFields: if both are absent from the parsed blob, that's malformed.
		// If either was present (even with value ""), it was explicitly set.
		if !c.HasField("Subject") && !c.HasField("Issuer") {
			return ErrCredentialNoFields
		}
	}

	// If present, Subject and Issuer must not be zero accounts. A present but
	// zero-length STAccount deserializes to "", which rippled sees as a present
	// account that isZero(), so it must be rejected the same as a present 20-byte
	// zero account — not treated as absent.
	// Reference: rippled Credentials.cpp — (subject && subject->isZero()) etc.
	if c.HasField("Subject") && c.Subject == "" {
		return ErrCredentialZeroAccount
	}
	if c.Subject != "" {
		if subjectID, err := state.DecodeAccountID(c.Subject); err == nil {
			var zeroAccount [20]byte
			if subjectID == zeroAccount {
				return ErrCredentialZeroAccount
			}
		}
	}
	if c.HasField("Issuer") && c.Issuer == "" {
		return ErrCredentialZeroAccount
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

func (c *CredentialDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CredentialDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Reference: rippled Credentials.cpp CredentialDelete::doApply()
// Preclaim verifies the target credential exists (tecNO_ENTRY), matching rippled
// CredentialDelete::preclaim. Subject and Issuer both default to Account. The
// subject/issuer/expired permission gate (tecNO_PERMISSION) stays in Apply,
// mirroring rippled CredentialDelete::doApply.
func (c *CredentialDelete) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}
	accountID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	subjectID := accountID
	if c.Subject != "" {
		if subjectID, err = state.DecodeAccountID(c.Subject); err != nil {
			return ter.TecNO_TARGET
		}
	}
	issuerID := accountID
	if c.Issuer != "" {
		if issuerID, err = state.DecodeAccountID(c.Issuer); err != nil {
			return ter.TecNO_TARGET
		}
	}
	if exists, _ := view.Exists(keylet.Credential(subjectID, issuerID, credTypeBytes)); !exists {
		return ter.TecNO_ENTRY
	}
	return ter.TesSUCCESS
}

func (c *CredentialDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("credential delete apply",
		"account", c.Account,
		"subject", c.Subject,
		"issuer", c.Issuer,
		"credentialType", c.CredentialType,
	)

	if c.CredentialType == "" {
		return ter.TemINVALID
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}

	// Default subject/issuer to Account if not specified
	var subjectID, issuerID [20]byte

	if c.Subject != "" {
		subjectID, err = state.DecodeAccountID(c.Subject)
		if err != nil {
			return ter.TecNO_TARGET
		}
	} else {
		subjectID = ctx.AccountID
	}

	if c.Issuer != "" {
		issuerID, err = state.DecodeAccountID(c.Issuer)
		if err != nil {
			return ter.TecNO_TARGET
		}
	} else {
		issuerID = ctx.AccountID
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	credKeylet := keylet.Credential(subjectID, issuerID, credTypeBytes)

	// Preclaim check: verify credential exists
	credData, err := ctx.View.Read(credKeylet)
	if err != nil || credData == nil {
		return ter.TecNO_ENTRY
	}

	// Parse the credential entry
	cred, err := ParseCredentialEntry(credData)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Permission check: only subject or issuer can delete non-expired credentials
	// Anyone can delete expired credentials
	closeTime := ctx.Config.ParentCloseTime
	isExpired := CheckCredentialExpired(cred, closeTime)
	isSubject := subjectID == ctx.AccountID
	isIssuer := issuerID == ctx.AccountID

	if !isSubject && !isIssuer && !isExpired {
		ctx.Log.Trace("credential delete: can't delete non-expired credential")
		return ter.TecNO_PERMISSION
	}

	if result := DeleteSLE(ctx, credKeylet, cred); result != ter.TesSUCCESS {
		return result
	}

	// DeleteSLE adjusts owner counts through the view; when the sender owns the
	// credential, resync ctx.Account so the engine's writeback keeps the change.
	if isSubject || isIssuer {
		ctx.SyncSenderOwnerCount()
	}

	return ter.TesSUCCESS
}

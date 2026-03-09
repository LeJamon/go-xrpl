package credential

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeCredentialDelete, func() tx.Transaction {
		return &CredentialDelete{BaseTx: *tx.NewBaseTx(tx.TypeCredentialDelete, "")}
	})
}

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

// TxType returns the transaction type
func (c *CredentialDelete) TxType() tx.Type {
	return tx.TypeCredentialDelete
}

// Validate validates the CredentialDelete transaction
// Reference: rippled Credentials.cpp CredentialDelete::preflight()
func (c *CredentialDelete) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask)
	// Reference: rippled Credentials.cpp:217-222
	if c.Common.Flags != nil && *c.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// At least one of Subject or Issuer must be present
	// Reference: rippled Credentials.cpp:224-233
	if c.Subject == "" && c.Issuer == "" {
		return ErrCredentialNoFields
	}

	// If present, Subject and Issuer must not be zero accounts
	// Reference: rippled Credentials.cpp:235-241
	if c.Subject != "" {
		if subjectID, err := sle.DecodeAccountID(c.Subject); err == nil {
			var zeroAccount [20]byte
			if subjectID == zeroAccount {
				return ErrCredentialZeroAccount
			}
		}
	}
	if c.Issuer != "" {
		if issuerID, err := sle.DecodeAccountID(c.Issuer); err == nil {
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
		return errors.New("temMALFORMED: CredentialType must be valid hex string")
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
func (c *CredentialDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments returns the amendments required for this transaction type
func (c *CredentialDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Apply applies the CredentialDelete transaction to ledger state.
// Reference: rippled Credentials.cpp CredentialDelete::doApply()
func (c *CredentialDelete) Apply(ctx *tx.ApplyContext) tx.Result {
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
		subjectID, err = sle.DecodeAccountID(c.Subject)
		if err != nil {
			return tx.TecNO_TARGET
		}
	} else {
		subjectID = ctx.AccountID
	}

	if c.Issuer != "" {
		issuerID, err = sle.DecodeAccountID(c.Issuer)
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
	isExpired := checkCredentialExpired(cred, closeTime)
	isSubject := subjectID == ctx.AccountID
	isIssuer := issuerID == ctx.AccountID

	if !isSubject && !isIssuer && !isExpired {
		return tx.TecNO_PERMISSION
	}

	// Remove from issuer's owner directory
	issuerDirKey := keylet.OwnerDir(issuerID)
	sle.DirRemove(ctx.View, issuerDirKey, cred.IssuerNode, credKeylet.Key, false)

	// Remove from subject's owner directory (if different from issuer)
	if subjectID != issuerID {
		subjectDirKey := keylet.OwnerDir(subjectID)
		sle.DirRemove(ctx.View, subjectDirKey, cred.SubjectNode, credKeylet.Key, false)
	}

	// Delete the credential
	if err := ctx.View.Erase(credKeylet); err != nil {
		return tx.TefINTERNAL
	}

	// Adjust owner count based on who owns the credential
	// If accepted, subject owns it. If not accepted, issuer owns it.
	if cred.IsAccepted() {
		// Credential was accepted, subject owns it
		if isSubject {
			// Transaction sender is the subject (owner)
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		} else {
			// Need to decrease subject's owner count
			subjectAccountKeylet := keylet.Account(subjectID)
			subjectData, err := ctx.View.Read(subjectAccountKeylet)
			if err == nil && subjectData != nil {
				subjectAccount, err := sle.ParseAccountRoot(subjectData)
				if err == nil && subjectAccount.OwnerCount > 0 {
					subjectAccount.OwnerCount--
					updatedData, err := sle.SerializeAccountRoot(subjectAccount)
					if err == nil {
						ctx.View.Update(subjectAccountKeylet, updatedData)
					}
				}
			}
		}
	} else {
		// Credential was not accepted, issuer owns it
		if isIssuer {
			// Transaction sender is the issuer (owner)
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		} else {
			// Need to decrease issuer's owner count
			issuerAccountKeylet := keylet.Account(issuerID)
			issuerData, err := ctx.View.Read(issuerAccountKeylet)
			if err == nil && issuerData != nil {
				issuerAccount, err := sle.ParseAccountRoot(issuerData)
				if err == nil && issuerAccount.OwnerCount > 0 {
					issuerAccount.OwnerCount--
					updatedData, err := sle.SerializeAccountRoot(issuerAccount)
					if err == nil {
						ctx.View.Update(issuerAccountKeylet, updatedData)
					}
				}
			}
		}
	}

	return tx.TesSUCCESS
}

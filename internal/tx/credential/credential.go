package credential

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeCredentialAccept, func() tx.Transaction {
		return &CredentialAccept{BaseTx: *tx.NewBaseTx(tx.TypeCredentialAccept, "")}
	})
}

// CredentialAccept accepts a credential.
type CredentialAccept struct {
	tx.BaseTx

	// Issuer is the account that issued the credential (required)
	Issuer string `json:"Issuer" xrpl:"Issuer"`

	// CredentialType is the type of credential (required, hex-encoded)
	CredentialType string `json:"CredentialType" xrpl:"CredentialType"`
}

// NewCredentialAccept creates a new CredentialAccept transaction
func NewCredentialAccept(account, issuer, credentialType string) *CredentialAccept {
	return &CredentialAccept{
		BaseTx:         *tx.NewBaseTx(tx.TypeCredentialAccept, account),
		Issuer:         issuer,
		CredentialType: credentialType,
	}
}

// TxType returns the transaction type
func (c *CredentialAccept) TxType() tx.Type {
	return tx.TypeCredentialAccept
}

// Validate validates the CredentialAccept transaction
// Reference: rippled Credentials.cpp CredentialAccept::preflight()
// Note: The fixInvalidTxFlags-gated flag check is done in Apply() because
// Validate() has no access to amendment rules.
func (c *CredentialAccept) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// Flag check is deferred to Apply() where amendment rules are available.
	// Reference: rippled Credentials.cpp:304-308 — gated behind fixInvalidTxFlags.

	// Issuer is required and must not be zero
	// Reference: rippled Credentials.cpp:310-314
	if c.Issuer == "" {
		return ErrCredentialNoIssuer
	}
	if issuerID, err := state.DecodeAccountID(c.Issuer); err == nil {
		var zeroAccount [20]byte
		if issuerID == zeroAccount {
			return ErrCredentialNoIssuer
		}
	}

	// Validate CredentialType field (required, max 64 bytes)
	// Reference: rippled Credentials.cpp:316-323
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
func (c *CredentialAccept) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments returns the amendments required for this transaction type
func (c *CredentialAccept) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// ApplyOnTec applies side-effects for tecEXPIRED: delete the expired credential and adjust owner count.
// Reference: rippled Transactor.cpp removeExpiredCredentials()
func (c *CredentialAccept) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	if c.Issuer == "" || c.CredentialType == "" {
		return tx.TemINVALID
	}

	issuerID, err := state.DecodeAccountID(c.Issuer)
	if err != nil {
		return tx.TefINTERNAL
	}

	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return tx.TefINTERNAL
	}

	// credential(subject=sender, issuer, credType)
	credKeylet := keylet.Credential(ctx.AccountID, issuerID, credTypeBytes)

	// Check the credential exists and is expired
	credData, err := ctx.View.Read(credKeylet)
	if err != nil || credData == nil {
		return tx.TesSUCCESS
	}

	cred, err := ParseCredentialEntry(credData)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !CheckCredentialExpired(cred, ctx.Config.ParentCloseTime) {
		return tx.TesSUCCESS
	}

	// Use DeleteSLE to properly clean up directories and owner counts
	if err := DeleteSLE(ctx.View, credKeylet, cred); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// Apply applies the CredentialAccept transaction to ledger state.
// Reference: rippled Credentials.cpp CredentialAccept::doApply()
func (c *CredentialAccept) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check for invalid flags, gated behind fixInvalidTxFlags
	// Reference: rippled Credentials.cpp:304-308
	if ctx.Rules().Enabled(amendment.FeatureFixInvalidTxFlags) {
		if c.GetFlags()&tx.TfUniversalMask != 0 {
			return tx.TemINVALID_FLAG
		}
	}

	if c.Issuer == "" || c.CredentialType == "" {
		return tx.TemINVALID
	}

	issuerID, err := state.DecodeAccountID(c.Issuer)
	if err != nil {
		return tx.TecNO_TARGET
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return tx.TemINVALID
	}

	// Preclaim check: verify issuer account exists
	issuerAccountKeylet := keylet.Account(issuerID)
	issuerExists, err := ctx.View.Exists(issuerAccountKeylet)
	if err != nil || !issuerExists {
		return tx.TecNO_ISSUER
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	// where subject = ctx.AccountID (the transaction sender)
	credKeylet := keylet.Credential(ctx.AccountID, issuerID, credTypeBytes)

	// Read the credential
	credData, err := ctx.View.Read(credKeylet)
	if err != nil || credData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse the credential entry
	cred, err := ParseCredentialEntry(credData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check if already accepted
	if cred.IsAccepted() {
		return tx.TecDUPLICATE
	}

	// Check if credential is expired
	closeTime := ctx.Config.ParentCloseTime
	if CheckCredentialExpired(cred, closeTime) {
		// Delete expired credentials even if the transaction failed
		if err := ctx.View.Erase(credKeylet); err != nil {
			return tx.TefINTERNAL
		}
		// Decrease issuer's owner count
		issuerData, err := ctx.View.Read(issuerAccountKeylet)
		if err == nil && issuerData != nil {
			issuerAccount, err := state.ParseAccountRoot(issuerData)
			if err == nil && issuerAccount.OwnerCount > 0 {
				issuerAccount.OwnerCount--
				updatedIssuerData, err := state.SerializeAccountRoot(issuerAccount)
				if err == nil {
					ctx.View.Update(issuerAccountKeylet, updatedIssuerData)
				}
			}
		}
		return tx.TecEXPIRED
	}

	// Check reserve for subject (ctx.Account)
	// Use prior balance (before fee deduction) to match rippled's behavior
	// Reference: rippled Credentials.cpp line 376: if (mPriorBalance < reserve)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Set accepted flag
	cred.SetAccepted()

	// Serialize and update the credential
	updatedCredData, err := serializeCredentialEntry(cred)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(credKeylet, updatedCredData); err != nil {
		return tx.TefINTERNAL
	}

	// Transfer ownership: decrease issuer's owner count, increase subject's owner count
	// Read issuer account
	issuerData, err := ctx.View.Read(issuerAccountKeylet)
	if err != nil || issuerData == nil {
		return tx.TefINTERNAL
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Decrease issuer's owner count
	if issuerAccount.OwnerCount > 0 {
		issuerAccount.OwnerCount--
	}

	// Serialize and update issuer account
	if result := ctx.UpdateAccountRoot(issuerID, issuerAccount); result != tx.TesSUCCESS {
		return result
	}

	// Increase subject's owner count
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

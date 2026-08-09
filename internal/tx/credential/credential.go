package credential

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func (c *CredentialAccept) TxType() tx.Type {
	return tx.TypeCredentialAccept
}

// GetFlagsMask reports the invalid-flag mask. rippled's
// CredentialAccept::getFlagsMask is `fixInvalidTxFlags ? tfUniversalMask : 0`,
// so with the amendment active any flag is rejected temINVALID_FLAG at
// preflight0 (before the field checks and signature verification); with it off
// the mask is zero and all flags pass.
func (c *CredentialAccept) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.Enabled(amendment.FeatureFixInvalidTxFlags) {
		return tx.TfUniversalMask
	}
	return 0
}

// Reference: rippled Credentials.cpp CredentialAccept::preflight()
func (c *CredentialAccept) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

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

func (c *CredentialAccept) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CredentialAccept) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureCredentials}
}

// Preclaim verifies the issuer account exists (tecNO_ISSUER), the credential
// exists (tecNO_ENTRY), and it is not already accepted (tecDUPLICATE), matching
// rippled CredentialAccept::preclaim. The expiry check (tecEXPIRED, with the
// expired-credential deletion) and the reserve check stay in Apply, mirroring
// rippled CredentialAccept::doApply — the deletion needs an ApplyView, so a
// tecEXPIRED never escapes preclaim.
func (c *CredentialAccept) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	subjectID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	issuerID, err := state.DecodeAccountID(c.Issuer)
	if err != nil {
		return ter.TecNO_TARGET
	}
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}
	if exists, _ := view.Exists(keylet.Account(issuerID)); !exists {
		return ter.TecNO_ISSUER
	}
	credData, rerr := view.Read(keylet.Credential(subjectID, issuerID, credTypeBytes))
	if rerr != nil || credData == nil {
		return ter.TecNO_ENTRY
	}
	cred, perr := ParseCredentialEntry(credData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	if cred.IsAccepted() {
		return ter.TecDUPLICATE
	}
	return ter.TesSUCCESS
}

// Reference: rippled Credentials.cpp CredentialAccept::doApply()
func (c *CredentialAccept) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("credential accept apply",
		"account", c.Account,
		"issuer", c.Issuer,
		"credentialType", c.CredentialType,
	)

	if c.Issuer == "" || c.CredentialType == "" {
		return ter.TemINVALID
	}

	issuerID, err := state.DecodeAccountID(c.Issuer)
	if err != nil {
		return ter.TecNO_TARGET
	}

	// Decode credential type from hex to bytes
	credTypeBytes, err := hex.DecodeString(c.CredentialType)
	if err != nil {
		return ter.TemINVALID
	}

	// Compute correct keylet: credential(subject, issuer, credType)
	// where subject = ctx.AccountID (the transaction sender)
	credKeylet := keylet.Credential(ctx.AccountID, issuerID, credTypeBytes)

	// The reserve check precedes expiration handling. An expired credential is
	// left in place when the subject cannot afford to accept it.
	if result := tx.CheckReserve(ctx, c.GetCommon(), ctx.AccountID, ctx.Account, ctx.PriorBalance(), tx.ReserveAdjustment{OwnerCountDelta: 1}, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
		return result
	}

	// Read the credential (Preclaim guaranteed it exists and is unaccepted; the
	// entry is needed here for the expiry check and the accept mutation).
	credData, err := ctx.View.Read(credKeylet)
	if err != nil || credData == nil {
		return ter.TecNO_ENTRY
	}

	// Parse the credential entry
	cred, err := ParseCredentialEntry(credData)
	if err != nil {
		return ter.TefINTERNAL
	}

	closeTime := ctx.Config.ParentCloseTime
	if CheckCredentialExpired(cred, closeTime) {
		// Delete the expired credential, cleaning up both owner directories and
		// the issuer's owner count, even though the accept itself fails.
		if result := DeleteSLE(ctx, credKeylet, cred); result != ter.TesSUCCESS {
			return result
		}
		return ter.TecEXPIRED
	}

	cred.SetAccepted()
	issuerAccount := ctx.Account
	if issuerID != ctx.AccountID {
		issuerAccount, err = tx.ReadAccountRoot(ctx.View, issuerID)
		if err != nil || issuerAccount == nil {
			return ter.TefINTERNAL
		}
	}
	if result := tx.DecreaseOwnerCountForObject(ctx, issuerID, issuerAccount, credData, "Sponsor", 1); result != ter.TesSUCCESS {
		return result
	}

	sponsorAddress, result := tx.IncreaseOwnerCount(ctx, c.GetCommon(), ctx.AccountID, ctx.Account, 1)
	if result != ter.TesSUCCESS {
		return result
	}
	cred.Sponsor = sponsorAddress

	// Serialize and update the credential
	updatedCredData, err := serializeCredentialEntry(cred)
	if err != nil {
		return ter.TefINTERNAL
	}

	if err := ctx.View.Update(credKeylet, updatedCredData); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

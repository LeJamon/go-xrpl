package escrow

import (
	"encoding/hex"
	"errors"
	"sort"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeEscrowFinish, func() tx.Transaction {
		return &EscrowFinish{BaseTx: *tx.NewBaseTx(tx.TypeEscrowFinish, "")}
	})
}

// EscrowFinish completes an escrow, releasing the escrowed XRP.
type EscrowFinish struct {
	tx.BaseTx

	// Owner is the account that created the escrow (required)
	Owner string `json:"Owner" xrpl:"Owner"`

	// OfferSequence is the sequence number of the EscrowCreate (required)
	OfferSequence uint32 `json:"OfferSequence" xrpl:"OfferSequence"`

	// Condition is the crypto-condition that was fulfilled (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Condition *string `json:"Condition,omitempty" xrpl:"Condition,omitempty"`

	// Fulfillment is the fulfillment for the condition (optional).
	// Pointer to distinguish "not set" (nil) from "set to empty" (ptr to "").
	Fulfillment *string `json:"Fulfillment,omitempty" xrpl:"Fulfillment,omitempty"`

	// CredentialIDs is a list of credential ledger entry IDs (uint256 hashes as hex strings)
	// Used for deposit preauth with credentials.
	// Reference: rippled sfCredentialIDs
	CredentialIDs []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`
}

// NewEscrowFinish creates a new EscrowFinish transaction
func NewEscrowFinish(account, owner string, offerSequence uint32) *EscrowFinish {
	return &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, account),
		Owner:         owner,
		OfferSequence: offerSequence,
	}
}

// TxType returns the transaction type
func (e *EscrowFinish) TxType() tx.Type {
	return tx.TypeEscrowFinish
}

// Validate validates the EscrowFinish transaction
// Reference: rippled Escrow.cpp EscrowFinish::preflight()
func (e *EscrowFinish) Validate() error {
	if err := e.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	if e.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	if e.Owner == "" {
		return errors.New("temMALFORMED: Owner is required")
	}

	// Both Condition and Fulfillment must be present or absent together
	// Reference: rippled Escrow.cpp:644-646
	// "Present" means the field exists in the transaction (even if empty value).
	hasCondition := e.Condition != nil
	hasFulfillment := e.Fulfillment != nil
	if hasCondition != hasFulfillment {
		return errors.New("temMALFORMED: Condition and Fulfillment must be provided together")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (e *EscrowFinish) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(e)
}

// Apply applies an EscrowFinish transaction
// Reference: rippled Escrow.cpp EscrowFinish::preclaim() + doApply()
func (ef *EscrowFinish) Apply(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()

	// Amendment-gated check: CredentialIDs requires Credentials amendment
	// Reference: rippled Escrow.cpp preflight() credential check
	if len(ef.CredentialIDs) > 0 && !rules.Enabled(amendment.FeatureCredentials) {
		return tx.TemDISABLED
	}

	// --- Preclaim: credential validation (before time checks) ---
	// Reference: rippled EscrowFinish::preclaim() calls credentials::valid()
	// This must run before doApply's time checks because rippled's preclaim
	// runs before doApply.
	if len(ef.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := validateCredentials(ctx, ef.CredentialIDs); result != tx.TesSUCCESS {
			return result
		}
	}

	// Get the escrow owner's account ID
	ownerID, err := sle.DecodeAccountID(ef.Owner)
	if err != nil {
		return tx.TemINVALID
	}

	// Find the escrow
	escrowKey := keylet.Escrow(ownerID, ef.OfferSequence)
	escrowData, err := ctx.View.Read(escrowKey)
	if err != nil || escrowData == nil {
		return tx.TecNO_TARGET
	}

	// Parse escrow
	escrowEntry, err := sle.ParseEscrow(escrowData)
	if err != nil {
		return tx.TefINTERNAL
	}

	closeTime := ctx.Config.ParentCloseTime

	// --- doApply: Time validation ---
	// Reference: rippled Escrow.cpp doApply() lines 1030-1055
	if rules.Enabled(amendment.FeatureFix1571) {
		// fix1571: FinishAfter check — close time must be strictly after finish time
		if escrowEntry.FinishAfter > 0 && closeTime <= escrowEntry.FinishAfter {
			return tx.TecNO_PERMISSION
		}
		// fix1571: CancelAfter check — if past cancel time, finish not allowed
		if escrowEntry.CancelAfter > 0 && closeTime > escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	} else {
		// Pre-fix1571: both use <= comparison (known bug in cancel check)
		if escrowEntry.FinishAfter > 0 && closeTime <= escrowEntry.FinishAfter {
			return tx.TecNO_PERMISSION
		}
		if escrowEntry.CancelAfter > 0 && closeTime <= escrowEntry.CancelAfter {
			return tx.TecNO_PERMISSION
		}
	}

	// Crypto-condition verification
	// Reference: rippled Escrow.cpp doApply() lines 1057-1101
	txCondition := ""
	if ef.Condition != nil {
		txCondition = *ef.Condition
	}
	txFulfillment := ""
	if ef.Fulfillment != nil {
		txFulfillment = *ef.Fulfillment
	}

	if escrowEntry.Condition == "" {
		// Escrow has no condition — tx must NOT provide condition/fulfillment
		if txCondition != "" || txFulfillment != "" {
			return tx.TecCRYPTOCONDITION_ERROR
		}
	} else {
		// Escrow has a condition — fulfillment is required (non-empty)
		if txFulfillment == "" {
			return tx.TecCRYPTOCONDITION_ERROR
		}

		// Condition in tx must match condition on escrow
		if txCondition != escrowEntry.Condition {
			return tx.TecCRYPTOCONDITION_ERROR
		}

		// Verify fulfillment matches condition
		if err := validateCryptoCondition(txFulfillment, escrowEntry.Condition); err != nil {
			return tx.TecCRYPTOCONDITION_ERROR
		}
	}

	// Determine if finisher is the destination and/or the owner.
	destIsSelf := ctx.AccountID == escrowEntry.DestinationID

	// Read destination account for deposit auth check
	var destAccount *sle.AccountRoot
	destKey := keylet.Account(escrowEntry.DestinationID)
	if destIsSelf {
		destAccount = ctx.Account
	} else {
		destData, err := ctx.View.Read(destKey)
		if err != nil {
			return tx.TecNO_DST
		}
		destAccount, err = sle.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}
	}

	// Deposit authorization check
	// Reference: rippled verifyDepositPreauth() in CredentialHelpers.cpp
	if rules.Enabled(amendment.FeatureDepositAuth) {
		if (destAccount.Flags & sle.LsfDepositAuth) != 0 {
			if ctx.AccountID != escrowEntry.DestinationID {
				// Check account-based DepositPreauth
				preauthKey := keylet.DepositPreauth(escrowEntry.DestinationID, ctx.AccountID)
				if exists, _ := ctx.View.Exists(preauthKey); !exists {
					// No account-based preauth — check credential-based
					if len(ef.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
						if result := authorizedDepositPreauth(ctx, ef.CredentialIDs, escrowEntry.DestinationID); result != tx.TesSUCCESS {
							return result
						}
					} else {
						return tx.TecNO_PERMISSION
					}
				}
			}
		}
	}

	// Transfer the escrowed amount to destination
	destAccount.Balance += escrowEntry.Amount

	// Write destination back (only if it's a separate account from the finisher)
	if !destIsSelf {
		destUpdatedData, err := sle.SerializeAccountRoot(destAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Remove escrow from owner directory
	// Reference: rippled Escrow.cpp doApply() lines 1130-1140
	ownerDirKey := keylet.OwnerDir(escrowEntry.Account)
	sle.DirRemove(ctx.View, ownerDirKey, escrowEntry.OwnerNode, escrowKey.Key, false)

	// Remove escrow from destination directory (if cross-account)
	if escrowEntry.HasDestNode {
		destDirKey := keylet.OwnerDir(escrowEntry.DestinationID)
		sle.DirRemove(ctx.View, destDirKey, escrowEntry.DestinationNode, escrowKey.Key, false)
	}

	// Delete the escrow
	if err := ctx.View.Erase(escrowKey); err != nil {
		return tx.TefINTERNAL
	}

	// Decrement OwnerCount for escrow owner
	adjustOwnerCount(ctx, ownerID, -1)

	// If cross-account, also decrement destination's OwnerCount
	if escrowEntry.Account != escrowEntry.DestinationID {
		adjustOwnerCount(ctx, escrowEntry.DestinationID, -1)
	}

	return tx.TesSUCCESS
}

// validateCredentials implements rippled's credentials::valid() preclaim check.
// For each credential ID, it reads the Credential SLE and validates:
// 1. The credential exists
// 2. The credential's Subject matches the transaction sender (src)
// 3. The credential has been accepted (lsfAccepted flag)
// Reference: rippled CredentialHelpers.cpp credentials::valid()
func validateCredentials(ctx *tx.ApplyContext, credentialIDs []string) tx.Result {
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TecBAD_CREDENTIALS
		}

		var credID [32]byte
		copy(credID[:], credHash)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			// Credential doesn't exist
			return tx.TecBAD_CREDENTIALS
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		// Subject must match the transaction sender
		if credEntry.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		// Credential must be accepted
		if (credEntry.Flags & credential.LsfCredentialAccepted) == 0 {
			return tx.TecBAD_CREDENTIALS
		}
	}

	return tx.TesSUCCESS
}

// authorizedDepositPreauth implements rippled's credentials::authorizedDepositPreauth().
// It reads each credential, extracts the (Issuer, CredentialType) pairs,
// sorts them, and checks if a credential-based DepositPreauth exists for the destination.
// Reference: rippled CredentialHelpers.cpp credentials::authorizedDepositPreauth()
func authorizedDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, dst [20]byte) tx.Result {
	type credPair struct {
		issuer [20]byte
		credType []byte
	}

	pairs := make([]credPair, 0, len(credentialIDs))
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TefINTERNAL
		}

		var credID [32]byte
		copy(credID[:], credHash)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		pairs = append(pairs, credPair{
			issuer:   credEntry.Issuer,
			credType: credEntry.CredentialType,
		})
	}

	// Sort by (issuer, credType) to match rippled's sorted set
	sort.Slice(pairs, func(i, j int) bool {
		cmp := compareBytesSlice(pairs[i].issuer[:], pairs[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return compareBytesSlice(pairs[i].credType, pairs[j].credType) < 0
	})

	// Convert to keylet.CredentialPair for keylet computation
	sortedCreds := make([]keylet.CredentialPair, len(pairs))
	for i, p := range pairs {
		sortedCreds[i] = keylet.CredentialPair{
			Issuer:         p.issuer,
			CredentialType: p.credType,
		}
	}

	// Check if credential-based DepositPreauth exists
	dpKey := keylet.DepositPreauthCredentials(dst, sortedCreds)
	if exists, _ := ctx.View.Exists(dpKey); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// compareBytesSlice compares two byte slices lexicographically.
func compareBytesSlice(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// adjustOwnerCount adjusts the OwnerCount of the given account by delta.
// When the target account is ctx.Account (the transaction sender), it modifies
// ctx.Account directly. Otherwise it reads/writes through the table.
func adjustOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int) {
	if accountID == ctx.AccountID {
		if delta > 0 {
			ctx.Account.OwnerCount += uint32(delta)
		} else if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
		return
	}

	acctKey := keylet.Account(accountID)
	acctData, err := ctx.View.Read(acctKey)
	if err != nil {
		return
	}
	acct, err := sle.ParseAccountRoot(acctData)
	if err != nil {
		return
	}

	if delta > 0 {
		acct.OwnerCount += uint32(delta)
	} else if acct.OwnerCount > 0 {
		acct.OwnerCount--
	}

	if updatedData, err := sle.SerializeAccountRoot(acct); err == nil {
		ctx.View.Update(acctKey, updatedData)
	}
}

package depositpreauth

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/crypto/common"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

const (
	// maxCredentialsArraySize is the maximum number of credentials in
	// AuthorizeCredentials/UnauthorizeCredentials arrays.
	// Reference: rippled Protocol.h maxCredentialsArraySize = 8
	maxCredentialsArraySize = 8

	// maxCredentialTypeLength is the maximum byte length of a CredentialType.
	// Reference: rippled Protocol.h maxCredentialTypeLength = 64
	maxCredentialTypeLength = 64
)

func init() {
	tx.Register(tx.TypeDepositPreauth, func() tx.Transaction {
		return &DepositPreauth{BaseTx: *tx.NewBaseTx(tx.TypeDepositPreauth, "")}
	})
}

// CredentialSpec identifies a credential by issuer and type.
// Reference: rippled's AuthorizeCredentials inner object
type CredentialSpec struct {
	Issuer         string `json:"Issuer" xrpl:"Issuer"`
	CredentialType string `json:"CredentialType" xrpl:"CredentialType"`
}

// CredentialWrapper wraps a CredentialSpec in the XRPL STObject pattern.
// Reference: rippled's Credential inner object in AuthorizeCredentials array
type CredentialWrapper struct {
	Credential CredentialSpec `json:"Credential" xrpl:"Credential"`
}

// DepositPreauth preauthorizes an account for direct deposits.
type DepositPreauth struct {
	tx.BaseTx

	// Authorize is the account to preauthorize (mutually exclusive with others)
	Authorize string `json:"Authorize,omitempty" xrpl:"Authorize,omitempty"`

	// Unauthorize is the account to remove preauthorization (mutually exclusive with others)
	Unauthorize string `json:"Unauthorize,omitempty" xrpl:"Unauthorize,omitempty"`

	// AuthorizeCredentials authorizes deposits from accounts with matching credentials.
	// Mutually exclusive with Authorize, Unauthorize, and UnauthorizeCredentials.
	// Reference: rippled DepositPreauth with sfAuthorizeCredentials
	AuthorizeCredentials []CredentialWrapper `json:"AuthorizeCredentials,omitempty" xrpl:"AuthorizeCredentials,omitempty"`

	// UnauthorizeCredentials removes credential-based deposit authorization.
	// Mutually exclusive with Authorize, Unauthorize, and AuthorizeCredentials.
	// Reference: rippled DepositPreauth with sfUnauthorizeCredentials
	UnauthorizeCredentials []CredentialWrapper `json:"UnauthorizeCredentials,omitempty" xrpl:"UnauthorizeCredentials,omitempty"`
}

// NewDepositPreauth creates a new DepositPreauth transaction
func NewDepositPreauth(account string) *DepositPreauth {
	return &DepositPreauth{
		BaseTx: *tx.NewBaseTx(tx.TypeDepositPreauth, account),
	}
}

// TxType returns the transaction type
func (d *DepositPreauth) TxType() tx.Type {
	return tx.TypeDepositPreauth
}

// RequiredAmendments returns the amendments required for this transaction type.
// Reference: rippled DepositPreauth::preflight() amendment checks
func (d *DepositPreauth) RequiredAmendments() [][32]byte {
	amendments := [][32]byte{amendment.FeatureDepositPreauth}
	if len(d.AuthorizeCredentials) > 0 || len(d.UnauthorizeCredentials) > 0 {
		amendments = append(amendments, amendment.FeatureCredentials)
	}
	return amendments
}

// Validate validates the DepositPreauth transaction fields.
// Reference: rippled DepositPreauth::preflight()
func (d *DepositPreauth) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// No flags allowed
	// Reference: rippled preflight() - tx.getFlags() & tfUniversalMask
	if d.Flags != nil && *d.Flags != 0 {
		return errors.New("temINVALID_FLAG: Invalid flags set")
	}

	// Count which fields are present.
	// Use nil check (not len>0) because an empty non-nil array means the field
	// IS present, just empty — which is still a presence for mutual exclusivity.
	hasAuth := d.Authorize != ""
	hasUnauth := d.Unauthorize != ""
	hasAuthCreds := d.AuthorizeCredentials != nil
	hasUnauthCreds := d.UnauthorizeCredentials != nil

	authPresent := boolToInt(hasAuth) + boolToInt(hasUnauth)
	authCredPresent := boolToInt(hasAuthCreds) + boolToInt(hasUnauthCreds)

	// Exactly one of the 4 fields must be present
	// Reference: rippled preflight() - authPresent + authCredPresent != 1
	if authPresent+authCredPresent != 1 {
		return errors.New("temMALFORMED: Invalid Authorize and Unauthorize field combination")
	}

	if authPresent > 0 {
		// Account-based preauth validation
		target := d.Authorize
		if target == "" {
			target = d.Unauthorize
		}

		// Validate target account is not zero
		targetID, err := state.DecodeAccountID(target)
		if err != nil {
			return errors.New("temINVALID_ACCOUNT_ID: Authorized or Unauthorized field invalid")
		}
		if targetID == [20]byte{} {
			return errors.New("temINVALID_ACCOUNT_ID: Authorized or Unauthorized field zeroed")
		}

		// Cannot preauthorize self (only checked for Authorize, not Unauthorize)
		// Reference: rippled preflight() - optAuth && target == ctx.tx[sfAccount]
		if hasAuth && target == d.Account {
			return errors.New("temCAN_NOT_PREAUTH_SELF: Attempting to DepositPreauth self")
		}
	} else {
		// Credential-based preauth validation
		var creds []CredentialWrapper
		if hasAuthCreds {
			creds = d.AuthorizeCredentials
		} else {
			creds = d.UnauthorizeCredentials
		}

		if err := checkCredentialArray(creds); err != nil {
			return err
		}
	}

	return nil
}

// checkCredentialArray validates a credential array.
// Reference: rippled credentials::checkArray()
func checkCredentialArray(creds []CredentialWrapper) error {
	if len(creds) == 0 {
		return errors.New("temARRAY_EMPTY: Invalid credentials size: 0")
	}
	if len(creds) > maxCredentialsArraySize {
		return fmt.Errorf("temARRAY_TOO_LARGE: Invalid credentials size: %d", len(creds))
	}

	// Check each credential and detect duplicates
	duplicates := make(map[[32]byte]bool)
	for _, cw := range creds {
		c := cw.Credential

		// Validate issuer
		issuerID, err := state.DecodeAccountID(c.Issuer)
		if err != nil || issuerID == ([20]byte{}) {
			return errors.New("temINVALID_ACCOUNT_ID: Issuer account is invalid")
		}

		// Validate credential type (hex-encoded, 1-64 raw bytes)
		credTypeBytes, err := hex.DecodeString(c.CredentialType)
		if err != nil || len(credTypeBytes) == 0 || len(credTypeBytes) > maxCredentialTypeLength {
			return errors.New("temMALFORMED: Invalid credentialType size")
		}

		// Check for duplicates using sha512Half(issuer, credType)
		hash := common.Sha512Half(issuerID[:], credTypeBytes)
		if duplicates[hash] {
			return errors.New("temMALFORMED: duplicates in credentials")
		}
		duplicates[hash] = true
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (d *DepositPreauth) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(d)
}

// SetAuthorize sets the account to authorize
func (d *DepositPreauth) SetAuthorize(account string) {
	d.Authorize = account
	d.Unauthorize = ""
}

// SetUnauthorize sets the account to unauthorize
func (d *DepositPreauth) SetUnauthorize(account string) {
	d.Unauthorize = account
	d.Authorize = ""
}

// sortedCredPair is a sorted (issuer, credentialType) pair.
type sortedCredPair struct {
	issuer   [20]byte
	credType []byte
}

// makeSorted creates a sorted, deduplicated list of credential pairs from a
// CredentialWrapper array. Returns nil if duplicates are found.
// Reference: rippled credentials::makeSorted()
func makeSorted(creds []CredentialWrapper) []sortedCredPair {
	pairs := make([]sortedCredPair, 0, len(creds))
	for _, cw := range creds {
		issuerID, err := state.DecodeAccountID(cw.Credential.Issuer)
		if err != nil {
			return nil
		}
		credTypeBytes, err := hex.DecodeString(cw.Credential.CredentialType)
		if err != nil {
			return nil
		}
		pairs = append(pairs, sortedCredPair{issuer: issuerID, credType: credTypeBytes})
	}

	// Sort by issuer first, then by credType
	sort.Slice(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].issuer[:], pairs[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(pairs[i].credType, pairs[j].credType) < 0
	})

	// Check for duplicates
	for i := 1; i < len(pairs); i++ {
		if pairs[i].issuer == pairs[i-1].issuer &&
			bytes.Equal(pairs[i].credType, pairs[i-1].credType) {
			return nil
		}
	}

	return pairs
}

// toKeyletPairs converts sorted credential pairs to keylet.CredentialPair for
// keylet computation.
func toKeyletPairs(pairs []sortedCredPair) []keylet.CredentialPair {
	result := make([]keylet.CredentialPair, len(pairs))
	for i, p := range pairs {
		result[i] = keylet.CredentialPair{
			Issuer:         p.issuer,
			CredentialType: p.credType,
		}
	}
	return result
}

// Apply applies the DepositPreauth transaction to ledger state.
// Combines preclaim checks and doApply logic.
// Reference: rippled DepositPreauth::preclaim() + DepositPreauth::doApply()
func (d *DepositPreauth) Apply(ctx *tx.ApplyContext) tx.Result {
	if d.Authorize != "" {
		return d.applyAuthorize(ctx)
	} else if d.Unauthorize != "" {
		return d.applyUnauthorize(ctx)
	} else if len(d.AuthorizeCredentials) > 0 {
		return d.applyAuthorizeCredentials(ctx)
	} else if len(d.UnauthorizeCredentials) > 0 {
		return d.applyUnauthorizeCredentials(ctx)
	}
	return tx.TemMALFORMED
}

// applyAuthorize handles the Authorize case.
// Reference: rippled DepositPreauth preclaim(sfAuthorize) + doApply(sfAuthorize)
func (d *DepositPreauth) applyAuthorize(ctx *tx.ApplyContext) tx.Result {
	authorizedID, err := state.DecodeAccountID(d.Authorize)
	if err != nil {
		return tx.TemINVALID
	}

	// --- Preclaim: verify target account exists ---
	if exists, _ := ctx.View.Exists(keylet.Account(authorizedID)); !exists {
		return tx.TecNO_TARGET
	}

	// --- Preclaim: verify preauth entry doesn't already exist ---
	preauthKey := keylet.DepositPreauth(ctx.AccountID, authorizedID)
	if exists, _ := ctx.View.Exists(preauthKey); exists {
		return tx.TecDUPLICATE
	}

	// --- doApply: check reserve ---
	// Use prior balance (before fee deduction) to match rippled's mPriorBalance
	// Reference: rippled DepositPreauth.cpp doApply() - mPriorBalance < reserve
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// --- doApply: create and insert preauth entry ---
	preauthData, err := state.SerializeDepositPreauth(ctx.AccountID, authorizedID)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(preauthKey, preauthData); err != nil {
		return tx.TefINTERNAL
	}

	// --- doApply: insert into owner directory ---
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, preauthKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	// Update OwnerNode on the preauth entry
	if dirResult.Page != 0 {
		if err := updateOwnerNode(ctx, preauthKey, dirResult.Page); err != nil {
			return tx.TefINTERNAL
		}
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// applyUnauthorize handles the Unauthorize case.
// Reference: rippled DepositPreauth preclaim(sfUnauthorize) + doApply(sfUnauthorize)
func (d *DepositPreauth) applyUnauthorize(ctx *tx.ApplyContext) tx.Result {
	unauthorizedID, err := state.DecodeAccountID(d.Unauthorize)
	if err != nil {
		return tx.TemINVALID
	}

	preauthKey := keylet.DepositPreauth(ctx.AccountID, unauthorizedID)

	// --- Preclaim: verify preauth entry exists ---
	if exists, _ := ctx.View.Exists(preauthKey); !exists {
		return tx.TecNO_ENTRY
	}

	return removeFromLedger(ctx, preauthKey)
}

// applyAuthorizeCredentials handles the AuthorizeCredentials case.
// Reference: rippled DepositPreauth preclaim(sfAuthorizeCredentials) + doApply(sfAuthorizeCredentials)
func (d *DepositPreauth) applyAuthorizeCredentials(ctx *tx.ApplyContext) tx.Result {
	// --- Preclaim: sort and validate credentials ---
	sorted := makeSorted(d.AuthorizeCredentials)
	if sorted == nil {
		return tx.TefINTERNAL
	}

	// Verify each issuer account exists
	for _, p := range sorted {
		if exists, _ := ctx.View.Exists(keylet.Account(p.issuer)); !exists {
			return tx.TecNO_ISSUER
		}
	}

	// Verify preauth entry doesn't already exist
	preauthKey := keylet.DepositPreauthCredentials(ctx.AccountID, toKeyletPairs(sorted))
	if exists, _ := ctx.View.Exists(preauthKey); exists {
		return tx.TecDUPLICATE
	}

	// --- doApply: check reserve ---
	// Use prior balance (before fee deduction) to match rippled's mPriorBalance
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// --- doApply: create and insert preauth entry with sorted credentials ---
	sleCreds := make([]state.DepositPreauthCredential, len(sorted))
	for i, p := range sorted {
		addr, err := addresscodec.EncodeAccountIDToClassicAddress(p.issuer[:])
		if err != nil {
			return tx.TefINTERNAL
		}
		sleCreds[i] = state.DepositPreauthCredential{
			Issuer:         addr,
			CredentialType: hex.EncodeToString(p.credType),
		}
	}

	preauthData, err := state.SerializeDepositPreauthCredentials(ctx.AccountID, sleCreds)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(preauthKey, preauthData); err != nil {
		return tx.TefINTERNAL
	}

	// --- doApply: insert into owner directory ---
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, preauthKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	if dirResult.Page != 0 {
		if err := updateOwnerNode(ctx, preauthKey, dirResult.Page); err != nil {
			return tx.TefINTERNAL
		}
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// applyUnauthorizeCredentials handles the UnauthorizeCredentials case.
// Reference: rippled DepositPreauth preclaim(sfUnauthorizeCredentials) + doApply(sfUnauthorizeCredentials)
func (d *DepositPreauth) applyUnauthorizeCredentials(ctx *tx.ApplyContext) tx.Result {
	sorted := makeSorted(d.UnauthorizeCredentials)
	if sorted == nil {
		return tx.TefINTERNAL
	}

	preauthKey := keylet.DepositPreauthCredentials(ctx.AccountID, toKeyletPairs(sorted))

	// --- Preclaim: verify preauth entry exists ---
	if exists, _ := ctx.View.Exists(preauthKey); !exists {
		return tx.TecNO_ENTRY
	}

	return removeFromLedger(ctx, preauthKey)
}

// removeFromLedger removes a deposit preauth entry from the ledger.
// Reads the entry to find OwnerNode, removes from owner directory,
// adjusts owner count, and erases the entry.
// Reference: rippled DepositPreauth::removeFromLedger()
func removeFromLedger(ctx *tx.ApplyContext, preauthKey keylet.Keylet) tx.Result {
	// Read the preauth entry to get OwnerNode for directory removal
	preauthData, err := ctx.View.Read(preauthKey)
	if err != nil || preauthData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse OwnerNode from the binary entry
	var ownerNode uint64
	entry, err := state.ParseDepositPreauth(preauthData)
	if err == nil && entry != nil {
		ownerNode = entry.OwnerNode
	}
	// If parsing fails, default to page 0 which handles the common case

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	_, err = state.DirRemove(ctx.View, ownerDirKey, ownerNode, preauthKey.Key, false)
	if err != nil {
		return tx.TefBAD_LEDGER
	}

	// Erase the entry
	if err := ctx.View.Erase(preauthKey); err != nil {
		return tx.TefINTERNAL
	}

	// Decrement owner count
	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

// updateOwnerNode reads a ledger entry, updates its OwnerNode field, and writes
// it back. This is needed because OwnerNode is set after dir insertion.
func updateOwnerNode(ctx *tx.ApplyContext, k keylet.Keylet, page uint64) error {
	data, err := ctx.View.Read(k)
	if err != nil {
		return err
	}

	entry, err := state.ParseDepositPreauth(data)
	if err != nil {
		return err
	}
	_ = entry // OwnerNode is set during serialization

	// Re-serialize with updated OwnerNode
	// For simplicity, we decode to JSON, update OwnerNode, and re-encode.
	hexStr := hex.EncodeToString(data)
	jsonObj, err := decodeBinary(hexStr)
	if err != nil {
		return err
	}
	jsonObj["OwnerNode"] = fmt.Sprintf("%016X", page)

	newData, err := encodeBinary(jsonObj)
	if err != nil {
		return err
	}

	return ctx.View.Update(k, newData)
}

// decodeBinary decodes binary ledger data to a JSON map.
func decodeBinary(hexStr string) (map[string]any, error) {
	return binarycodec.Decode(hexStr)
}

// encodeBinary encodes a JSON map to binary ledger data.
func encodeBinary(jsonObj map[string]any) ([]byte, error) {
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(hexStr)
}

// boolToInt converts a bool to 0 or 1.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package depositpreauth

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/keylet"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

func (d *DepositPreauth) TxType() tx.Type {
	return tx.TypeDepositPreauth
}

func (d *DepositPreauth) RequiredAmendments() [][32]byte {
	return nil
}

// CheckExtraFeatures gates the credential-based forms on the Credentials
// amendment. Presence is keyed on the field, not its length, so a present but
// empty AuthorizeCredentials/UnauthorizeCredentials array with the amendment
// disabled is temDISABLED — the same NotTEC rippled returns from
// checkExtraFeatures, before preflight1's common checks and before the
// temARRAY_EMPTY body check.
func (d *DepositPreauth) CheckExtraFeatures(rules *amendment.Rules) error {
	if (d.hasAuthorizeCredentials() || d.hasUnauthorizeCredentials()) &&
		!rules.Enabled(amendment.FeatureCredentials) {
		return ter.Errorf(ter.TemDISABLED, "credentials require the Credentials amendment")
	}
	return nil
}

// GetFlagsMask reports the invalid-flag mask. DepositPreauth defines no
// type-specific flags, so only the universal bits (tfFullyCanonicalSig,
// tfInnerBatchTxn) are permitted — matching rippled, which leaves getFlagsMask
// at its tfUniversalMask default. The engine rejects flags intersecting the
// mask at preflight0.
func (d *DepositPreauth) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled DepositPreauth::preflight()
func (d *DepositPreauth) Validate() error {
	if err := d.BaseTx.Validate(); err != nil {
		return err
	}

	// Count which fields are present.
	// Empty arrays are still present for mutual exclusivity.
	hasAuth := d.hasAuthorize()
	hasUnauth := d.hasUnauthorize()
	hasAuthCreds := d.hasAuthorizeCredentials()
	hasUnauthCreds := d.hasUnauthorizeCredentials()

	authPresent := boolToInt(hasAuth) + boolToInt(hasUnauth)
	authCredPresent := boolToInt(hasAuthCreds) + boolToInt(hasUnauthCreds)

	// Exactly one of the 4 fields must be present
	// Reference: rippled preflight() - authPresent + authCredPresent != 1
	if authPresent+authCredPresent != 1 {
		return ter.Errorf(ter.TemMALFORMED, "Invalid Authorize and Unauthorize field combination")
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
			return ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "Authorized or Unauthorized field invalid")
		}
		if targetID == [20]byte{} {
			return ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "Authorized or Unauthorized field zeroed")
		}

		// Cannot preauthorize self (only checked for Authorize, not Unauthorize)
		// Reference: rippled preflight() - optAuth && target == ctx.tx[sfAccount]
		if hasAuth && target == d.Account {
			return ter.Errorf(ter.TemCAN_NOT_PREAUTH_SELF, "Attempting to DepositPreauth self")
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
		return ter.Errorf(ter.TemARRAY_EMPTY, "Invalid credentials size: 0")
	}
	if len(creds) > maxCredentialsArraySize {
		return ter.Errorf(ter.TemARRAY_TOO_LARGE, "Invalid credentials size: %d", len(creds))
	}

	// Check each credential and detect duplicates
	duplicates := make(map[[32]byte]bool)
	for _, cw := range creds {
		c := cw.Credential

		// Validate issuer
		issuerID, err := state.DecodeAccountID(c.Issuer)
		if err != nil || issuerID == ([20]byte{}) {
			return ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "Issuer account is invalid")
		}

		// Validate credential type (hex-encoded, 1-64 raw bytes)
		credTypeBytes, err := hex.DecodeString(c.CredentialType)
		if err != nil || len(credTypeBytes) == 0 || len(credTypeBytes) > maxCredentialTypeLength {
			return ter.Errorf(ter.TemMALFORMED, "Invalid credentialType size")
		}

		// Check for duplicates using sha512Half(issuer, credType)
		hash := sha512half.Sum(issuerID[:], credTypeBytes)
		if duplicates[hash] {
			return ter.Errorf(ter.TemMALFORMED, "duplicates in credentials")
		}
		duplicates[hash] = true
	}

	return nil
}

func (d *DepositPreauth) Flatten() (map[string]any, error) {
	fields, err := tx.ReflectFlatten(d)
	if err != nil {
		return nil, err
	}
	if len(d.AuthorizeCredentials) == 0 && d.hasAuthorizeCredentials() {
		fields["AuthorizeCredentials"] = []map[string]any{}
	}
	if len(d.UnauthorizeCredentials) == 0 && d.hasUnauthorizeCredentials() {
		fields["UnauthorizeCredentials"] = []map[string]any{}
	}
	return fields, nil
}

func (d *DepositPreauth) hasAuthorizeCredentials() bool {
	return d.FieldPresent("AuthorizeCredentials", d.AuthorizeCredentials != nil)
}

func (d *DepositPreauth) hasUnauthorizeCredentials() bool {
	return d.FieldPresent("UnauthorizeCredentials", d.UnauthorizeCredentials != nil)
}

func (d *DepositPreauth) hasAuthorize() bool {
	return d.FieldPresent("Authorize", d.Authorize != "")
}

func (d *DepositPreauth) hasUnauthorize() bool {
	return d.FieldPresent("Unauthorize", d.Unauthorize != "")
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

// Preclaim runs the ledger-aware existence checks for the four mutually
// exclusive forms, matching rippled DepositPreauth::preclaim's order: the
// Authorize target must exist (tecNO_TARGET) and its preauth entry must not
// (tecDUPLICATE); an Unauthorize target's preauth entry must exist (tecNO_ENTRY);
// every AuthorizeCredentials issuer must exist (tecNO_ISSUER) and the entry must
// not (tecDUPLICATE); an UnauthorizeCredentials entry must exist (tecNO_ENTRY).
// The reserve check and mutation stay in Apply (rippled doApply).
// Reference: rippled DepositPreauth::preclaim.
func (d *DepositPreauth) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(d.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	switch {
	case d.hasAuthorize():
		authorizedID, derr := tx.DecodeAccountIDField(d.Authorize, true)
		if derr != nil {
			return ter.TemINVALID
		}
		exists, err := view.Exists(keylet.Account(authorizedID))
		if err != nil {
			return ter.TefEXCEPTION
		}
		if !exists {
			return ter.TecNO_TARGET
		}
		if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_3_0) && tx.IsPseudoAccountID(view, authorizedID) {
			return ter.TecPSEUDO_ACCOUNT
		}
		exists, err = view.Exists(keylet.DepositPreauth(accountID, authorizedID))
		if err != nil {
			return ter.TefEXCEPTION
		}
		if exists {
			return ter.TecDUPLICATE
		}
	case d.hasUnauthorize():
		unauthorizedID, derr := tx.DecodeAccountIDField(d.Unauthorize, true)
		if derr != nil {
			return ter.TemINVALID
		}
		exists, err := view.Exists(keylet.DepositPreauth(accountID, unauthorizedID))
		if err != nil {
			return ter.TefEXCEPTION
		}
		if !exists {
			return ter.TecNO_ENTRY
		}
	case d.hasAuthorizeCredentials():
		sorted := makeSorted(d.AuthorizeCredentials)
		if sorted == nil {
			return ter.TefINTERNAL
		}
		for _, p := range sorted {
			exists, err := view.Exists(keylet.Account(p.issuer))
			if err != nil {
				return ter.TefEXCEPTION
			}
			if !exists {
				return ter.TecNO_ISSUER
			}
		}
		exists, err := view.Exists(keylet.DepositPreauthCredentials(accountID, toKeyletPairs(sorted)))
		if err != nil {
			return ter.TefEXCEPTION
		}
		if exists {
			return ter.TecDUPLICATE
		}
	case d.hasUnauthorizeCredentials():
		sorted := makeSorted(d.UnauthorizeCredentials)
		if sorted == nil {
			return ter.TefINTERNAL
		}
		exists, err := view.Exists(keylet.DepositPreauthCredentials(accountID, toKeyletPairs(sorted)))
		if err != nil {
			return ter.TefEXCEPTION
		}
		if !exists {
			return ter.TecNO_ENTRY
		}
	}
	return ter.TesSUCCESS
}

// Apply performs the ledger mutation (rippled DepositPreauth::doApply). The
// existence checks live in Preclaim.
func (d *DepositPreauth) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("deposit preauth apply",
		"account", d.Account,
		"authorize", d.Authorize,
		"unauthorize", d.Unauthorize,
		"authorizeCredentials", len(d.AuthorizeCredentials),
		"unauthorizeCredentials", len(d.UnauthorizeCredentials),
	)

	if d.hasAuthorize() {
		return d.applyAuthorize(ctx)
	} else if d.hasUnauthorize() {
		return d.applyUnauthorize(ctx)
	} else if d.hasAuthorizeCredentials() {
		return d.applyAuthorizeCredentials(ctx)
	} else if d.hasUnauthorizeCredentials() {
		return d.applyUnauthorizeCredentials(ctx)
	}
	return ter.TemMALFORMED
}

// applyAuthorize handles the Authorize case.
// Reference: rippled DepositPreauth preclaim(sfAuthorize) + doApply(sfAuthorize)
func (d *DepositPreauth) applyAuthorize(ctx *tx.ApplyContext) ter.Result {
	authorizedID, err := tx.DecodeAccountIDField(d.Authorize, d.hasAuthorize())
	if err != nil {
		return ter.TemINVALID
	}

	preauthKey := keylet.DepositPreauth(ctx.AccountID, authorizedID)

	// Check reserve using the prior balance (before the actual fee was
	// deducted), matching rippled's mPriorBalance comparison.
	if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, ctx.PriorBalance(), 1, 0, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
		ctx.Log.Warn("deposit preauth authorize: insufficient reserve")
		return result
	}

	// Insert into the owner directory first so OwnerNode records the page the
	// entry actually landed on.
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, preauthKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("DepositPreauth.Authorize.DirInsert", err)
	}

	// --- doApply: create and insert preauth entry ---
	preauthData, err := state.SerializeDepositPreauth(ctx.AccountID, authorizedID, dirResult.Page)
	if err != nil {
		ctx.Log.Error("deposit preauth authorize: failed to serialize preauth entry", "error", err)
		return ter.TefINTERNAL
	}

	sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
	if result != ter.TesSUCCESS {
		return result
	}
	preauthData, err = tx.SetLedgerEntrySponsor(preauthData, "Sponsor", sponsorAddress)
	if err != nil {
		return ctx.Internal("DepositPreauth.Authorize.SetSponsor", err)
	}
	if err := ctx.View.Insert(preauthKey, preauthData); err != nil {
		ctx.Log.Error("deposit preauth authorize: failed to insert preauth entry", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// applyUnauthorize handles the Unauthorize case.
// Reference: rippled DepositPreauth preclaim(sfUnauthorize) + doApply(sfUnauthorize)
func (d *DepositPreauth) applyUnauthorize(ctx *tx.ApplyContext) ter.Result {
	unauthorizedID, err := tx.DecodeAccountIDField(d.Unauthorize, d.hasUnauthorize())
	if err != nil {
		return ter.TemINVALID
	}

	preauthKey := keylet.DepositPreauth(ctx.AccountID, unauthorizedID)
	return removeFromLedger(ctx, preauthKey)
}

// applyAuthorizeCredentials handles the AuthorizeCredentials case.
// Reference: rippled DepositPreauth preclaim(sfAuthorizeCredentials) + doApply(sfAuthorizeCredentials)
func (d *DepositPreauth) applyAuthorizeCredentials(ctx *tx.ApplyContext) ter.Result {
	sorted := makeSorted(d.AuthorizeCredentials)
	if sorted == nil {
		return ter.TefINTERNAL
	}
	preauthKey := keylet.DepositPreauthCredentials(ctx.AccountID, toKeyletPairs(sorted))

	// Check reserve using the prior balance (before the actual fee was
	// deducted), matching rippled's mPriorBalance comparison.
	if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, ctx.PriorBalance(), 1, 0, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
		ctx.Log.Warn("deposit preauth authorize credentials: insufficient reserve")
		return result
	}

	// Insert into the owner directory first so OwnerNode records the page the
	// entry actually landed on.
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, preauthKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("DepositPreauth.AuthorizeCredentials.DirInsert", err)
	}

	// --- doApply: create and insert preauth entry with sorted credentials ---
	sleCreds := make([]state.DepositPreauthCredential, len(sorted))
	for i, p := range sorted {
		addr, err := addresscodec.EncodeAccountIDToClassicAddress(p.issuer[:])
		if err != nil {
			ctx.Log.Error("deposit preauth authorize credentials: failed to encode issuer address", "error", err)
			return ter.TefINTERNAL
		}
		sleCreds[i] = state.DepositPreauthCredential{
			Issuer:         addr,
			CredentialType: hex.EncodeToString(p.credType),
		}
	}

	preauthData, err := state.SerializeDepositPreauthCredentials(ctx.AccountID, sleCreds, dirResult.Page)
	if err != nil {
		ctx.Log.Error("deposit preauth authorize credentials: failed to serialize preauth entry", "error", err)
		return ter.TefINTERNAL
	}

	sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
	if result != ter.TesSUCCESS {
		return result
	}
	preauthData, err = tx.SetLedgerEntrySponsor(preauthData, "Sponsor", sponsorAddress)
	if err != nil {
		return ctx.Internal("DepositPreauth.AuthorizeCredentials.SetSponsor", err)
	}
	if err := ctx.View.Insert(preauthKey, preauthData); err != nil {
		ctx.Log.Error("deposit preauth authorize credentials: failed to insert preauth entry", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// applyUnauthorizeCredentials handles the UnauthorizeCredentials case.
// Reference: rippled DepositPreauth preclaim(sfUnauthorizeCredentials) + doApply(sfUnauthorizeCredentials)
func (d *DepositPreauth) applyUnauthorizeCredentials(ctx *tx.ApplyContext) ter.Result {
	sorted := makeSorted(d.UnauthorizeCredentials)
	if sorted == nil {
		return ter.TefINTERNAL
	}
	preauthKey := keylet.DepositPreauthCredentials(ctx.AccountID, toKeyletPairs(sorted))
	return removeFromLedger(ctx, preauthKey)
}

// removeFromLedger removes a deposit preauth entry from the ledger.
// Reads the entry to find OwnerNode, removes from owner directory,
// adjusts owner count, and erases the entry.
// Reference: rippled DepositPreauth::removeFromLedger()
func removeFromLedger(ctx *tx.ApplyContext, preauthKey keylet.Keylet) ter.Result {
	// Read the preauth entry to get OwnerNode for directory removal
	preauthData, err := ctx.View.Read(preauthKey)
	if err != nil {
		return ctx.Internal("DepositPreauth.Remove.Read", err)
	}
	if preauthData == nil {
		ctx.Log.Warn("deposit preauth remove: entry not found in ledger")
		return ter.TecNO_ENTRY
	}

	entry, err := state.ParseDepositPreauth(preauthData)
	if err != nil || entry == nil {
		ctx.Log.Error("deposit preauth remove: malformed ledger entry", "error", err)
		return ter.TefEXCEPTION
	}
	if entry.Account != ctx.AccountID {
		ctx.Log.Error("deposit preauth remove: ledger entry owner mismatch")
		return ter.TefBAD_LEDGER
	}
	if ctx.Account == nil {
		return ctx.Internal("DepositPreauth.Remove.Owner", errors.New("owner account missing"))
	}
	sponsorAddress, err := tx.LedgerEntrySponsor(preauthData, "Sponsor")
	if err != nil {
		return ctx.Internal("DepositPreauth.Remove.Sponsor", err)
	}
	if ctx.Account.OwnerCount == 0 {
		ctx.Log.Error("deposit preauth remove: owner count underflow")
	}

	ownerDirKey := keylet.OwnerDir(entry.Account)
	res, err := state.DirRemove(ctx.View, ownerDirKey, entry.OwnerNode, preauthKey.Key, false)
	if err != nil || !res.Success {
		ctx.Log.Error("deposit preauth remove: failed to remove from owner directory", "error", err)
		return ter.TefBAD_LEDGER
	}

	if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, sponsorAddress, 1); err != nil {
		return ctx.Internal("DepositPreauth.Remove.OwnerCount", err)
	}

	// Erase the entry
	if err := ctx.View.Erase(preauthKey); err != nil {
		ctx.Log.Error("deposit preauth remove: failed to erase entry", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// boolToInt converts a bool to 0 or 1.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

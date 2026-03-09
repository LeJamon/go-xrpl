package payment

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"strconv"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/LeJamon/goXRPLd/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypePayment, func() tx.Transaction {
		return &Payment{BaseTx: *tx.NewBaseTx(tx.TypePayment, "")}
	})
}

// checkTrustLineAuthorization checks if a trust line is authorized when the issuer requires auth.
// Reference: rippled DirectStep.cpp:417-430
//
// Parameters:
//   - view: ledger view to read account and trust line data
//   - issuerID: the issuer account ID
//   - holderID: the holder (non-issuer) account ID
//   - trustLine: the parsed RippleState (trust line) object
//
// Returns terNO_AUTH if:
//   - The issuer has lsfRequireAuth flag set, AND
//   - The trust line doesn't have the appropriate auth flag set, AND
//   - The trust line balance is zero (new relationship)
//
// Returns tesSUCCESS if authorized or if auth not required.
func checkTrustLineAuthorization(view tx.LedgerView, issuerID, holderID [20]byte, trustLine *state.RippleState) tx.Result {
	// Read the issuer's account to check for lsfRequireAuth
	issuerKey := keylet.Account(issuerID)
	issuerData, err := view.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TefINTERNAL
	}

	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If issuer doesn't require auth, always authorized
	if (issuerAccount.Flags & state.LsfRequireAuth) == 0 {
		return tx.TesSUCCESS
	}

	// Issuer requires auth - check if the trust line is authorized
	// The auth flag depends on account ordering in the trust line
	// Reference: rippled DirectStep.cpp:420
	// auto const authField = (src_ > dst_) ? lsfHighAuth : lsfLowAuth;
	var authFlag uint32
	if state.CompareAccountIDs(issuerID, holderID) > 0 {
		// Issuer is HIGH, holder is LOW - need lsfHighAuth
		authFlag = state.LsfHighAuth
	} else {
		// Issuer is LOW, holder is HIGH - need lsfLowAuth
		authFlag = state.LsfLowAuth
	}

	// Check if trust line has the auth flag
	if (trustLine.Flags & authFlag) != 0 {
		return tx.TesSUCCESS
	}

	// Trust line is not authorized - only block if balance is zero
	// Reference: rippled DirectStep.cpp:424
	// !((*sleLine)[sfFlags] & authField) && (*sleLine)[sfBalance] == beast::zero
	if trustLine.Balance.IsZero() {
		return tx.TerNO_AUTH
	}

	// Non-zero balance means existing relationship, allow it
	return tx.TesSUCCESS
}

// Payment transaction moves value from one account to another.
// It is the most fundamental transaction type in the XRPL.
type Payment struct {
	tx.BaseTx

	// Amount is the amount of currency to deliver (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the account receiving the payment (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// InvoiceID is a 256-bit hash for identifying this payment (optional)
	InvoiceID string `json:"InvoiceID,omitempty" xrpl:"InvoiceID,omitempty"`

	// Paths for cross-currency payments (optional)
	Paths [][]PathStep `json:"Paths,omitempty" xrpl:"Paths,omitempty"`

	// SendMax is the maximum amount to send (optional, for cross-currency)
	SendMax *tx.Amount `json:"SendMax,omitempty" xrpl:"SendMax,omitempty,amount"`

	// DeliverMin is the minimum amount to deliver (optional, for partial payments)
	DeliverMin *tx.Amount `json:"DeliverMin,omitempty" xrpl:"DeliverMin,omitempty,amount"`

	// CredentialIDs is a list of credential ledger entry IDs (uint256 hashes as hex strings)
	// used to authorize the payment when the destination requires deposit preauthorization
	// via credentials.
	// Reference: rippled sfCredentialIDs
	CredentialIDs []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`

	// MPTokenIssuanceID is the issuance ID for MPT direct payments (optional).
	// When set, the payment follows the MPT direct path instead of IOU trust line path.
	MPTokenIssuanceID string `json:"MPTokenIssuanceID,omitempty" xrpl:"MPTokenIssuanceID,omitempty"`

	// DomainID is the permissioned domain for this payment (optional).
	// When set, only offers within the specified domain are consumed on the payment path.
	// Both sender and destination must be members of the domain.
	// Requires FeaturePermissionedDEX amendment.
	// Reference: rippled Payment.cpp sfDomainID
	DomainID *string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`
}

// PathStep represents a single step in a payment path
type PathStep struct {
	Account  string `json:"account,omitempty"`
	Currency string `json:"currency,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
	Type     int    `json:"type,omitempty"`
	TypeHex  string `json:"type_hex,omitempty"`
}

// Payment flags
const (
	// tfNoDirectRipple prevents direct rippling (tfNoRippleDirect in rippled)
	PaymentFlagNoDirectRipple uint32 = 0x00010000
	// tfPartialPayment allows partial payments
	PaymentFlagPartialPayment uint32 = 0x00020000
	// tfLimitQuality limits quality of paths
	PaymentFlagLimitQuality uint32 = 0x00040000
)

// Path constraints matching rippled
const (
	// MaxPathSize is the maximum number of paths in a payment (rippled: MaxPathSize = 7)
	MaxPathSize = 7
	// MaxPathLength is the maximum number of steps per path (rippled: MaxPathLength = 8)
	MaxPathLength = 8
)

// maxCredentialsArraySize is the maximum number of credential IDs allowed.
// Reference: rippled Protocol.h maxCredentialsArraySize = 8
const maxCredentialsArraySize = 8

// NewPayment creates a new Payment transaction
func NewPayment(account, destination string, amount tx.Amount) *Payment {
	return &Payment{
		BaseTx:      *tx.NewBaseTx(tx.TypePayment, account),
		Amount:      amount,
		Destination: destination,
	}
}

// TxType returns the transaction type
func (p *Payment) TxType() tx.Type {
	return tx.TypePayment
}

// RequiredAmendments returns amendments required for this transaction.
// Reference: rippled Payment.cpp preflight() - featureCredentials check for sfCredentialIDs
func (p *Payment) RequiredAmendments() [][32]byte {
	var amendments [][32]byte
	if p.MPTokenIssuanceID != "" {
		amendments = append(amendments, amendment.FeatureMPTokensV1)
	}
	if p.CredentialIDs != nil {
		amendments = append(amendments, amendment.FeatureCredentials)
	}
	if p.DomainID != nil {
		amendments = append(amendments, amendment.FeaturePermissionedDEX)
	}
	return amendments
}

// Validate validates the payment transaction
// Reference: rippled Payment.cpp preflight() function
func (p *Payment) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	if p.Destination == "" {
		return errors.New("temDST_NEEDED: Destination is required")
	}

	if p.Amount.IsZero() {
		return errors.New("temBAD_AMOUNT: Amount is required")
	}

	// Determine if this is an MPT direct payment
	mptDirect := p.MPTokenIssuanceID != ""

	// Determine if this is an XRP-to-XRP (direct) payment
	// Reference: rippled Payment.cpp:129
	xrpDirect := p.Amount.IsNative() && (p.SendMax == nil || p.SendMax.IsNative())

	// Check flags based on payment type
	flags := p.GetFlags()

	// MPT payments use a stricter flag mask (no tfNoRippleDirect, no tfLimitQuality)
	// Reference: rippled Payment.cpp:93-99 tfMPTPaymentMask
	if mptDirect {
		// tfMPTPaymentMask = ~(tfUniversal | tfPartialPayment)
		// Only tfPartialPayment is allowed for MPT payments (beyond universal flags)
		mptPaymentMask := ^(tx.TfUniversal | PaymentFlagPartialPayment)
		if flags&mptPaymentMask != 0 {
			return errors.New("temINVALID_FLAG: Invalid flags for MPT payment")
		}
	}

	partialPaymentAllowed := (flags & PaymentFlagPartialPayment) != 0
	limitQuality := (flags & PaymentFlagLimitQuality) != 0
	noRippleDirect := (flags & PaymentFlagNoDirectRipple) != 0
	hasPaths := len(p.Paths) > 0

	// MPT payments cannot have paths
	// Reference: rippled Payment.cpp:101-102
	if mptDirect && hasPaths {
		return errors.New("temMALFORMED: Paths not allowed for MPT payment")
	}

	// Cannot send to self with same source/destination asset (temREDUNDANT)
	// Reference: rippled Payment.cpp:126-127,159-167
	// srcAsset = maxSourceAmount.asset() (SendMax if set, else Amount)
	// dstAsset = dstAmount.asset()
	// Only redundant if equalTokens(srcAsset, dstAsset) — same currency+issuer
	srcAmount := p.Amount
	if p.SendMax != nil {
		srcAmount = *p.SendMax
	}
	equalTokens := (srcAmount.IsNative() && p.Amount.IsNative()) ||
		(!srcAmount.IsNative() && !p.Amount.IsNative() &&
			srcAmount.Currency == p.Amount.Currency &&
			srcAmount.Issuer == p.Amount.Issuer)
	if mptDirect {
		equalTokens = true // MPT direct: src and dst are same issuance
	}
	if p.Account == p.Destination && equalTokens && !hasPaths {
		return errors.New("temREDUNDANT: cannot send to self without path")
	}

	// XRP to XRP with SendMax is invalid (temBAD_SEND_XRP_MAX)
	// Reference: rippled Payment.cpp:168-174
	if xrpDirect && p.SendMax != nil {
		return errors.New("temBAD_SEND_XRP_MAX: SendMax specified for XRP to XRP")
	}

	// XRP to XRP with paths is invalid (temBAD_SEND_XRP_PATHS)
	// Reference: rippled Payment.cpp:175-181
	if xrpDirect && hasPaths {
		return errors.New("temBAD_SEND_XRP_PATHS: Paths specified for XRP to XRP")
	}

	// tfPartialPayment flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_PARTIAL)
	// Reference: rippled Payment.cpp:182-188
	if xrpDirect && partialPaymentAllowed {
		return errors.New("temBAD_SEND_XRP_PARTIAL: Partial payment specified for XRP to XRP")
	}

	// tfLimitQuality flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_LIMIT)
	// Reference: rippled Payment.cpp:189-196
	if xrpDirect && limitQuality {
		return errors.New("temBAD_SEND_XRP_LIMIT: Limit quality specified for XRP to XRP")
	}

	// tfNoRippleDirect flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_NO_DIRECT)
	// Reference: rippled Payment.cpp:197-204
	if xrpDirect && noRippleDirect {
		return errors.New("temBAD_SEND_XRP_NO_DIRECT: No ripple direct specified for XRP to XRP")
	}

	// DeliverMin can only be used with tfPartialPayment flag (temBAD_AMOUNT)
	// Reference: rippled Payment.cpp:206-214
	if p.DeliverMin != nil && !partialPaymentAllowed {
		return errors.New("temBAD_AMOUNT: DeliverMin requires tfPartialPayment flag")
	}

	// Validate DeliverMin if present
	// Reference: rippled Payment.cpp:216-238
	if p.DeliverMin != nil {
		// DeliverMin must be positive (not zero, not negative)
		if p.DeliverMin.IsZero() || p.DeliverMin.IsNegative() {
			return errors.New("temBAD_AMOUNT: DeliverMin must be positive")
		}

		// DeliverMin currency must match Amount currency
		if p.DeliverMin.Currency != p.Amount.Currency || p.DeliverMin.Issuer != p.Amount.Issuer {
			return errors.New("temBAD_AMOUNT: DeliverMin currency must match Amount")
		}

		// DeliverMin cannot exceed Amount
		// Reference: rippled Payment.cpp:232-238
		if p.DeliverMin.Compare(p.Amount) > 0 {
			return errors.New("temBAD_AMOUNT: DeliverMin cannot exceed Amount")
		}
	}

	// Paths array max length is 7 (temMALFORMED if exceeded)
	// Reference: rippled Payment.cpp:353-359 (MaxPathSize)
	if len(p.Paths) > MaxPathSize {
		return errors.New("temMALFORMED: Paths array exceeds maximum size of 7")
	}

	// Each path can have max 8 steps (temMALFORMED if exceeded)
	// Reference: rippled Payment.cpp:354-358 (MaxPathLength)
	for i, path := range p.Paths {
		if len(path) > MaxPathLength {
			return errors.New("temMALFORMED: Path " + string(rune('0'+i)) + " exceeds maximum length of 8 steps")
		}
	}

	// Validate path elements
	// Reference: rippled PaySteps.cpp:157-186
	if err := p.validatePathElements(); err != nil {
		return err
	}

	// Validate CredentialIDs field
	// Reference: rippled credentials::checkFields() in CredentialHelpers.cpp
	if p.CredentialIDs != nil {
		if len(p.CredentialIDs) == 0 || len(p.CredentialIDs) > maxCredentialsArraySize {
			return errors.New("temMALFORMED: Invalid credentials array size")
		}

		// Check for duplicates
		seen := make(map[string]bool, len(p.CredentialIDs))
		for _, id := range p.CredentialIDs {
			if seen[id] {
				return errors.New("temMALFORMED: Duplicate credential ID")
			}
			seen[id] = true
		}
	}

	return nil
}

// validatePathElements validates individual path elements
// Reference: rippled PaySteps.cpp toStrand() lines 157-186
func (p *Payment) validatePathElements() error {
	for _, path := range p.Paths {
		for _, elem := range path {
			// Determine what the element has
			hasAccount := elem.Account != ""
			hasCurrency := elem.Currency != ""
			hasIssuer := elem.Issuer != ""

			// Calculate element type
			elemType := 0
			if hasAccount {
				elemType |= int(PathTypeAccount)
			}
			if hasCurrency {
				elemType |= int(PathTypeCurrency)
			}
			if hasIssuer {
				elemType |= int(PathTypeIssuer)
			}

			// Path element with type zero is invalid
			// Reference: rippled PaySteps.cpp:161 - if ((t & ~STPathElement::typeAll) || !t)
			if elemType == 0 {
				return errors.New("temBAD_PATH: Path element has no account, currency, or issuer")
			}

			// Account element cannot also have currency or issuer
			// Reference: rippled PaySteps.cpp:168-169
			if hasAccount && (hasCurrency || hasIssuer) {
				return errors.New("temBAD_PATH: Path element has account with currency or issuer")
			}

			// XRP issuer is invalid (issuer must not be XRP pseudo-account)
			// Reference: rippled PaySteps.cpp:171-172
			if hasIssuer && (elem.Issuer == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || elem.Issuer == "" && hasCurrency && elem.Currency == "XRP") {
				return errors.New("temBAD_PATH: Path element has XRP issuer")
			}

			// XRP account in path is invalid (account must not be XRP pseudo-account)
			// Reference: rippled PaySteps.cpp:174-175
			if hasAccount && elem.Account == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" {
				return errors.New("temBAD_PATH: Path element has XRP account")
			}

			// XRP currency with non-XRP issuer or vice versa is invalid
			// Reference: rippled PaySteps.cpp:177-179
			if hasCurrency && hasIssuer {
				isXRPCurrency := elem.Currency == "XRP" || elem.Currency == ""
				isXRPIssuer := elem.Issuer == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || elem.Issuer == ""
				if isXRPCurrency != isXRPIssuer {
					return errors.New("temBAD_PATH: XRP currency mismatch with issuer")
				}
			}
		}
	}
	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *Payment) Flatten() (map[string]any, error) {
	m, err := tx.ReflectFlatten(p)
	if err != nil {
		return nil, err
	}

	// Convert Paths from [][]PathStep to []any for serialization
	if len(p.Paths) > 0 {
		pathSet := make([]any, len(p.Paths))
		for i, path := range p.Paths {
			pathSteps := make([]any, len(path))
			for j, step := range path {
				stepMap := make(map[string]any)
				if step.Account != "" {
					stepMap["account"] = step.Account
				}
				if step.Currency != "" {
					stepMap["currency"] = step.Currency
				}
				if step.Issuer != "" {
					stepMap["issuer"] = step.Issuer
				}
				pathSteps[j] = stepMap
			}
			pathSet[i] = pathSteps
		}
		m["Paths"] = pathSet
	}

	return m, nil
}

// SetPartialPayment enables partial payment flag
func (p *Payment) SetPartialPayment() {
	flags := p.GetFlags() | PaymentFlagPartialPayment
	p.SetFlags(flags)
}

// SetNoDirectRipple enables no direct ripple flag
func (p *Payment) SetNoDirectRipple() {
	flags := p.GetFlags() | PaymentFlagNoDirectRipple
	p.SetFlags(flags)
}

// validateCredentials performs preclaim-level validation of CredentialIDs.
// Checks each credential exists in the ledger, belongs to the sender, and is accepted.
// Reference: rippled credentials::valid() in CredentialHelpers.cpp
func (p *Payment) validateCredentials(ctx *tx.ApplyContext) tx.Result {
	if len(p.CredentialIDs) == 0 {
		return tx.TesSUCCESS
	}

	for _, idHex := range p.CredentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TecBAD_CREDENTIALS
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TecBAD_CREDENTIALS
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		// Subject must be the transaction sender
		if cred.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		// Credential must be accepted
		if !cred.IsAccepted() {
			return tx.TecBAD_CREDENTIALS
		}
	}

	return tx.TesSUCCESS
}

// removeExpiredCredentials checks for expired credentials and deletes them.
// Returns true if any credentials were expired.
// Reference: rippled credentials::removeExpired() in CredentialHelpers.cpp
func (p *Payment) removeExpiredCredentials(ctx *tx.ApplyContext) bool {
	if len(p.CredentialIDs) == 0 {
		return false
	}

	closeTime := ctx.Config.ParentCloseTime
	anyExpired := false

	for _, idHex := range p.CredentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			continue
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			continue
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			continue
		}

		// Check expiration
		if cred.Expiration != nil && closeTime > *cred.Expiration {
			// Delete expired credential from ledger
			_ = credential.DeleteSLE(ctx.View, credKey, cred)
			anyExpired = true
		}
	}

	return anyExpired
}

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects persist.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (p *Payment) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	p.removeExpiredCredentials(ctx)
	return tx.TecEXPIRED
}

// authorizedDepositPreauth checks if the provided credentials match a
// credential-based DepositPreauth entry on the destination account.
// Reference: rippled credentials::authorizedDepositPreauth() in CredentialHelpers.cpp
func (p *Payment) authorizedDepositPreauth(ctx *tx.ApplyContext, dstAccountID [20]byte) tx.Result {
	// Read each credential, extract (Issuer, CredentialType) pairs
	type credPair struct {
		issuer   [20]byte
		credType []byte
	}
	pairs := make([]credPair, 0, len(p.CredentialIDs))

	seen := make(map[string]bool)
	for _, idHex := range p.CredentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TefINTERNAL
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Build a dedup key from (Issuer, CredentialType)
		pairKey := credentialPairKey(cred.Issuer, cred.CredentialType)
		if seen[pairKey] {
			return tx.TefINTERNAL
		}
		seen[pairKey] = true

		pairs = append(pairs, credPair{issuer: cred.Issuer, credType: cred.CredentialType})
	}

	// Sort pairs by (issuer, credType) to match keylet computation
	sort.Slice(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].issuer[:], pairs[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(pairs[i].credType, pairs[j].credType) < 0
	})

	// Convert to keylet.CredentialPair
	keyletPairs := make([]keylet.CredentialPair, len(pairs))
	for i, cp := range pairs {
		keyletPairs[i] = keylet.CredentialPair{
			Issuer:         cp.issuer,
			CredentialType: cp.credType,
		}
	}

	// Check if credential-based DepositPreauth exists for destination
	preauthKey := keylet.DepositPreauthCredentials(dstAccountID, keyletPairs)
	if exists, _ := ctx.View.Exists(preauthKey); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// credentialPairKey returns a unique string key for deduplication of (issuer, credType) pairs.
func credentialPairKey(issuer [20]byte, credType []byte) string {
	return hex.EncodeToString(issuer[:]) + ":" + hex.EncodeToString(credType)
}

// verifyDepositPreauth checks deposit authorization for a payment.
// Reference: rippled verifyDepositPreauth() in Payment.cpp
func (p *Payment) verifyDepositPreauth(ctx *tx.ApplyContext, srcAccountID, dstAccountID [20]byte, dstAccount *state.AccountRoot) tx.Result {
	credentialsPresent := len(p.CredentialIDs) > 0

	// Remove expired credentials first
	if credentialsPresent {
		if p.removeExpiredCredentials(ctx) {
			return tx.TecEXPIRED
		}
	}

	// Check if destination requires deposit authorization
	if dstAccount != nil && (dstAccount.Flags&state.LsfDepositAuth) != 0 {
		// Self-payments always allowed
		if srcAccountID != dstAccountID {
			// Try account-based DepositPreauth first
			preauthKey := keylet.DepositPreauth(dstAccountID, srcAccountID)
			if exists, _ := ctx.View.Exists(preauthKey); !exists {
				// Account-based preauth not found — try credential-based
				if !credentialsPresent {
					return tx.TecNO_PERMISSION
				}
				return p.authorizedDepositPreauth(ctx, dstAccountID)
			}
		}
	}

	return tx.TesSUCCESS
}

// Apply applies the Payment transaction to the ledger state.
func (p *Payment) Apply(ctx *tx.ApplyContext) tx.Result {
	// Domain membership checks for permissioned payments.
	// Reference: rippled Payment.cpp preclaim() sfDomainID checks
	if p.DomainID != nil {
		domainID, err := permissioneddomain.ParseDomainID(*p.DomainID)
		if err != nil {
			return tx.TemMALFORMED
		}
		closeTime := ctx.Config.ParentCloseTime
		senderID, err := state.DecodeAccountID(p.Account)
		if err != nil {
			return tx.TefINTERNAL
		}
		if !permissioneddomain.AccountInDomain(ctx.View, senderID, domainID, closeTime) {
			return tx.TecNO_PERMISSION
		}
		destID, err := state.DecodeAccountID(p.Destination)
		if err != nil {
			return tx.TefINTERNAL
		}
		if !permissioneddomain.AccountInDomain(ctx.View, destID, domainID, closeTime) {
			return tx.TecNO_PERMISSION
		}
	}

	// MPT direct payment
	if p.MPTokenIssuanceID != "" {
		return p.applyMPTPayment(ctx)
	}

	// Determine if this is a "ripple" payment (uses the flow engine).
	// Reference: rippled Payment.cpp:435-436:
	//   bool const ripple = (hasPaths || sendMax || !dstAmount.native()) && !mptDirect;
	hasPaths := len(p.Paths) > 0
	hasSendMax := p.SendMax != nil
	ripple := hasPaths || hasSendMax || !p.Amount.IsNative()

	if !ripple {
		// XRP-to-XRP direct payment (no paths, no SendMax, Amount is native)
		return p.applyXRPPayment(ctx)
	}

	// IOU / cross-currency payment - uses the flow engine
	return p.applyIOUPayment(ctx)
}

// applyMPTPayment applies an MPT direct payment.
// Reference: rippled Payment.cpp doApply() mptDirect path + View.cpp rippleSendMPT/rippleCreditMPT
func (p *Payment) applyMPTPayment(ctx *tx.ApplyContext) tx.Result {
	// Parse MPTokenIssuanceID
	issuanceIDBytes, err := hex.DecodeString(p.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return tx.TecOBJECT_NOT_FOUND
	}
	var mptID [24]byte
	copy(mptID[:], issuanceIDBytes)

	// Look up the issuance
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	issuerID := issuance.Issuer

	// Decode destination
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}

	// Check destination exists
	destKey := keylet.Account(destAccountID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// requireAuth: check sender is authorized
	// Reference: rippled Payment.cpp:518-520
	if issuance.Flags&entry.LsfMPTRequireAuth != 0 && ctx.AccountID != issuerID {
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, err := ctx.View.Read(senderTokenKey)
		if err != nil || senderTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		senderToken, err := state.ParseMPToken(senderTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if senderToken.Flags&entry.LsfMPTAuthorized == 0 {
			return tx.TecNO_AUTH
		}
	}

	// requireAuth: check destination is authorized
	// Reference: rippled Payment.cpp:522-524
	if issuance.Flags&entry.LsfMPTRequireAuth != 0 && destAccountID != issuerID {
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, err := ctx.View.Read(destTokenKey)
		if err != nil || destTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		destToken, err := state.ParseMPToken(destTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if destToken.Flags&entry.LsfMPTAuthorized == 0 {
			return tx.TecNO_AUTH
		}
	}

	// Verify deposit preauth
	// Reference: rippled Payment.cpp:531-539
	if result := p.verifyDepositPreauth(ctx, ctx.AccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
		return result
	}

	// Extract the payment amount as uint64
	dstAmount := mptAmountToUint64(p.Amount)
	if dstAmount == 0 {
		return tx.TemBAD_AMOUNT
	}

	senderIsIssuer := ctx.AccountID == issuerID
	destIsIssuer := destAccountID == issuerID

	// canTransfer: holder-to-holder requires CanTransfer flag
	// Reference: rippled Payment.cpp:526-529
	if !senderIsIssuer && !destIsIssuer {
		if issuance.Flags&entry.LsfMPTCanTransfer == 0 {
			return tx.TecNO_AUTH
		}
	}

	// Compute transfer rate for holder-to-holder transfers
	// Reference: rippled Payment.cpp:546-557, View.cpp transferRate()
	// rate is in QUALITY_ONE format: 1_000_000_000 = 1.0
	rate := uint64(qualityOne)
	if !senderIsIssuer && !destIsIssuer {
		// Check frozen (globally or individually locked)
		if issuance.Flags&entry.LsfMPTLocked != 0 {
			return tx.TecLOCKED
		}
		// Check individual locks on sender and destination
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, _ := ctx.View.Read(senderTokenKey)
		if senderTokenRaw != nil {
			senderToken, _ := state.ParseMPToken(senderTokenRaw)
			if senderToken != nil && senderToken.Flags&entry.LsfMPTLocked != 0 {
				return tx.TecLOCKED
			}
		}
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, _ := ctx.View.Read(destTokenKey)
		if destTokenRaw != nil {
			destToken, _ := state.ParseMPToken(destTokenRaw)
			if destToken != nil && destToken.Flags&entry.LsfMPTLocked != 0 {
				return tx.TecLOCKED
			}
		}

		// Transfer fee: rate = 1_000_000_000 + 10_000 * TransferFee
		if issuance.TransferFee > 0 {
			rate = qualityOne + 10_000*uint64(issuance.TransferFee)
		}
	}

	// maxSourceAmount: SendMax if present, otherwise dstAmount
	// Reference: rippled Payment.cpp:384-398 getMaxSourceAmount()
	maxSourceAmount := dstAmount
	if p.SendMax != nil {
		maxSourceAmount = mptAmountToUint64(*p.SendMax)
	}

	// Amount to deliver and required source amount factoring in transfer rate
	// Reference: rippled Payment.cpp:560-580
	amountDeliver := dstAmount
	requiredMaxSourceAmount := mptMultiply(dstAmount, rate)

	// Partial payment: if required exceeds maxSource, adjust amountDeliver
	isPartialPayment := p.GetFlags()&PaymentFlagPartialPayment != 0
	if isPartialPayment && requiredMaxSourceAmount > maxSourceAmount {
		requiredMaxSourceAmount = maxSourceAmount
		amountDeliver = mptDivide(maxSourceAmount, rate)
	}

	// Check: source insufficient
	if requiredMaxSourceAmount > maxSourceAmount {
		return tx.TecPATH_PARTIAL
	}

	// Check: DeliverMin not met
	if p.DeliverMin != nil {
		deliverMin := mptAmountToUint64(*p.DeliverMin)
		if deliverMin > 0 && amountDeliver < deliverMin {
			return tx.TecPATH_PARTIAL
		}
	}

	// Execute the actual transfer
	// Reference: rippled Payment.cpp:582-595
	var res tx.Result
	if senderIsIssuer || destIsIssuer {
		// Direct transfer (issuer involved, no transfer fee)
		res = p.mptDirectTransfer(ctx, issuance, issuanceKey, amountDeliver, senderIsIssuer, destIsIssuer, destAccountID)
	} else {
		// Transit through issuer (holder-to-holder, with transfer fee)
		res = p.mptTransitTransfer(ctx, issuance, issuanceKey, amountDeliver, rate, destAccountID)
	}

	// Map error codes per rippled Payment.cpp:593-594
	if res == tx.TecINSUFFICIENT_FUNDS || res == tx.TecPATH_DRY {
		res = tx.TecPATH_PARTIAL
	}

	return res
}

// mptDirectTransfer handles MPT payment where one party is the issuer.
// No transfer fee applies. Handles MaximumAmount enforcement.
func (p *Payment) mptDirectTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amount uint64, senderIsIssuer, destIsIssuer bool, destAccountID [20]byte) tx.Result {

	// If sender is issuer: check MaximumAmount
	// Reference: rippled View.cpp rippleSendMPT() lines 2044-2055
	if senderIsIssuer {
		maxAmount := uint64(maxMPTokenAmount)
		if issuance.MaximumAmount != nil {
			maxAmount = *issuance.MaximumAmount
		}
		if amount > maxAmount || issuance.OutstandingAmount > maxAmount-amount {
			return tx.TecPATH_DRY
		}
	}

	// rippleCreditMPT: sender side
	if senderIsIssuer {
		issuance.OutstandingAmount += amount
	} else {
		senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
		senderTokenRaw, err := ctx.View.Read(senderTokenKey)
		if err != nil || senderTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		senderToken, err := state.ParseMPToken(senderTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		if senderToken.MPTAmount < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		senderToken.MPTAmount -= amount
		updatedSenderToken, err := state.SerializeMPToken(senderToken)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
			return tx.TefINTERNAL
		}
	}

	// rippleCreditMPT: receiver side
	if destIsIssuer {
		if issuance.OutstandingAmount < amount {
			return tx.TefINTERNAL
		}
		issuance.OutstandingAmount -= amount
	} else {
		destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
		destTokenRaw, err := ctx.View.Read(destTokenKey)
		if err != nil || destTokenRaw == nil {
			return tx.TecNO_AUTH
		}
		destToken, err := state.ParseMPToken(destTokenRaw)
		if err != nil {
			return tx.TefINTERNAL
		}
		destToken.MPTAmount += amount
		updatedDestToken, err := state.SerializeMPToken(destToken)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Update issuance
	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// mptTransitTransfer handles holder-to-holder MPT payment via transit through issuer.
// Transfer fee is applied: sender pays amountDeliver * rate / QUALITY_ONE.
// Reference: rippled View.cpp rippleSendMPT() lines 2068-2085
func (p *Payment) mptTransitTransfer(ctx *tx.ApplyContext, issuance *state.MPTokenIssuanceData,
	issuanceKey keylet.Keylet, amountDeliver, rate uint64, destAccountID [20]byte) tx.Result {

	// Actual amount sender pays (includes transfer fee)
	saActual := mptMultiply(amountDeliver, rate)

	// Step 1: Credit receiver (issuer → receiver via rippleCreditMPT)
	// Outstanding increases by amountDeliver
	issuance.OutstandingAmount += amountDeliver

	destTokenKey := keylet.MPToken(issuanceKey.Key, destAccountID)
	destTokenRaw, err := ctx.View.Read(destTokenKey)
	if err != nil || destTokenRaw == nil {
		return tx.TecNO_AUTH
	}
	destToken, err := state.ParseMPToken(destTokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}
	destToken.MPTAmount += amountDeliver

	// Step 2: Debit sender (sender → issuer via rippleCreditMPT)
	// Outstanding decreases by saActual
	senderTokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)
	senderTokenRaw, err := ctx.View.Read(senderTokenKey)
	if err != nil || senderTokenRaw == nil {
		return tx.TecNO_AUTH
	}
	senderToken, err := state.ParseMPToken(senderTokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}
	if senderToken.MPTAmount < saActual {
		return tx.TecINSUFFICIENT_FUNDS
	}
	senderToken.MPTAmount -= saActual
	issuance.OutstandingAmount -= saActual

	// Net OutstandingAmount change: amountDeliver - saActual (negative, fee burned)

	// Serialize and update all modified entries
	updatedSenderToken, err := state.SerializeMPToken(senderToken)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(senderTokenKey, updatedSenderToken); err != nil {
		return tx.TefINTERNAL
	}

	updatedDestToken, err := state.SerializeMPToken(destToken)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(destTokenKey, updatedDestToken); err != nil {
		return tx.TefINTERNAL
	}

	updatedIssuance, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedIssuance); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

const (
	// qualityOne is the identity rate (1.0) in rippled's rate format
	qualityOne = 1_000_000_000
	// maxMPTokenAmount is the maximum MPT value (int64 max)
	maxMPTokenAmount = 0x7FFFFFFFFFFFFFFF
)

// mptMultiply multiplies amount by rate/QUALITY_ONE using big.Int to avoid overflow.
// Reference: rippled STAmount multiply() for MPT - "No rounding"
func mptMultiply(amount, rate uint64) uint64 {
	if rate == qualityOne {
		return amount
	}
	result := new(big.Int).Mul(
		new(big.Int).SetUint64(amount),
		new(big.Int).SetUint64(rate),
	)
	result.Div(result, new(big.Int).SetUint64(qualityOne))
	return result.Uint64()
}

// mptDivide divides amount by rate/QUALITY_ONE using big.Int to avoid overflow.
// Reference: rippled STAmount divide() for MPT - "No rounding"
func mptDivide(amount, rate uint64) uint64 {
	if rate == qualityOne {
		return amount
	}
	result := new(big.Int).Mul(
		new(big.Int).SetUint64(amount),
		new(big.Int).SetUint64(qualityOne),
	)
	result.Div(result, new(big.Int).SetUint64(rate))
	return result.Uint64()
}

// mptAmountToUint64 converts an Amount to a uint64 integer value.
// Prefers the raw MPT int64 value when available to avoid IOU normalization precision loss.
func mptAmountToUint64(a tx.Amount) uint64 {
	// Use raw MPT value if available (preserves precision for large values)
	if raw, ok := a.MPTRaw(); ok {
		if raw <= 0 {
			return 0
		}
		return uint64(raw)
	}
	// Fallback: reconstruct from IOU mantissa/exponent
	mantissa := a.Mantissa()
	if mantissa <= 0 {
		return 0
	}
	exp := a.Exponent()
	result := uint64(mantissa)
	for exp > 0 {
		result *= 10
		exp--
	}
	for exp < 0 {
		result /= 10
		exp++
	}
	return result
}

// applyXRPPayment applies an XRP-to-XRP payment
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp doApply() for XRP direct payments
func (p *Payment) applyXRPPayment(ctx *tx.ApplyContext) tx.Result {
	// Get the amount in drops
	drops := p.Amount.Drops()
	if drops <= 0 {
		return tx.TemBAD_AMOUNT
	}
	amountDrops := uint64(drops)

	// Parse the fee from the transaction
	feeDrops, err := strconv.ParseUint(p.Fee, 10, 64)
	if err != nil {
		feeDrops = ctx.Config.BaseFee // fallback to base fee if not specified
	}

	// IMPORTANT: sender.Balance has already had fee deducted (in doApply).
	// Rippled checks against mPriorBalance (balance BEFORE fee deduction).
	// We reconstruct the pre-fee balance for the check.
	// Reference: rippled Payment.cpp:619 - if (mPriorBalance < dstAmount.xrp() + mmm)
	priorBalance := ctx.Account.Balance + feeDrops

	// Calculate reserve as: ReserveBase + (ownerCount * ReserveIncrement)
	// This matches rippled's accountReserve(ownerCount) calculation
	reserve := ctx.Config.ReserveBase + (uint64(ctx.Account.OwnerCount) * ctx.Config.ReserveIncrement)

	// Use max(reserve, fee) as the minimum balance that must remain
	// This matches rippled's behavior: auto const mmm = std::max(reserve, ctx_.tx.getFieldAmount(sfFee).xrp())
	// Reference: rippled Payment.cpp:617
	mmm := reserve
	if feeDrops > mmm {
		mmm = feeDrops
	}

	// Check sender has enough balance using PRE-FEE balance
	// Reference: rippled Payment.cpp:619 - if (mPriorBalance < dstAmount.xrp() + mmm)
	if priorBalance < amountDrops+mmm {
		return tx.TecUNFUNDED_PAYMENT
	}

	// Get destination account
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}
	destKey := keylet.Account(destAccountID)

	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if destExists {
		// Destination exists - just credit the amount
		destData, err := ctx.View.Read(destKey)
		if err != nil {
			return tx.TefINTERNAL
		}

		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check for pseudo-account (AMM accounts cannot receive direct payments)
		// See rippled Payment.cpp:636-637: if (isPseudoAccount(sleDst)) return tecNO_PERMISSION
		if (destAccount.Flags & state.LsfAMM) != 0 {
			return tx.TecNO_PERMISSION
		}

		// Check destination's lsfDisallowXRP flag
		// Per rippled, if lsfDisallowXRP is set and sender != destination, return tecNO_TARGET
		// This allows accounts to indicate they don't want to receive XRP
		// Reference: this matches rippled behavior for direct XRP payments
		if (destAccount.Flags & state.LsfDisallowXRP) != 0 {
			senderAccountID, err := state.DecodeAccountID(ctx.Account.Account)
			if err != nil {
				return tx.TefINTERNAL
			}
			// Only reject if sender is not the destination (self-payments are allowed)
			if senderAccountID != destAccountID {
				return tx.TecNO_TARGET
			}
		}

		// Check if destination requires a tag
		if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
			return tx.TecDST_TAG_NEEDED
		}

		// Validate credentials (preclaim)
		if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
			return result
		}

		// Check deposit authorization
		// Reference: rippled Payment.cpp:641-677
		// XRP payments have a wedge-prevention exemption: if BOTH the payment amount
		// AND destination balance are <= base reserve, deposit preauth is NOT required.
		if (destAccount.Flags & state.LsfDepositAuth) != 0 {
			dstReserve := ctx.Config.ReserveBase

			if amountDrops > dstReserve || destAccount.Balance > dstReserve {
				if result := p.verifyDepositPreauth(ctx, ctx.AccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
					return result
				}
			}
		} else if len(p.CredentialIDs) > 0 {
			// Even without lsfDepositAuth, remove expired credentials if present
			if p.removeExpiredCredentials(ctx) {
				return tx.TecEXPIRED
			}
		}

		// Credit destination
		destAccount.Balance += amountDrops

		// Clear PasswordSpent flag if set (lsfPasswordSpent = 0x00010000)
		// Per rippled Payment.cpp:686-687, receiving XRP clears this flag
		if (destAccount.Flags & state.LsfPasswordSpent) != 0 {
			destAccount.Flags &^= state.LsfPasswordSpent
		}

		// Update PreviousTxnID and PreviousTxnLgrSeq on destination (thread the account)
		destAccount.PreviousTxnID = ctx.TxHash
		destAccount.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

		// Debit sender
		ctx.Account.Balance -= amountDrops

		// Update destination
		updatedDestData, err := state.SerializeAccountRoot(destAccount)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Update tracked automatically by ApplyStateTable
		if err := ctx.View.Update(destKey, updatedDestData); err != nil {
			return tx.TefINTERNAL
		}

		return tx.TesSUCCESS
	}

	// Destination doesn't exist - need to create it
	// Check minimum amount for account creation
	if amountDrops < ctx.Config.ReserveBase {
		return tx.TecNO_DST_INSUF_XRP
	}

	// Create new account
	// With featureDeletableAccounts enabled, new accounts start with sequence
	// equal to the current ledger sequence. Otherwise, sequence starts at 1.
	// (see rippled Payment.cpp:409-411)
	var accountSequence uint32
	if ctx.Rules().DeletableAccountsEnabled() {
		accountSequence = ctx.Config.LedgerSequence
	} else {
		accountSequence = 1
	}
	newAccount := &state.AccountRoot{
		Account:           p.Destination,
		Balance:           amountDrops,
		Sequence:          accountSequence,
		Flags:             0,
		PreviousTxnID:     ctx.TxHash,
		PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
	}

	// Debit sender
	ctx.Account.Balance -= amountDrops

	// Serialize and insert new account
	newAccountData, err := state.SerializeAccountRoot(newAccount)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(destKey, newAccountData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// applyIOUPayment applies an IOU (issued currency) or cross-currency payment.
// This is called for any payment with paths, SendMax, or non-native Amount.
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp
func (p *Payment) applyIOUPayment(ctx *tx.ApplyContext) tx.Result {
	// Validate the amount
	if p.Amount.IsZero() {
		return tx.TemBAD_AMOUNT
	}
	if p.Amount.IsNegative() {
		return tx.TemBAD_AMOUNT
	}

	// Get account IDs
	senderAccountID, err := state.DecodeAccountID(ctx.Account.Account)
	if err != nil {
		return tx.TefINTERNAL
	}

	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}

	// For cross-currency payments where Amount is XRP, we always need the flow engine
	// (no issuer to decode, no direct IOU path possible)
	if p.Amount.IsNative() {
		// Cross-currency: Amount=XRP with SendMax=IOU or paths
		// Always requires the flow engine
		return p.applyRipplePayment(ctx, senderAccountID, destAccountID)
	}

	issuerAccountID, err := state.DecodeAccountID(p.Amount.Issuer)
	if err != nil {
		return tx.TemBAD_ISSUER
	}

	// Use the tx.Amount directly (no conversion needed)
	amount := p.Amount

	// Reference: rippled Payment.cpp:435-436:
	// bool const ripple = (hasPaths || sendMax || !dstAmount.native()) && !mptDirect;
	// Since we're in the IOU branch (past IsNative() check), !dstAmount.native() is always
	// true, so ALL IOU payments go through the flow engine (RippleCalc).
	requiresPathFinding := true

	// Determine payment type: is this a direct payment to/from issuer?
	senderIsIssuer := senderAccountID == issuerAccountID
	destIsIssuer := destAccountID == issuerAccountID

	// Check destination exists (needed for DepositAuth check and destination flags)
	destKey := keylet.Account(destAccountID)
	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !destExists {
		return tx.TecNO_DST
	}

	// Get destination account to check flags
	destData, err := ctx.View.Read(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Validate credentials (preclaim)
	if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
		return result
	}

	// Check deposit authorization for IOU payments (including path-finding payments)
	// Reference: rippled Payment.cpp:429-464
	depositPreauth := ctx.Rules().Enabled(amendment.FeatureDepositPreauth)
	reqDepositAuth := (destAccount.Flags&state.LsfDepositAuth) != 0

	// Before DepositPreauth amendment: ALL ripple payments to accounts with
	// DepositAuth are blocked (including self-payments). This was a bug that
	// the DepositPreauth amendment fixed.
	// Reference: rippled Payment.cpp:440-441
	if !depositPreauth && reqDepositAuth {
		return tx.TecNO_PERMISSION
	}

	// With DepositPreauth amendment: self-payments and preauthorized accounts are allowed.
	if depositPreauth && reqDepositAuth {
		if result := p.verifyDepositPreauth(ctx, senderAccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
			return result
		}
	}

	// For path-finding payments, use the Flow Engine (RippleCalculate)
	if requiresPathFinding {
		return p.applyIOUPaymentWithPaths(ctx, senderAccountID, destAccountID, issuerAccountID)
	}

	// Determine if partial payment is allowed
	flags := p.GetFlags()
	partialPayment := (flags & PaymentFlagPartialPayment) != 0

	// Handle three cases:
	// 1. Sender is issuer - creating new tokens
	// 2. Destination is issuer - redeeming tokens
	// 3. Neither - transfer between accounts via trust lines

	var result tx.Result
	var deliveredAmount tx.Amount

	if senderIsIssuer {
		// Sender is issuing their own currency to destination
		// Need trust line from destination to sender (issuer)
		result, deliveredAmount = p.applyIOUIssueWithDelivered(ctx, destAccount, senderAccountID, destAccountID, amount, partialPayment)
	} else if destIsIssuer {
		// Destination is the issuer - sender is redeeming tokens
		// Need trust line from sender to destination (issuer)
		result, deliveredAmount = p.applyIOURedeemWithDelivered(ctx, destAccount, senderAccountID, destAccountID, amount, partialPayment)
	} else {
		// Neither is issuer - transfer between two non-issuer accounts
		// This requires trust lines from both parties to the issuer
		result, deliveredAmount = p.applyIOUTransferWithDelivered(ctx, destAccount, senderAccountID, destAccountID, issuerAccountID, amount, partialPayment)
	}

	// DeliverMin enforcement for partial payments
	// Reference: rippled Payment.cpp:496-500
	// If tfPartialPayment is set and DeliverMin is specified, check that delivered >= DeliverMin
	if result == tx.TesSUCCESS && p.DeliverMin != nil {
		flags := p.GetFlags()
		if (flags & PaymentFlagPartialPayment) != 0 {
			if deliveredAmount.Compare(*p.DeliverMin) < 0 {
				return tx.TecPATH_PARTIAL
			}
		}
	}

	return result
}

// applyRipplePayment handles cross-currency payments where Amount is XRP but
// the payment goes through the order book (has SendMax or paths).
// Reference: rippled Payment.cpp doApply() when ripple=true
func (p *Payment) applyRipplePayment(ctx *tx.ApplyContext, senderID, destID [20]byte) tx.Result {
	// Check destination exists
	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}
	destAccount, err := state.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check destination tag requirement
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// Validate credentials
	if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
		return result
	}

	// Check deposit authorization
	if result := p.verifyDepositPreauth(ctx, senderID, destID, destAccount); result != tx.TesSUCCESS {
		return result
	}

	// Use the flow engine (issuerID is unused for XRP amount, pass zero)
	var zeroID [20]byte
	return p.applyIOUPaymentWithPaths(ctx, senderID, destID, zeroID)
}

// applyIOUPaymentWithPaths handles IOU payments that require path finding using the Flow Engine.
// This is the main entry point for cross-currency payments and payments with explicit paths.
// Reference: rippled/src/xrpld/app/paths/RippleCalc.cpp
func (p *Payment) applyIOUPaymentWithPaths(ctx *tx.ApplyContext, senderID, destID, issuerID [20]byte) tx.Result {
	// Determine payment flags
	flags := p.GetFlags()
	partialPayment := (flags & PaymentFlagPartialPayment) != 0
	limitQuality := (flags & PaymentFlagLimitQuality) != 0
	noDirectRipple := (flags & PaymentFlagNoDirectRipple) != 0

	// addDefaultPath is true unless tfNoRippleDirect is set
	addDefaultPath := !noDirectRipple

	// Execute RippleCalculate
	rules := ctx.Rules()
	rcOpts := []RippleCalculateOption{
		WithAmendments(
			ctx.Config.ParentCloseTime,
			rules.Enabled(amendment.FeatureFixReducedOffersV1),
			rules.Enabled(amendment.FeatureFixReducedOffersV2),
			rules.Enabled(amendment.FeatureFixRmSmallIncreasedQOffers),
			rules.Enabled(amendment.FeatureFlowSortStrands),
		),
		WithAMMAmendments(
			rules.Enabled(amendment.FeatureFixAMMv1_1),
			rules.Enabled(amendment.FeatureFixAMMv1_2),
			rules.Enabled(amendment.FeatureFixAMMOverflowOffer),
		),
	}
	// Thread domain ID to the flow engine for permissioned domain payments.
	if p.DomainID != nil {
		domainID, err := permissioneddomain.ParseDomainID(*p.DomainID)
		if err == nil {
			rcOpts = append(rcOpts, WithDomainID(&domainID))
		}
	}
	_, actualOut, _, sandbox, result := RippleCalculate(
		ctx.View,
		senderID,
		destID,
		p.Amount,
		p.SendMax,
		p.Paths,
		addDefaultPath,
		partialPayment,
		limitQuality,
		ctx.TxHash,
		ctx.Config.LedgerSequence,
		rcOpts...,
	)

	// Because of its overhead, if RippleCalc fails with a retry code (ter*),
	// claim a fee instead. Reference: rippled Payment.cpp:509-510
	if result.IsTer() {
		result = tx.TecPATH_DRY
	}

	// Handle result
	if result != tx.TesSUCCESS && result != tx.TecPATH_PARTIAL {
		return result
	}

	// Apply sandbox changes back to the ledger view (through ApplyStateTable for tracking)
	if sandbox != nil {
		if err := sandbox.ApplyToView(ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Re-read the sender account from the view so the engine's post-Apply
	// write-back includes balance changes made by the flow engine.
	// Without this, ctx.Account has stale data that the engine would overwrite.
	{
		updatedData, err := ctx.View.Read(keylet.Account(senderID))
		if err == nil && updatedData != nil {
			if updated, parseErr := state.ParseAccountRoot(updatedData); parseErr == nil {
				*ctx.Account = *updated
			}
		}
	}

	// Check if partial payment delivered enough (DeliverMin)
	if partialPayment && p.DeliverMin != nil {
		deliverMin := ToEitherAmount(*p.DeliverMin)
		if actualOut.Compare(deliverMin) < 0 {
			return tx.TecPATH_PARTIAL
		}
	}

	// Record delivered amount in metadata
	deliveredAmt := FromEitherAmount(actualOut)
	ctx.Metadata.DeliveredAmount = &deliveredAmt

	// Offer deletions and trust line modifications tracked automatically by ApplyStateTable

	return result
}

// applyIOUIssue handles when sender is the issuer creating new tokens
func (p *Payment) applyIOUIssue(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount) tx.Result {
	// Look up the trust line between destination and issuer (sender)
	trustLineKey := keylet.Line(destID, senderID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !trustLineExists {
		// No trust line exists - destination has not authorized holding this currency
		return tx.TerNO_LINE
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check trust line authorization
	// Reference: rippled DirectStep.cpp:417-430
	// Issuer (sender) may require auth - check if destination's trust line is authorized
	if result := checkTrustLineAuthorization(ctx.View, senderID, destID, rippleState); result != tx.TesSUCCESS {
		return result
	}

	// Determine which side is low/high account
	destIsLow := state.CompareAccountIDsForLine(destID, senderID) < 0

	// Get the trust limit set by the destination (recipient)
	var trustLimit tx.Amount
	if destIsLow {
		trustLimit = rippleState.LowLimit
	} else {
		trustLimit = rippleState.HighLimit
	}

	// Calculate new balance after adding the amount
	// RippleState balance semantics:
	// - Negative balance = LOW owes HIGH (HIGH holds tokens)
	// - Positive balance = HIGH owes LOW (LOW holds tokens)
	var newBalance tx.Amount
	if destIsLow {
		// Dest is LOW, sender (issuer) is HIGH
		// Issuing means issuer (HIGH) now owes dest (LOW) more
		// Positive balance = HIGH owes LOW, so make MORE positive
		newBalance, _ = rippleState.Balance.Add(amount)
	} else {
		// Dest is HIGH, sender (issuer) is LOW
		// Issuing means issuer (LOW) now owes dest (HIGH) more
		// Negative balance = LOW owes HIGH, so make MORE negative
		newBalance, _ = rippleState.Balance.Sub(amount)
	}

	// Check if the new balance exceeds the trust limit
	absNewBalance := newBalance
	if absNewBalance.IsNegative() {
		absNewBalance = absNewBalance.Negate()
	}

	// The trust limit applies to the absolute balance
	if !trustLimit.IsZero() && absNewBalance.Compare(trustLimit) > 0 {
		return tx.TecPATH_PARTIAL
	}

	// Ensure the new balance has the correct currency and issuer
	// (the parsed balance may have null bytes for currency if it was zero)
	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer

	// Update the trust line
	rippleState.Balance = newBalance

	// Update PreviousTxnID and PreviousTxnLgrSeq to this transaction
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Serialize and update
	updatedTrustLine, err := state.SerializeRippleState(rippleState)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(trustLineKey, updatedTrustLine); err != nil {
		return tx.TefINTERNAL
	}

	// RippleState modification tracked automatically by ApplyStateTable

	delivered := p.Amount
	ctx.Metadata.DeliveredAmount = &delivered

	return tx.TesSUCCESS
}

// applyIOURedeem handles when destination is the issuer (redeeming tokens)
func (p *Payment) applyIOURedeem(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount) tx.Result {
	// Look up the trust line between sender and issuer (destination)
	trustLineKey := keylet.Line(senderID, destID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !trustLineExists {
		// No trust line exists - sender doesn't hold this currency
		return tx.TerNO_LINE
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine which side is low/high account
	senderIsLow := state.CompareAccountIDsForLine(senderID, destID) < 0

	// Get sender's current balance (how much issuer owes them)
	// RippleState balance semantics:
	// - Negative balance = LOW owes HIGH (HIGH holds tokens)
	// - Positive balance = HIGH owes LOW (LOW holds tokens)
	var senderBalance tx.Amount
	if senderIsLow {
		// Sender is LOW, issuer (dest) is HIGH
		// Positive balance = sender holds tokens (HIGH owes LOW)
		senderBalance = rippleState.Balance
	} else {
		// Sender is HIGH, issuer (dest) is LOW
		// Negative balance = sender holds tokens (LOW owes HIGH)
		// Negate to get positive holdings value
		senderBalance = rippleState.Balance.Negate()
	}

	// Check sender has enough balance
	if senderBalance.Compare(amount) < 0 {
		return tx.TecPATH_PARTIAL
	}

	// Update balance by reducing sender's holding
	// When redeeming, the issuer owes less to the sender
	var newBalance tx.Amount
	if senderIsLow {
		// Sender is LOW, issuer is HIGH
		// Positive balance = sender holds. Reduce by subtracting.
		newBalance, _ = rippleState.Balance.Sub(amount)
	} else {
		// Sender is HIGH, issuer is LOW
		// Negative balance = sender holds. Make less negative by adding.
		newBalance, _ = rippleState.Balance.Add(amount)
	}

	// Ensure the new balance has the correct currency and issuer
	// (the parsed balance may have null bytes for currency if it was zero)
	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer

	rippleState.Balance = newBalance

	// Update PreviousTxnID and PreviousTxnLgrSeq to this transaction
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Serialize and update
	updatedTrustLine, err := state.SerializeRippleState(rippleState)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(trustLineKey, updatedTrustLine); err != nil {
		return tx.TefINTERNAL
	}

	// RippleState modification tracked automatically by ApplyStateTable

	delivered := p.Amount
	ctx.Metadata.DeliveredAmount = &delivered

	return tx.TesSUCCESS
}

// applyIOUTransfer handles transfer between two non-issuer accounts
// Reference: rippled Payment.cpp, DirectStep.cpp, and StepChecks.h
func (p *Payment) applyIOUTransfer(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID, issuerID [20]byte, amount tx.Amount) tx.Result {
	// Both sender and destination need trust lines to the issuer

	// Check if issuer has GlobalFreeze enabled
	// Reference: rippled StepChecks.h checkFreeze() line 45-48
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if (issuerAccount.Flags & state.LsfGlobalFreeze) != 0 {
		return tx.TerNO_LINE
	}

	// Get transfer rate from issuer for holder-to-holder transfers
	// Reference: rippled Payment.cpp:544-557 (MPT) and DirectStep.cpp:798 (IOU)
	transferRate := GetTransferRate(ctx.View, issuerID)

	// Calculate gross amount sender needs to spend (includes transfer fee)
	// grossAmount = amount * (transferRate / QualityOne), round up
	grossAmount := amount.MulRatio(transferRate, QualityOne, true)

	// Get sender's trust line to issuer
	senderTrustLineKey := keylet.Line(senderID, issuerID, amount.Currency)
	senderTrustExists, err := ctx.View.Exists(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !senderTrustExists {
		return tx.TerNO_LINE
	}

	// Get destination's trust line to issuer
	destTrustLineKey := keylet.Line(destID, issuerID, amount.Currency)
	destTrustExists, err := ctx.View.Exists(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	if !destTrustExists {
		return tx.TerNO_LINE
	}

	// Read sender's trust line
	senderTrustData, err := ctx.View.Read(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	senderRippleState, err := state.ParseRippleState(senderTrustData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check trust line authorization for sender
	// Reference: rippled DirectStep.cpp:417-430
	if result := checkTrustLineAuthorization(ctx.View, issuerID, senderID, senderRippleState); result != tx.TesSUCCESS {
		return result
	}

	// Check if sender's trust line is frozen by the issuer
	// Reference: rippled StepChecks.h checkFreeze() line 51-56
	// checkFreeze(src, dst, currency) checks if dst has frozen their trust line with src
	// For sender→issuer, we check if issuer has frozen
	senderIsLowInTrustLine := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	if senderIsLowInTrustLine {
		// Sender is LOW, issuer is HIGH
		// Check if issuer (HIGH) has frozen sender's trust line
		if (senderRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE
		}
	} else {
		// Sender is HIGH, issuer is LOW
		// Check if issuer (LOW) has frozen sender's trust line
		if (senderRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE
		}
	}

	// Read destination's trust line
	destTrustData, err := ctx.View.Read(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	destRippleState, err := state.ParseRippleState(destTrustData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check trust line authorization for destination
	// Reference: rippled DirectStep.cpp:417-430
	if result := checkTrustLineAuthorization(ctx.View, issuerID, destID, destRippleState); result != tx.TesSUCCESS {
		return result
	}

	// Check if destination's trust line is frozen (holder freeze)
	// Reference: rippled StepChecks.h checkFreeze(src, dst) line 51-56
	// checkFreeze checks if DST has frozen their trust line with SRC
	// For issuer→dest: checkFreeze(issuer, dest) checks DEST's freeze flag
	destIsLowInTrustLine := state.CompareAccountIDsForLine(destID, issuerID) < 0
	if destIsLowInTrustLine {
		// Dest is LOW, issuer is HIGH
		// Check if dest (LOW) has frozen their trust line
		if (destRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE
		}
	} else {
		// Dest is HIGH, issuer is LOW
		// Check if dest (HIGH) has frozen their trust line
		if (destRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE
		}
	}

	// Calculate sender's balance with issuer
	// RippleState balance semantics:
	// - Negative balance = LOW owes HIGH (HIGH holds tokens)
	// - Positive balance = HIGH owes LOW (LOW holds tokens)
	senderIsLowWithIssuer := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	var senderBalance tx.Amount
	if senderIsLowWithIssuer {
		// Sender is LOW, issuer is HIGH
		// Positive balance = sender holds tokens (HIGH/issuer owes LOW/sender)
		senderBalance = senderRippleState.Balance
	} else {
		// Sender is HIGH, issuer is LOW
		// Negative balance = sender holds tokens (LOW/issuer owes HIGH/sender)
		senderBalance = senderRippleState.Balance.Negate()
	}

	// Check sender has enough for gross amount (includes transfer fee)
	if senderBalance.Compare(grossAmount) < 0 {
		return tx.TecPATH_PARTIAL
	}

	// Calculate destination's current balance and trust limit
	destIsLowWithIssuer := state.CompareAccountIDsForLine(destID, issuerID) < 0
	var destBalance, destLimit tx.Amount
	if destIsLowWithIssuer {
		// Dest is LOW, issuer is HIGH
		// Positive balance = dest holds tokens
		destBalance = destRippleState.Balance
		destLimit = destRippleState.LowLimit
	} else {
		// Dest is HIGH, issuer is LOW
		// Negative balance = dest holds tokens
		destBalance = destRippleState.Balance.Negate()
		destLimit = destRippleState.HighLimit
	}

	// Check destination trust limit (for net amount delivered)
	newDestBalance, _ := destBalance.Add(amount)
	if !destLimit.IsZero() && newDestBalance.Compare(destLimit) > 0 {
		return tx.TecPATH_PARTIAL
	}

	// Update sender's trust line (decrease by GROSS amount - includes transfer fee)
	// The transfer fee reduces the issuer's liability (sender pays more than dest receives)
	var newSenderRippleBalance tx.Amount
	if senderIsLowWithIssuer {
		// Sender is LOW, positive balance = holdings. Decrease by subtracting gross.
		newSenderRippleBalance, _ = senderRippleState.Balance.Sub(grossAmount)
	} else {
		// Sender is HIGH, negative balance = holdings. Make less negative by adding gross.
		newSenderRippleBalance, _ = senderRippleState.Balance.Add(grossAmount)
	}
	// Ensure the new balance has the correct currency and issuer
	newSenderRippleBalance.Currency = amount.Currency
	newSenderRippleBalance.Issuer = amount.Issuer
	senderRippleState.Balance = newSenderRippleBalance

	// Update PreviousTxnID and PreviousTxnLgrSeq on sender's trust line
	senderRippleState.PreviousTxnID = ctx.TxHash
	senderRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Update destination's trust line (increase by NET amount - what they actually receive)
	var newDestRippleBalance tx.Amount
	if destIsLowWithIssuer {
		// Dest is LOW, positive balance = holdings. Increase by adding net amount.
		newDestRippleBalance, _ = destRippleState.Balance.Add(amount)
	} else {
		// Dest is HIGH, negative balance = holdings. Make more negative by subtracting net amount.
		newDestRippleBalance, _ = destRippleState.Balance.Sub(amount)
	}
	// Ensure the new balance has the correct currency and issuer
	newDestRippleBalance.Currency = amount.Currency
	newDestRippleBalance.Issuer = amount.Issuer
	destRippleState.Balance = newDestRippleBalance

	// Update PreviousTxnID and PreviousTxnLgrSeq on dest's trust line
	destRippleState.PreviousTxnID = ctx.TxHash
	destRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Serialize and update sender's trust line
	updatedSenderTrust, err := state.SerializeRippleState(senderRippleState)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(senderTrustLineKey, updatedSenderTrust); err != nil {
		return tx.TefINTERNAL
	}

	// Serialize and update destination's trust line
	updatedDestTrust, err := state.SerializeRippleState(destRippleState)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(destTrustLineKey, updatedDestTrust); err != nil {
		return tx.TefINTERNAL
	}

	// RippleState modifications tracked automatically by ApplyStateTable

	// Delivered amount is the NET amount (what destination actually receives)
	delivered := amount
	ctx.Metadata.DeliveredAmount = &delivered

	return tx.TesSUCCESS
}

// applyIOUIssueWithDelivered wraps applyIOUIssue to return the delivered amount
// If partialPayment is true and the full amount cannot be issued, it will issue
// as much as possible up to the destination's trust limit.
func (p *Payment) applyIOUIssueWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOUIssuePartial(ctx, dest, senderID, destID, amount, partialPayment)
}

// applyIOURedeemWithDelivered wraps applyIOURedeem to return the delivered amount
// If partialPayment is true and the full amount cannot be redeemed, it will redeem
// as much as possible based on sender's balance.
func (p *Payment) applyIOURedeemWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOURedeemPartial(ctx, dest, senderID, destID, amount, partialPayment)
}

// applyIOUTransferWithDelivered wraps applyIOUTransfer to return the delivered amount
// If partialPayment is true and the full amount cannot be transferred, it will transfer
// as much as possible based on sender's balance and destination's trust limit.
func (p *Payment) applyIOUTransferWithDelivered(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID, issuerID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	return p.applyIOUTransferPartial(ctx, dest, senderID, destID, issuerID, amount, partialPayment)
}

// ============================================================================
// Partial Payment Implementations
// Reference: rippled Flow.cpp, RippleCalc.cpp
// ============================================================================

// applyIOUIssuePartial handles issuing currency with partial payment support
func (p *Payment) applyIOUIssuePartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Look up the trust line between destination and issuer (sender)
	trustLineKey := keylet.Line(destID, senderID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	if !trustLineExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization
	if result := checkTrustLineAuthorization(ctx.View, senderID, destID, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Determine which side is low/high account
	destIsLow := state.CompareAccountIDsForLine(destID, senderID) < 0

	// Get the current balance and trust limit
	var currentBalance, trustLimit tx.Amount
	if destIsLow {
		currentBalance = rippleState.Balance
		trustLimit = rippleState.LowLimit
	} else {
		currentBalance = rippleState.Balance.Negate()
		trustLimit = rippleState.HighLimit
	}

	// Calculate maximum we can issue based on trust limit
	var maxDeliverable tx.Amount
	if trustLimit.IsZero() {
		// No limit set, can issue any amount
		maxDeliverable = amount
	} else {
		// Calculate room available: limit - current balance
		room, _ := trustLimit.Sub(currentBalance)
		if room.IsNegative() || room.IsZero() {
			if partialPayment {
				return tx.TesSUCCESS, tx.Amount{} // Nothing to deliver, but partial is OK
			}
			return tx.TecPATH_PARTIAL, tx.Amount{}
		}
		if room.Compare(amount) < 0 {
			maxDeliverable = room
		} else {
			maxDeliverable = amount
		}
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If nothing to deliver
	if maxDeliverable.IsZero() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Calculate new balance
	var newBalance tx.Amount
	if destIsLow {
		newBalance, _ = rippleState.Balance.Add(maxDeliverable)
	} else {
		newBalance, _ = rippleState.Balance.Sub(maxDeliverable)
	}

	// Ensure the new balance has the correct currency and issuer
	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer

	// Update the trust line
	rippleState.Balance = newBalance
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Check for trust line cleanup (default state → delete)
	// Reference: rippled View.cpp rippleCreditIOU() lines 1692-1745
	if result := trustLineCleanup(ctx, dest, senderID, destID, trustLineKey, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// applyIOURedeemPartial handles redeeming currency with partial payment support
func (p *Payment) applyIOURedeemPartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Look up the trust line between sender and issuer (destination)
	trustLineKey := keylet.Line(senderID, destID, amount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	if !trustLineExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read and parse the trust line
	trustLineData, err := ctx.View.Read(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	rippleState, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Determine which side is low/high account
	senderIsLow := state.CompareAccountIDsForLine(senderID, destID) < 0

	// Get sender's current balance
	var senderBalance tx.Amount
	if senderIsLow {
		senderBalance = rippleState.Balance
	} else {
		senderBalance = rippleState.Balance.Negate()
	}

	// Calculate maximum we can redeem based on sender's balance
	var maxDeliverable tx.Amount
	if senderBalance.Compare(amount) < 0 {
		if senderBalance.IsNegative() || senderBalance.IsZero() {
			if partialPayment {
				return tx.TesSUCCESS, tx.Amount{}
			}
			return tx.TecPATH_PARTIAL, tx.Amount{}
		}
		maxDeliverable = senderBalance
	} else {
		maxDeliverable = amount
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If nothing to deliver
	if maxDeliverable.IsZero() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Update balance
	var newBalance tx.Amount
	if senderIsLow {
		newBalance, _ = rippleState.Balance.Sub(maxDeliverable)
	} else {
		newBalance, _ = rippleState.Balance.Add(maxDeliverable)
	}

	newBalance.Currency = amount.Currency
	newBalance.Issuer = amount.Issuer
	rippleState.Balance = newBalance
	rippleState.PreviousTxnID = ctx.TxHash
	rippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Check for trust line cleanup (default state → delete)
	// Reference: rippled View.cpp rippleCreditIOU() lines 1692-1745
	if result := trustLineCleanup(ctx, dest, senderID, destID, trustLineKey, rippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// applyIOUTransferPartial handles transfers between non-issuer accounts with partial payment support
func (p *Payment) applyIOUTransferPartial(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID, issuerID [20]byte, amount tx.Amount, partialPayment bool) (tx.Result, tx.Amount) {
	// Check if issuer has GlobalFreeze enabled
	issuerKey := keylet.Account(issuerID)
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if (issuerAccount.Flags & state.LsfGlobalFreeze) != 0 {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Get transfer rate from issuer
	transferRate := GetTransferRate(ctx.View, issuerID)

	// Get sender's trust line to issuer
	senderTrustLineKey := keylet.Line(senderID, issuerID, amount.Currency)
	senderTrustExists, err := ctx.View.Exists(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if !senderTrustExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Get destination's trust line to issuer
	destTrustLineKey := keylet.Line(destID, issuerID, amount.Currency)
	destTrustExists, err := ctx.View.Exists(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if !destTrustExists {
		return tx.TerNO_LINE, tx.Amount{}
	}

	// Read sender's trust line
	senderTrustData, err := ctx.View.Read(senderTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	senderRippleState, err := state.ParseRippleState(senderTrustData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization for sender
	if result := checkTrustLineAuthorization(ctx.View, issuerID, senderID, senderRippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Check if sender's trust line is frozen
	senderIsLowInTrustLine := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	if senderIsLowInTrustLine {
		if (senderRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	} else {
		if (senderRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	}

	// Read destination's trust line
	destTrustData, err := ctx.View.Read(destTrustLineKey)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	destRippleState, err := state.ParseRippleState(destTrustData)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Check trust line authorization for destination
	if result := checkTrustLineAuthorization(ctx.View, issuerID, destID, destRippleState); result != tx.TesSUCCESS {
		return result, tx.Amount{}
	}

	// Check if destination's trust line is frozen
	destIsLowInTrustLine := state.CompareAccountIDsForLine(destID, issuerID) < 0
	if destIsLowInTrustLine {
		if (destRippleState.Flags & state.LsfLowFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	} else {
		if (destRippleState.Flags & state.LsfHighFreeze) != 0 {
			return tx.TerNO_LINE, tx.Amount{}
		}
	}

	// Calculate sender's balance with issuer
	senderIsLowWithIssuer := state.CompareAccountIDsForLine(senderID, issuerID) < 0
	var senderBalance tx.Amount
	if senderIsLowWithIssuer {
		senderBalance = senderRippleState.Balance
	} else {
		senderBalance = senderRippleState.Balance.Negate()
	}

	// Calculate destination's balance and trust limit
	destIsLowWithIssuer := state.CompareAccountIDsForLine(destID, issuerID) < 0
	var destBalance, destLimit tx.Amount
	if destIsLowWithIssuer {
		destBalance = destRippleState.Balance
		destLimit = destRippleState.LowLimit
	} else {
		destBalance = destRippleState.Balance.Negate()
		destLimit = destRippleState.HighLimit
	}

	// Calculate maximum deliverable based on:
	// 1. Sender's available balance (accounting for transfer fee)
	// 2. Destination's trust limit room

	// Max based on sender's balance: senderBalance * (QualityOne / transferRate)
	// This accounts for the transfer fee in reverse
	maxFromSender := senderBalance.MulRatio(QualityOne, transferRate, false)

	// Max based on destination's trust limit
	var maxFromDestLimit tx.Amount
	if destLimit.IsZero() {
		// No limit - use a very large value (effectively unlimited)
		maxFromDestLimit = amount
	} else {
		room, _ := destLimit.Sub(destBalance)
		if room.IsNegative() {
			room = tx.NewIssuedAmountFromFloat64(0, amount.Currency, amount.Issuer)
		}
		maxFromDestLimit = room
	}

	// Actual max is minimum of the two constraints
	var maxDeliverable tx.Amount
	if maxFromSender.Compare(maxFromDestLimit) < 0 {
		maxDeliverable = maxFromSender
	} else {
		maxDeliverable = maxFromDestLimit
	}

	// Cap at requested amount
	if maxDeliverable.Compare(amount) > 0 {
		maxDeliverable = amount
	}

	// If we can't deliver anything
	if maxDeliverable.IsZero() || maxDeliverable.IsNegative() {
		if partialPayment {
			return tx.TesSUCCESS, tx.Amount{}
		}
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// If we can't deliver the full amount and partial payment is not allowed, fail
	if maxDeliverable.Compare(amount) < 0 && !partialPayment {
		return tx.TecPATH_PARTIAL, tx.Amount{}
	}

	// Calculate gross amount sender needs to spend (includes transfer fee)
	grossAmount := maxDeliverable.MulRatio(transferRate, QualityOne, true)

	// Update sender's trust line
	var newSenderRippleBalance tx.Amount
	if senderIsLowWithIssuer {
		newSenderRippleBalance, _ = senderRippleState.Balance.Sub(grossAmount)
	} else {
		newSenderRippleBalance, _ = senderRippleState.Balance.Add(grossAmount)
	}
	newSenderRippleBalance.Currency = amount.Currency
	newSenderRippleBalance.Issuer = amount.Issuer
	senderRippleState.Balance = newSenderRippleBalance
	senderRippleState.PreviousTxnID = ctx.TxHash
	senderRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Update destination's trust line
	var newDestRippleBalance tx.Amount
	if destIsLowWithIssuer {
		newDestRippleBalance, _ = destRippleState.Balance.Add(maxDeliverable)
	} else {
		newDestRippleBalance, _ = destRippleState.Balance.Sub(maxDeliverable)
	}
	newDestRippleBalance.Currency = amount.Currency
	newDestRippleBalance.Issuer = amount.Issuer
	destRippleState.Balance = newDestRippleBalance
	destRippleState.PreviousTxnID = ctx.TxHash
	destRippleState.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

	// Serialize and update sender's trust line
	updatedSenderTrust, err := state.SerializeRippleState(senderRippleState)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if err := ctx.View.Update(senderTrustLineKey, updatedSenderTrust); err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	// Serialize and update destination's trust line
	updatedDestTrust, err := state.SerializeRippleState(destRippleState)
	if err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}
	if err := ctx.View.Update(destTrustLineKey, updatedDestTrust); err != nil {
		return tx.TefINTERNAL, tx.Amount{}
	}

	ctx.Metadata.DeliveredAmount = &maxDeliverable

	return tx.TesSUCCESS, maxDeliverable
}

// trustLineCleanup checks if a trust line is in default state after a balance modification
// and deletes it if so, adjusting OwnerCount for both accounts.
// This matches rippled's rippleCreditIOU() logic in View.cpp lines 1692-1745.
//
// Parameters:
//   - ctx: apply context (ctx.Account = transaction sender, identified by senderID)
//   - dest: the other account's AccountRoot (identified by destID)
//   - senderID, destID: the two account IDs on the trust line
//   - tlKey: the keylet for the trust line
//   - rs: the already-modified RippleState (balance updated, not yet serialized)
//
// On success, either updates or erases the trust line via ctx.View. Returns TesSUCCESS.
func trustLineCleanup(ctx *tx.ApplyContext, dest *state.AccountRoot, senderID, destID [20]byte, tlKey keylet.Keylet, rs *state.RippleState) tx.Result {
	senderIsLow := state.CompareAccountIDsForLine(senderID, destID) < 0

	// Get both accounts' DefaultRipple flags
	var lowDefRipple, highDefRipple bool
	if senderIsLow {
		lowDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (dest.Flags & state.LsfDefaultRipple) != 0
	} else {
		lowDefRipple = (dest.Flags & state.LsfDefaultRipple) != 0
		highDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
	}

	bLowReserveSet := rs.LowQualityIn != 0 || rs.LowQualityOut != 0 ||
		((rs.Flags&state.LsfLowNoRipple) == 0) != lowDefRipple ||
		(rs.Flags&state.LsfLowFreeze) != 0 || !rs.LowLimit.IsZero() ||
		rs.Balance.Signum() > 0

	bHighReserveSet := rs.HighQualityIn != 0 || rs.HighQualityOut != 0 ||
		((rs.Flags&state.LsfHighNoRipple) == 0) != highDefRipple ||
		(rs.Flags&state.LsfHighFreeze) != 0 || !rs.HighLimit.IsZero() ||
		rs.Balance.Signum() < 0

	bLowReserved := (rs.Flags & state.LsfLowReserve) != 0
	bHighReserved := (rs.Flags & state.LsfHighReserve) != 0

	bDefault := !bLowReserveSet && !bHighReserveSet

	if bDefault && rs.Balance.IsZero() {
		// Remove from both owner directories before erasing
		// Reference: rippled trustDelete() in View.cpp
		var lowID, highID [20]byte
		if senderIsLow {
			lowID = senderID
			highID = destID
		} else {
			lowID = destID
			highID = senderID
		}
		lowDirKey := keylet.OwnerDir(lowID)
		state.DirRemove(ctx.View, lowDirKey, rs.LowNode, tlKey.Key, false)
		highDirKey := keylet.OwnerDir(highID)
		state.DirRemove(ctx.View, highDirKey, rs.HighNode, tlKey.Key, false)

		// Delete the trust line
		if err := ctx.View.Erase(tlKey); err != nil {
			return tx.TefINTERNAL
		}

		// Decrement OwnerCount for both sides that had reserve set
		if bLowReserved {
			if senderIsLow {
				if ctx.Account.OwnerCount > 0 {
					ctx.Account.OwnerCount--
				}
			} else {
				if dest.OwnerCount > 0 {
					dest.OwnerCount--
				}
			}
		}
		if bHighReserved {
			if !senderIsLow {
				if ctx.Account.OwnerCount > 0 {
					ctx.Account.OwnerCount--
				}
			} else {
				if dest.OwnerCount > 0 {
					dest.OwnerCount--
				}
			}
		}

		// Write dest account back if its OwnerCount changed
		destChanged := (bLowReserved && !senderIsLow) || (bHighReserved && senderIsLow)
		if destChanged {
			destKey := keylet.Account(destID)
			destUpdatedData, serErr := state.SerializeAccountRoot(dest)
			if serErr != nil {
				return tx.TefINTERNAL
			}
			if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
				return tx.TefINTERNAL
			}
		}
	} else {
		// Adjust reserve flags
		if bLowReserveSet && !bLowReserved {
			rs.Flags |= state.LsfLowReserve
		} else if !bLowReserveSet && bLowReserved {
			rs.Flags &^= state.LsfLowReserve
		}
		if bHighReserveSet && !bHighReserved {
			rs.Flags |= state.LsfHighReserve
		} else if !bHighReserveSet && bHighReserved {
			rs.Flags &^= state.LsfHighReserve
		}

		// Serialize and update
		updatedData, serErr := state.SerializeRippleState(rs)
		if serErr != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(tlKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	return tx.TesSUCCESS
}

package payment

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

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

	// DomainID is the permissioned domain for this payment (optional).
	// When set, only offers within the specified domain are consumed on the payment path.
	// Both sender and destination must be members of the domain.
	// Requires FeaturePermissionedDEX amendment.
	// Reference: rippled Payment.cpp sfDomainID
	DomainID *string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`
}

// PathStep represents a single step in a payment path
type PathStep struct {
	Account       string `json:"account,omitempty"`
	Currency      string `json:"currency,omitempty"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
	Type          int    `json:"type,omitempty"`
	TypeHex       string `json:"type_hex,omitempty"`
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

// Payment invalid-flag masks (rippled tfPaymentMask / tfMPTPaymentMask).
const (
	tfPaymentMask    uint32 = ^(tx.TfUniversal | PaymentFlagPartialPayment | PaymentFlagLimitQuality | PaymentFlagNoDirectRipple)
	tfMPTPaymentMask uint32 = ^(tx.TfUniversal | PaymentFlagPartialPayment)
)

// maxNativeDrops is rippled STAmount::cMaxNativeN — the isLegalNet ceiling on a
// native (XRP) amount's magnitude.
const maxNativeDrops uint64 = 100_000_000_000_000_000

// Path constraints matching rippled
const (
	// MaxPathSize is the maximum number of paths in a payment (rippled: Payment.h MaxPathSize = 6)
	MaxPathSize = 6
	// MaxPathLength is the maximum number of steps per path (rippled: MaxPathLength = 8)
	MaxPathLength = 8
)

// NewPayment creates a new Payment transaction
func NewPayment(account, destination string, amount tx.Amount) *Payment {
	return &Payment{
		BaseTx:      *tx.NewBaseTx(tx.TypePayment, account),
		Amount:      amount,
		Destination: destination,
	}
}

func (p *Payment) TxType() tx.Type {
	return tx.TypePayment
}

// GetDomainID exposes the payment's DomainID to the ValidPermissionedDEX invariant.
func (p *Payment) GetDomainID() (*[32]byte, bool) {
	if p.DomainID == nil {
		return nil, false
	}
	id, err := permissioneddomain.ParseDomainID(*p.DomainID)
	if err != nil {
		return nil, false
	}
	return &id, true
}

// RequiredAmendments returns amendments required for this transaction. These
// mirror rippled's checkExtraFeatures gates (sfCredentialIDs → featureCredentials,
// sfDomainID → featurePermissionedDEX), which the engine evaluates before the
// flags mask. The MPT gate is evaluated in PreflightWithRules after the mask.
func (p *Payment) RequiredAmendments() [][32]byte {
	var amendments [][32]byte
	if p.CredentialIDs != nil || p.HasField("CredentialIDs") {
		amendments = append(amendments, amendment.FeatureCredentials)
	}
	if p.DomainID != nil {
		amendments = append(amendments, amendment.FeaturePermissionedDEX)
	}
	return amendments
}

// GetFlagsMask returns the invalid-flags mask enforced by the engine at the
// preflight0 position. An MPT-denominated Amount uses the restricted MPTokensV1
// mask until MPTokensV2 enables the regular payment flag surface.
func (p *Payment) GetFlagsMask(rules *amendment.Rules) uint32 {
	if p.Amount.IsMPT() && (rules == nil || !rules.MPTokensV2Enabled()) {
		return tfMPTPaymentMask
	}
	return tfPaymentMask
}

func (p *Payment) Validate() error {
	return p.validate(nil)
}

func (p *Payment) PreflightWithRules(rules *amendment.Rules) error {
	return p.validate(rules)
}

func (p *Payment) validate(rules *amendment.Rules) error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	isDstMPT := p.Amount.IsMPT()
	rulesAware := rules != nil
	mpTokensV2 := rulesAware && rules.MPTokensV2Enabled()
	hasPaths := len(p.Paths) > 0 || p.HasField("Paths")
	if rulesAware && isDstMPT && !rules.Enabled(amendment.FeatureMPTokensV1) {
		return ter.Errorf(ter.TemDISABLED, "MPT payment requires MPTokensV1 amendment")
	}
	if rulesAware && !mpTokensV2 && isDstMPT && hasPaths {
		return ter.Errorf(ter.TemMALFORMED, "Paths not allowed for MPT payment")
	}
	if rulesAware && p.DomainID != nil && rules.FixCleanup3_2_0Enabled() {
		if id, err := permissioneddomain.ParseDomainID(*p.DomainID); err == nil && id == ([32]byte{}) {
			return ter.Errorf(ter.TemMALFORMED, "DomainID cannot be zero")
		}
	}

	flags := p.GetFlags()
	srcAmount := p.Amount
	if p.SendMax != nil {
		srcAmount = *p.SendMax
	}
	if rulesAware && !mpTokensV2 && ((isDstMPT && !sameAsset(p.Amount, srcAmount)) ||
		(!isDstMPT && srcAmount.IsMPT())) {
		return ter.Errorf(ter.TemMALFORMED, "Inconsistent MPT issues in Amount and SendMax")
	}

	xrpDirect := srcAmount.IsNative() && p.Amount.IsNative()

	if !isLegalNet(p.Amount) || !isLegalNet(srcAmount) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "amount exceeds native limit")
	}

	if p.Destination == "" {
		return ter.Errorf(ter.TemDST_NEEDED, "Destination is required")
	}
	if destID, decErr := state.DecodeAccountID(p.Destination); decErr == nil && destID == ([20]byte{}) {
		return ter.Errorf(ter.TemDST_NEEDED, "Destination is required")
	}

	if p.SendMax != nil && (p.SendMax.IsZero() || p.SendMax.IsNegative()) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "SendMax must be positive")
	}
	if p.Amount.IsZero() || p.Amount.IsNegative() {
		return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	}

	if badPaymentAsset(srcAmount, mpTokensV2) || badPaymentAsset(p.Amount, mpTokensV2) {
		return ter.Errorf(ter.TemBAD_CURRENCY, "invalid payment asset")
	}

	if p.Account == p.Destination && equalTokens(srcAmount, p.Amount) && !hasPaths {
		return ter.Errorf(ter.TemREDUNDANT, "cannot send to self without path")
	}

	partialPaymentAllowed := (flags & PaymentFlagPartialPayment) != 0
	limitQuality := (flags & PaymentFlagLimitQuality) != 0
	noRippleDirect := (flags & PaymentFlagNoDirectRipple) != 0

	if xrpDirect && p.SendMax != nil {
		return ter.Errorf(ter.TemBAD_SEND_XRP_MAX, "SendMax specified for XRP to XRP")
	}
	legacyMPTDirect := rulesAware && !mpTokensV2 && isDstMPT
	if (xrpDirect || legacyMPTDirect) && hasPaths {
		return ter.Errorf(ter.TemBAD_SEND_XRP_PATHS, "Paths specified for XRP to XRP or MPT to MPT")
	}
	if xrpDirect && partialPaymentAllowed {
		return ter.Errorf(ter.TemBAD_SEND_XRP_PARTIAL, "Partial payment specified for XRP to XRP")
	}
	if (xrpDirect || legacyMPTDirect) && limitQuality {
		return ter.Errorf(ter.TemBAD_SEND_XRP_LIMIT, "Limit quality specified for XRP to XRP or MPT to MPT")
	}
	if (xrpDirect || legacyMPTDirect) && noRippleDirect {
		return ter.Errorf(ter.TemBAD_SEND_XRP_NO_DIRECT, "No ripple direct specified for XRP to XRP or MPT to MPT")
	}

	if p.DeliverMin != nil {
		if !partialPaymentAllowed {
			return ter.Errorf(ter.TemBAD_AMOUNT, "DeliverMin requires tfPartialPayment flag")
		}
		if !isLegalNet(*p.DeliverMin) || p.DeliverMin.IsZero() || p.DeliverMin.IsNegative() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "DeliverMin must be positive")
		}
		if !sameAsset(*p.DeliverMin, p.Amount) {
			return ter.Errorf(ter.TemBAD_AMOUNT, "DeliverMin asset must match Amount")
		}
		if p.DeliverMin.Compare(p.Amount) > 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "DeliverMin cannot exceed Amount")
		}
	}

	for _, path := range p.Paths {
		for _, elem := range path {
			if elem.Account == "" && elem.Currency == "" && elem.Issuer == "" && elem.MPTIssuanceID == "" {
				return ter.Errorf(ter.TemBAD_PATH, "path element has no account, currency, issuer, or MPT issuance")
			}
		}
	}

	present := p.CredentialIDs != nil || p.HasField("CredentialIDs")
	if err := credential.CheckFields(p.CredentialIDs, present, "Duplicate credential ID"); err != nil {
		return err
	}

	return nil
}

func badPaymentAsset(a tx.Amount, mpTokensV2 bool) bool {
	if !a.IsNative() && !a.IsMPT() {
		return a.Currency == tx.BadCurrency
	}
	if !mpTokensV2 || !a.IsMPT() {
		return false
	}
	id, ok := decodeMPTID(a.MPTIssuanceID())
	return !ok || mptIssuer(id) == ([20]byte{})
}

// isLegalNet mirrors rippled STAmount isLegalNet: a native amount's magnitude may
// not exceed cMaxNativeN; non-native amounts are always legal.
func isLegalNet(a tx.Amount) bool {
	if !a.IsNative() {
		return true
	}
	d := a.Drops()
	if d < 0 {
		d = -d
	}
	return uint64(d) <= maxNativeDrops
}

// equalTokens mirrors rippled equalTokens: two IOU tokens are equal iff their
// currencies match (issuer ignored); two MPT tokens iff their issuances match;
// two native amounts are always equal; any cross-kind pair is unequal.
func equalTokens(src, dst tx.Amount) bool {
	switch {
	case src.IsNative() && dst.IsNative():
		return true
	case src.IsMPT() && dst.IsMPT():
		return src.MPTIssuanceID() == dst.MPTIssuanceID()
	case !src.IsNative() && !src.IsMPT() && !dst.IsNative() && !dst.IsMPT():
		return src.Currency == dst.Currency
	default:
		return false
	}
}

// Preclaim performs stateful validation against the current ledger view,
// mirroring rippled's Payment::preclaim: destination-existence branching,
// the destination-tag check on an existing destination, the path-count limit,
// credential validation, and domain membership — in that order. Keeping these
// here (not in Apply) means their tec/tel codes originate in the preclaim phase
// (subject to the engine's likelyToClaimFee tapRETRY gate) and follow rippled's
// precedence: a payment that fails both credential and domain checks reports the
// credential code.
// Reference: rippled Payment.cpp:282-378
func (p *Payment) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	// Reference: rippled Payment.cpp:296-346
	if destID, err := state.DecodeAccountID(p.Destination); err == nil {
		destAccount, destExists := state.ReadAccountRoot(view, destID)
		if !destExists {
			// A non-native delivered amount cannot create the account.
			if !p.Amount.IsNative() {
				return ter.TecNO_DST
			}
			// A partial payment may not fund a new account on an open ledger.
			if config.OpenLedger && (p.GetFlags()&PaymentFlagPartialPayment) != 0 {
				return ter.TelNO_DST_PARTIAL
			}
			// The delivered amount must cover the account reserve.
			if uint64(p.Amount.Drops()) < config.ReserveBase {
				return ter.TecNO_DST_INSUF_XRP
			}
		} else if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
			// A newly-formed account is exempt — it has no way to set the flag.
			return ter.TecDST_TAG_NEEDED
		}
	}

	// Path count/length limits — only on an open ledger and only for "ripple"
	// payments (those that use transitive balances).
	// Reference: rippled Payment.cpp:348-360
	if config.OpenLedger {
		ripple := len(p.Paths) > 0 || p.HasField("Paths") || p.SendMax != nil || !p.Amount.IsNative()
		if ripple {
			if len(p.Paths) > MaxPathSize {
				return ter.TelBAD_PATH_COUNT
			}
			for _, path := range p.Paths {
				if len(path) > MaxPathLength {
					return ter.TelBAD_PATH_COUNT
				}
			}
		}
	}

	// Credential validation and domain membership both need the sender's
	// AccountID; credentials::valid is a no-op for an empty credential set, so
	// only resolve it when one of those checks applies.
	if len(p.CredentialIDs) > 0 || p.DomainID != nil {
		senderID, err := state.DecodeAccountID(p.Account)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Credential validation precedes the domain check: each credential must
		// exist, have the sender as its Subject, and be accepted. Expiry is not
		// checked here (deferred to Apply).
		// Reference: rippled Payment.cpp:362-365 / credentials::valid()
		if result := credential.ValidCredentials(view, senderID, p.CredentialIDs); result != ter.TesSUCCESS {
			return result
		}

		// Domain membership for permissioned payments: both source and destination
		// must belong to the named domain.
		// Reference: rippled Payment.cpp:367-376
		if p.DomainID != nil {
			domainID, err := permissioneddomain.ParseDomainID(*p.DomainID)
			if err != nil {
				return ter.TemMALFORMED
			}
			closeTime := config.ParentCloseTime
			if !permissioneddomain.AccountInDomain(view, senderID, domainID, closeTime) {
				return ter.TecNO_PERMISSION
			}
			destID, err := state.DecodeAccountID(p.Destination)
			if err != nil {
				return ter.TefINTERNAL
			}
			if !permissioneddomain.AccountInDomain(view, destID, domainID, closeTime) {
				return ter.TecNO_PERMISSION
			}
		}
	}

	return ter.TesSUCCESS
}

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
				if step.MPTIssuanceID != "" {
					stepMap["mpt_issuance_id"] = step.MPTIssuanceID
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

func (p *Payment) Apply(ctx *tx.ApplyContext) ter.Result {
	isDstMPT := p.Amount.IsMPT()
	mpTokensV2 := ctx.Rules().MPTokensV2Enabled()

	ctx.Log.Trace("payment apply",
		"src", p.Account,
		"dst", p.Destination,
		"amount", p.Amount,
		"hasPaths", len(p.Paths) > 0,
		"hasSendMax", p.SendMax != nil,
		"mpt", isDstMPT,
	)

	hasPaths := len(p.Paths) > 0 || p.HasField("Paths")
	hasSendMax := p.SendMax != nil
	ripple := (hasPaths || hasSendMax || !p.Amount.IsNative()) && (!isDstMPT || mpTokensV2)
	if ripple {
		return p.applyFlowPayment(ctx)
	}
	if isDstMPT {
		return p.applyMPTPayment(ctx)
	}

	return p.applyXRPPayment(ctx)
}

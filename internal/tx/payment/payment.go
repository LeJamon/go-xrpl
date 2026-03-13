package payment

import (
	"github.com/LeJamon/goXRPLd/amendment"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
)

func init() {
	tx.Register(tx.TypePayment, func() tx.Transaction {
		return &Payment{BaseTx: *tx.NewBaseTx(tx.TypePayment, "")}
	})
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
		return tx.Errorf(tx.TemDST_NEEDED, "Destination is required")
	}

	if p.Amount.IsZero() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
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
			return tx.Errorf(tx.TemINVALID_FLAG, "Invalid flags for MPT payment")
		}
	}

	partialPaymentAllowed := (flags & PaymentFlagPartialPayment) != 0
	limitQuality := (flags & PaymentFlagLimitQuality) != 0
	noRippleDirect := (flags & PaymentFlagNoDirectRipple) != 0
	hasPaths := len(p.Paths) > 0

	// MPT payments cannot have paths
	// Reference: rippled Payment.cpp:101-102
	if mptDirect && hasPaths {
		return tx.Errorf(tx.TemMALFORMED, "Paths not allowed for MPT payment")
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
		return tx.Errorf(tx.TemREDUNDANT, "cannot send to self without path")
	}

	// XRP to XRP with SendMax is invalid (temBAD_SEND_XRP_MAX)
	// Reference: rippled Payment.cpp:168-174
	if xrpDirect && p.SendMax != nil {
		return tx.Errorf(tx.TemBAD_SEND_XRP_MAX, "SendMax specified for XRP to XRP")
	}

	// XRP to XRP with paths is invalid (temBAD_SEND_XRP_PATHS)
	// Reference: rippled Payment.cpp:175-181
	if xrpDirect && hasPaths {
		return tx.Errorf(tx.TemBAD_SEND_XRP_PATHS, "Paths specified for XRP to XRP")
	}

	// tfPartialPayment flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_PARTIAL)
	// Reference: rippled Payment.cpp:182-188
	if xrpDirect && partialPaymentAllowed {
		return tx.Errorf(tx.TemBAD_SEND_XRP_PARTIAL, "Partial payment specified for XRP to XRP")
	}

	// tfLimitQuality flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_LIMIT)
	// Reference: rippled Payment.cpp:189-196
	if xrpDirect && limitQuality {
		return tx.Errorf(tx.TemBAD_SEND_XRP_LIMIT, "Limit quality specified for XRP to XRP")
	}

	// tfNoRippleDirect flag is invalid for XRP-to-XRP payments (temBAD_SEND_XRP_NO_DIRECT)
	// Reference: rippled Payment.cpp:197-204
	if xrpDirect && noRippleDirect {
		return tx.Errorf(tx.TemBAD_SEND_XRP_NO_DIRECT, "No ripple direct specified for XRP to XRP")
	}

	// DeliverMin can only be used with tfPartialPayment flag (temBAD_AMOUNT)
	// Reference: rippled Payment.cpp:206-214
	if p.DeliverMin != nil && !partialPaymentAllowed {
		return tx.Errorf(tx.TemBAD_AMOUNT, "DeliverMin requires tfPartialPayment flag")
	}

	// Validate DeliverMin if present
	// Reference: rippled Payment.cpp:216-238
	if p.DeliverMin != nil {
		// DeliverMin must be positive (not zero, not negative)
		if p.DeliverMin.IsZero() || p.DeliverMin.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "DeliverMin must be positive")
		}

		// DeliverMin currency must match Amount currency
		if p.DeliverMin.Currency != p.Amount.Currency || p.DeliverMin.Issuer != p.Amount.Issuer {
			return tx.Errorf(tx.TemBAD_AMOUNT, "DeliverMin currency must match Amount")
		}

		// DeliverMin cannot exceed Amount
		// Reference: rippled Payment.cpp:232-238
		if p.DeliverMin.Compare(p.Amount) > 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "DeliverMin cannot exceed Amount")
		}
	}

	// Paths array max length is 7 (temMALFORMED if exceeded)
	// Reference: rippled Payment.cpp:353-359 (MaxPathSize)
	if len(p.Paths) > MaxPathSize {
		return tx.Errorf(tx.TemMALFORMED, "Paths array exceeds maximum size of 7")
	}

	// Each path can have max 8 steps (temMALFORMED if exceeded)
	// Reference: rippled Payment.cpp:354-358 (MaxPathLength)
	for i, path := range p.Paths {
		if len(path) > MaxPathLength {
			return tx.Errorf(tx.TemMALFORMED, "Path %c exceeds maximum length of 8 steps", rune('0'+i))
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
			return tx.Errorf(tx.TemMALFORMED, "Invalid credentials array size")
		}

		// Check for duplicates
		seen := make(map[string]bool, len(p.CredentialIDs))
		for _, id := range p.CredentialIDs {
			if seen[id] {
				return tx.Errorf(tx.TemMALFORMED, "Duplicate credential ID")
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
				return tx.Errorf(tx.TemBAD_PATH, "Path element has no account, currency, or issuer")
			}

			// Account element cannot also have currency or issuer
			// Reference: rippled PaySteps.cpp:168-169
			if hasAccount && (hasCurrency || hasIssuer) {
				return tx.Errorf(tx.TemBAD_PATH, "Path element has account with currency or issuer")
			}

			// XRP issuer is invalid (issuer must not be XRP pseudo-account)
			// Reference: rippled PaySteps.cpp:171-172
			if hasIssuer && (elem.Issuer == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || elem.Issuer == "" && hasCurrency && elem.Currency == "XRP") {
				return tx.Errorf(tx.TemBAD_PATH, "Path element has XRP issuer")
			}

			// XRP account in path is invalid (account must not be XRP pseudo-account)
			// Reference: rippled PaySteps.cpp:174-175
			if hasAccount && elem.Account == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" {
				return tx.Errorf(tx.TemBAD_PATH, "Path element has XRP account")
			}

			// XRP currency with non-XRP issuer or vice versa is invalid
			// Reference: rippled PaySteps.cpp:177-179
			if hasCurrency && hasIssuer {
				isXRPCurrency := elem.Currency == "XRP" || elem.Currency == ""
				isXRPIssuer := elem.Issuer == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || elem.Issuer == ""
				if isXRPCurrency != isXRPIssuer {
					return tx.Errorf(tx.TemBAD_PATH, "XRP currency mismatch with issuer")
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

// Apply applies the Payment transaction to the ledger state.
func (p *Payment) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("payment apply",
		"src", p.Account,
		"dst", p.Destination,
		"amount", p.Amount,
		"hasPaths", len(p.Paths) > 0,
		"hasSendMax", p.SendMax != nil,
		"mpt", p.MPTokenIssuanceID != "",
	)

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

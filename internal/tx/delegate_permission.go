package tx

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Granular delegated-permission values. Granular permissions authorize a
// delegate to perform a narrow slice of a transaction type's behaviour (e.g.
// only setting a trust line's freeze flag) rather than the whole transaction.
// They occupy the value space above the maximum tx type (uint16) so they never
// collide with transaction-level permissions (which are txType + 1).
const (
	GranularTrustlineAuthorize     uint32 = 65537
	GranularTrustlineFreeze        uint32 = 65538
	GranularTrustlineUnfreeze      uint32 = 65539
	GranularAccountDomainSet       uint32 = 65540
	GranularAccountEmailHashSet    uint32 = 65541
	GranularAccountMessageKeySet   uint32 = 65542
	GranularAccountTransferRateSet uint32 = 65543
	GranularAccountTickSizeSet     uint32 = 65544
	GranularPaymentMint            uint32 = 65545
	GranularPaymentBurn            uint32 = 65546
	GranularMPTokenIssuanceLock    uint32 = 65547
	GranularMPTokenIssuanceUnlock  uint32 = 65548
)

// DelegatePermissionContext carries the ledger state a granular delegated-
// permission check needs beyond the transaction's own fields: the amendment
// rules, a read view, and the set of permission values the delegate was
// granted.
type DelegatePermissionContext struct {
	View        LedgerView
	Rules       *amendment.Rules
	Permissions []uint32
}

type granularPermission struct {
	txType        Type
	allowedFlags  uint32
	allowedFields []string
}

var granularPermissions = map[uint32]granularPermission{
	GranularTrustlineAuthorize: {
		txType: TypeTrustSet, allowedFlags: TfUniversal | 0x00010000,
		allowedFields: []string{"LimitAmount"},
	},
	GranularTrustlineFreeze: {
		txType: TypeTrustSet, allowedFlags: TfUniversal | 0x00100000,
		allowedFields: []string{"LimitAmount"},
	},
	GranularTrustlineUnfreeze: {
		txType: TypeTrustSet, allowedFlags: TfUniversal | 0x00200000,
		allowedFields: []string{"LimitAmount"},
	},
	GranularAccountDomainSet: {
		txType: TypeAccountSet, allowedFlags: TfUniversal,
		allowedFields: []string{"Domain"},
	},
	GranularAccountEmailHashSet: {
		txType: TypeAccountSet, allowedFlags: TfUniversal,
		allowedFields: []string{"EmailHash"},
	},
	GranularAccountMessageKeySet: {
		txType: TypeAccountSet, allowedFlags: TfUniversal,
		allowedFields: []string{"MessageKey"},
	},
	GranularAccountTransferRateSet: {
		txType: TypeAccountSet, allowedFlags: TfUniversal,
		allowedFields: []string{"TransferRate"},
	},
	GranularAccountTickSizeSet: {
		txType: TypeAccountSet, allowedFlags: TfUniversal,
		allowedFields: []string{"TickSize"},
	},
	GranularPaymentMint: {
		txType: TypePayment, allowedFlags: TfUniversal,
		allowedFields: []string{"Destination", "Amount", "SendMax", "InvoiceID", "DestinationTag", "CredentialIDs"},
	},
	GranularPaymentBurn: {
		txType: TypePayment, allowedFlags: TfUniversal,
		allowedFields: []string{"Destination", "Amount", "SendMax", "InvoiceID", "DestinationTag", "CredentialIDs"},
	},
	GranularMPTokenIssuanceLock: {
		txType: TypeMPTokenIssuanceSet, allowedFlags: TfUniversal | 0x00000001,
		allowedFields: []string{"MPTokenIssuanceID", "Holder"},
	},
	GranularMPTokenIssuanceUnlock: {
		txType: TypeMPTokenIssuanceSet, allowedFlags: TfUniversal | 0x00000002,
		allowedFields: []string{"MPTokenIssuanceID", "Holder"},
	},
}

var nonDelegatableTxTypes = map[Type]bool{
	TypeAccountSet:              true,
	TypeRegularKeySet:           true,
	TypeSignerListSet:           true,
	TypeAccountDelete:           true,
	TypeDelegateSet:             true,
	TypeVaultCreate:             true,
	TypeVaultSet:                true,
	TypeVaultDelete:             true,
	TypeVaultDeposit:            true,
	TypeVaultWithdraw:           true,
	TypeVaultClawback:           true,
	TypeBatch:                   true,
	TypeLoanBrokerSet:           true,
	TypeLoanBrokerDelete:        true,
	TypeLoanBrokerCoverDeposit:  true,
	TypeLoanBrokerCoverWithdraw: true,
	TypeLoanBrokerCoverClawback: true,
	TypeLoanSet:                 true,
	TypeLoanDelete:              true,
	TypeLoanManage:              true,
	TypeLoanPay:                 true,
	TypeConfidentialMPTConvert:  true,
	TypeSponsorshipTransfer:     true,
	TypeAmendment:               true,
	TypeFee:                     true,
	TypeUNLModify:               true,
}

// HasGranularPermissions reports whether a transaction type has at least one
// granular permission template.
func HasGranularPermissions(txType Type) bool {
	for _, permission := range granularPermissions {
		if permission.txType == txType {
			return true
		}
	}
	return false
}

// IsTransactionDelegable reports whether a transaction type supports a
// transaction-level permission.
func IsTransactionDelegable(txType Type) bool {
	return !nonDelegatableTxTypes[txType]
}

// GranularPermissionsFor returns the held granular permissions that apply to
// txType. Permissions belonging to another transaction type cannot contribute
// flags or fields to the transaction's permission template.
func GranularPermissionsFor(txType Type, held []uint32) []uint32 {
	permissions := make([]uint32, 0, len(held))
	for _, value := range held {
		permission, ok := granularPermissions[value]
		if ok && permission.txType == txType {
			permissions = append(permissions, value)
		}
	}
	return permissions
}

// GranularPermissionTxType returns the transaction type governed by a known
// granular permission value.
func GranularPermissionTxType(value uint32) (Type, bool) {
	permission, ok := granularPermissions[value]
	return permission.txType, ok
}

// CheckGranularPermissionTemplate reports whether every flag and field in a
// transaction is allowed by the union of its held granular permissions.
func CheckGranularPermissionTemplate(transaction Transaction, held []uint32) bool {
	if len(held) == 0 {
		return false
	}

	var allowedFlags uint32
	for _, value := range held {
		permission, ok := granularPermissions[value]
		if !ok || permission.txType != transaction.TxType() {
			return false
		}
		allowedFlags |= permission.allowedFlags
	}
	if transaction.GetCommon().GetFlags()&^allowedFlags != 0 {
		return false
	}

	fields, err := transaction.Flatten()
	if err != nil {
		return false
	}
	for field := range fields {
		if _, common := commonFieldStyles[field]; common {
			continue
		}
		allowed := false
		for _, value := range held {
			for _, candidate := range granularPermissions[value].allowedFields {
				if field == candidate {
					allowed = true
					break
				}
			}
			if allowed {
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

// HasGranular reports whether the delegate was granted the given granular
// permission value.
func (pc DelegatePermissionContext) HasGranular(value uint32) bool {
	for _, p := range pc.Permissions {
		if p == value {
			return true
		}
	}
	return false
}

// DelegatePermissionChecker is implemented by transaction types that accept
// granular delegated permissions in addition to a plain transaction-level
// grant. The engine invokes CheckDelegatePermission only when sfDelegate is
// present, the delegate ledger entry exists, and no transaction-level
// permission already covers the transaction.
type DelegatePermissionChecker interface {
	CheckDelegatePermission(pc DelegatePermissionContext) ter.Result
}

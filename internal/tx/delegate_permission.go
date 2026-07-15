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

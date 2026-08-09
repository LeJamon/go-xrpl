package invariants

// invariants.go — post-apply invariant checking matching rippled's InvariantCheck.cpp
//
// Called BEFORE table.Apply() so entries are still inspectable in the ApplyStateTable.
// On violation, the engine returns TecINVARIANT_FAILED (fee charged, state reverted).
//
// Reference: rippled/src/xrpld/app/tx/detail/InvariantCheck.cpp

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

// InitialXRP is the total XRP supply in drops (100 billion XRP).
const InitialXRP uint64 = 100_000_000_000_000_000

// InvariantEntry represents a single ledger entry modification to be checked by invariants.
// Before is nil for newly created entries; After is nil for deleted entries.
// DeleteFinal retains the final pre-erase image for deleted entries. It is
// separate from Before because metadata and most invariants need the original
// image, while cleanup and sponsorship invariants inspect the image immediately
// before the erase.
type InvariantEntry struct {
	Key         [32]byte // ledger key of the entry (for invariants like ValidNFTokenPage that need to inspect the key)
	EntryType   entry.Type
	Before      []byte // serialized SLE before the transaction (nil for inserts)
	After       []byte // serialized SLE after the transaction (nil for deletes)
	DeleteFinal []byte // serialized SLE immediately before deletion (nil for non-deletes)
	IsDelete    bool   // true if the entry was deleted
}

// InvariantViolation holds the name and description of a detected invariant violation.
type InvariantViolation struct {
	Name    string
	Message string
}

func (v *InvariantViolation) Error() string {
	return fmt.Sprintf("invariant violation %s: %s", v.Name, v.Message)
}

// Transaction is a minimal interface for the transaction fields needed by
// invariant checks. Callers in the tx package wrap their tx.Transaction in an
// adapter that satisfies this interface.
type Transaction interface {
	// TxType returns the transaction type code.
	TxType() TxType
	// TxAccount returns the transaction's Account field.
	TxAccount() string
	// TxHasField returns true if the named field was present in the original transaction.
	TxHasField(name string) bool
	// Flatten returns a flat map of all transaction fields for serialization.
	Flatten() (map[string]any, error)
}

// ReadView provides read-only access to ledger state for invariant checks.
// This is satisfied by tx.LedgerView and ApplyStateTable without importing the tx package.
type ReadView interface {
	Read(k keylet.Keylet) ([]byte, error)
	Exists(k keylet.Keylet) (bool, error)
	Succ(key [32]byte) ([32]byte, []byte, bool, error)
	// LedgerSeq returns the current (building) ledger sequence. Mirrors
	// rippled ReadView::seq(), used by ValidNewAccountRoot when
	// featureDeletableAccounts is enabled.
	LedgerSeq() uint32
}

// TxType is the transaction type code used by invariant checks. It aliases
// protocol.TxType so the type table is single-sourced and never drifts; the
// String() method (covering every type, including the XChain attestation types
// 45/46 that ValidNewAccountRoot permits) comes from protocol.
type TxType = protocol.TxType

// Transaction type constants used by invariant checks, aliased from the
// protocol package.
const (
	TypePayment                    = protocol.TxTypePayment
	TypeEscrowFinish               = protocol.TxTypeEscrowFinish
	TypeOfferCreate                = protocol.TxTypeOfferCreate
	TypeCheckCash                  = protocol.TxTypeCheckCash
	TypeAccountDelete              = protocol.TxTypeAccountDelete
	TypeNFTokenMint                = protocol.TxTypeNFTokenMint
	TypeNFTokenBurn                = protocol.TxTypeNFTokenBurn
	TypeClawback                   = protocol.TxTypeClawback
	TypeAMMClawback                = protocol.TxTypeAMMClawback
	TypeAMMCreate                  = protocol.TxTypeAMMCreate
	TypeAMMDeposit                 = protocol.TxTypeAMMDeposit
	TypeAMMWithdraw                = protocol.TxTypeAMMWithdraw
	TypeAMMVote                    = protocol.TxTypeAMMVote
	TypeAMMBid                     = protocol.TxTypeAMMBid
	TypeAMMDelete                  = protocol.TxTypeAMMDelete
	TypeMPTokenIssuanceCreate      = protocol.TxTypeMPTokenIssuanceCreate
	TypeMPTokenIssuanceDestroy     = protocol.TxTypeMPTokenIssuanceDestroy
	TypeMPTokenIssuanceSet         = protocol.TxTypeMPTokenIssuanceSet
	TypeMPTokenAuthorize           = protocol.TxTypeMPTokenAuthorize
	TypePermissionedDomainSet      = protocol.TxTypePermissionedDomainSet
	TypePermissionedDomainDelete   = protocol.TxTypePermissionedDomainDelete
	TypeVaultCreate                = protocol.TxTypeVaultCreate
	TypeVaultDelete                = protocol.TxTypeVaultDelete
	TypeVaultDeposit               = protocol.TxTypeVaultDeposit
	TypeBatch                      = protocol.TxTypeBatch
	TypeConfidentialMPTConvert     = protocol.TxTypeConfidentialMPTConvert
	TypeConfidentialMPTMergeInbox  = protocol.TxTypeConfidentialMPTMergeInbox
	TypeConfidentialMPTConvertBack = protocol.TxTypeConfidentialMPTConvertBack
	TypeConfidentialMPTSend        = protocol.TxTypeConfidentialMPTSend
	TypeConfidentialMPTClawback    = protocol.TxTypeConfidentialMPTClawback
)

// Result represents a transaction result code.
type Result int

// Result constants used by invariant checks.
const (
	TesSUCCESS    Result = 0
	TecINCOMPLETE Result = 169
)

// Amount is the type used by invariant checks for XRPL amounts.
type Amount = state.Amount

// Asset represents an XRPL asset: XRP, an issued currency (currency + issuer),
// or a multi-purpose token (MPTIssuanceID). Field-identical to tx.Asset so the
// engine adapter can convert without losing information (notably MPTIssuanceID,
// which an MPT-asset AMM invariant needs to locate the pool holding).
type Asset struct {
	Currency      string `json:"currency"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
}

// IsMPT reports whether the asset is a multi-purpose token.
func (a Asset) IsMPT() bool { return a.MPTIssuanceID != "" }

// IsNative reports whether the asset is native XRP.
func (a Asset) IsNative() bool {
	return !a.IsMPT() && a.Issuer == "" && (a.Currency == "" || a.Currency == "XRP")
}

var validLedgerEntryTypes = func() map[entry.Type]struct{} {
	types := make(map[entry.Type]struct{})
	for _, info := range protocol.LedgerEntryTypes() {
		if !info.Deprecated {
			types[info.Type] = struct{}{}
		}
	}
	return types
}()

// maxPermissionedDomainCredentials is the maximum number of credentials in a
// PermissionedDomain's AcceptedCredentials array.
// Reference: rippled Protocol.h — maxPermissionedDomainCredentialsArraySize = 10
const maxPermissionedDomainCredentials = 10

// CheckInvariants runs all invariant checkers against the set of modified entries.
// tx is the transaction being applied (for invariants that need to inspect transaction fields).
// result is the transaction result before any invariant override.
// fee is the fee in drops actually charged for this transaction.
// txDeclaredFee is the fee declared in the transaction itself (for TransactionFeeCheck).
// entries is the slice returned by ApplyStateTable.CollectEntries().
// view is the ledger view for invariants that need to read ledger state.
// rules is the amendment rules for amendment-gated invariant behavior.
//
// Returns non-nil if any invariant is violated.
// Reference: rippled InvariantCheck.h — finalize(STTx const&, TER, XRPAmount, ReadView const&, ...)
func CheckInvariants(tx Transaction, result Result, fee uint64, txDeclaredFee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContext ...state.NumberContext) *InvariantViolation {
	txType := tx.TxType().String()
	checks := []func() *InvariantViolation{
		func() *InvariantViolation { return checkTransactionFee(fee, txDeclaredFee) },
		func() *InvariantViolation { return checkXRPBalances(entries) },
		func() *InvariantViolation { return checkXRPNotCreated(result, fee, entries) },
		func() *InvariantViolation { return checkAccountRootsNotDeleted(txType, result, entries) },
		func() *InvariantViolation { return checkLedgerEntryTypesMatch(entries) },
		func() *InvariantViolation { return checkSponsorship(entries) },
		func() *InvariantViolation { return checkNoXRPTrustLines(entries) },
		func() *InvariantViolation {
			return checkNoDeepFreezeTrustLinesWithoutFreeze(entries)
		},
		func() *InvariantViolation {
			return checkTransfersNotFrozen(tx, entries, view, rules, numberContext...)
		},
		func() *InvariantViolation { return checkNoBadOffers(entries) },
		func() *InvariantViolation { return checkNoZeroEscrow(entries) },
		func() *InvariantViolation {
			return checkValidNewAccountRoot(txType, result, entries, view, rules)
		},
		func() *InvariantViolation {
			return checkNFTokenCountTracking(txType, result, entries)
		},
		func() *InvariantViolation {
			return checkValidClawback(tx, result, entries, view)
		},
		func() *InvariantViolation {
			return checkValidMPTIssuance(tx, result, entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidConfidentialMPToken(tx, result, entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDomain(tx, result, entries, rules)
		},
		func() *InvariantViolation {
			return checkValidNFTokenPage(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkAccountRootsDeletedClean(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDEX(tx, result, entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidBookDirectory(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidAMM(tx, result, entries, view, rules, numberContext...)
		},
		func() *InvariantViolation {
			return checkValidPseudoAccounts(entries, rules)
		},
		func() *InvariantViolation {
			return checkValidLoan(entries, rules)
		},
		func() *InvariantViolation {
			return checkValidLoanBroker(entries, view, rules, numberContext...)
		},
		func() *InvariantViolation {
			return checkValidVault(tx, result, fee, entries, view, rules, numberContext...)
		},
		func() *InvariantViolation {
			return checkNoModifiedUnmodifiableFields(entries, rules)
		},
		func() *InvariantViolation {
			return checkValidAmounts(entries, rules)
		},
	}
	for _, check := range checks {
		if v := check(); v != nil {
			return v
		}
	}
	return nil
}

// ClawbackAmountProvider is optionally implemented by Clawback transactions
// so the invariant checker can access the Amount field without importing the
// clawback subpackage.
type ClawbackAmountProvider interface {
	ClawbackAmount() Amount
}

// HolderFieldProvider is optionally implemented by transactions that have a
// Holder field (e.g., MPTokenAuthorize). Used by ValidMPTIssuance to determine
// whether the transaction was submitted by the issuer (Holder field present)
// or the holder (Holder field absent).
type HolderFieldProvider interface {
	HasHolder() bool
}

// DomainIDProvider is implemented by transactions that may have a DomainID field.
type DomainIDProvider interface {
	GetDomainID() (*[32]byte, bool)
}

// AMMAssetProvider is implemented by AMMDeposit, AMMWithdraw, and AMMClawback
// (via the adapter) to provide the AMM's asset pair.
type AMMAssetProvider interface {
	GetAMMAsset() Asset
	GetAMMAsset2() Asset
}

// AMMCreateIssueProvider is implemented by AMMCreate (via the adapter) to provide
// the asset issues from Amount and Amount2 fields.
type AMMCreateIssueProvider interface {
	GetAmountAsset() Asset
	GetAmount2Asset() Asset
}

package invariants

// invariants.go — post-apply invariant checking matching rippled's InvariantCheck.cpp
//
// Called BEFORE table.Apply() so entries are still inspectable in the ApplyStateTable.
// On violation, the engine returns TecINVARIANT_FAILED (fee charged, state reverted).
//
// Reference: rippled/src/xrpld/app/tx/detail/InvariantCheck.cpp

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// InitialXRP is the total XRP supply in drops (100 billion XRP).
const InitialXRP uint64 = 100_000_000_000_000_000

// xrpCurrencyBytes is the canonical XRP currency representation (all zeros in the 20-byte currency field).
var xrpCurrencyBytes = make([]byte, 20)

// InvariantEntry represents a single ledger entry modification to be checked by invariants.
// Before is nil for newly created entries; After is nil for deleted entries.
type InvariantEntry struct {
	Key       [32]byte // ledger key of the entry (for invariants like ValidNFTokenPage that need to inspect the key)
	EntryType string   // e.g. "AccountRoot", "RippleState", "Offer", "Escrow", "PayChannel"
	Before    []byte   // serialized SLE before the transaction (nil for inserts)
	After     []byte   // serialized SLE after the transaction (nil for deletes)
	IsDelete  bool     // true if the entry was deleted
}

// InvariantViolation holds the name and description of a detected invariant violation.
type InvariantViolation struct {
	Name    string
	Message string
}

func (v *InvariantViolation) Error() string {
	return fmt.Sprintf("invariant violation %s: %s", v.Name, v.Message)
}

// Transaction is a minimal interface for the transaction fields needed by invariant checks.
// This is satisfied by tx.Transaction (since tx.Type and TxType are both uint16).
// Callers in the tx package cast their tx.Transaction to this interface.
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
}

// TxType represents a transaction type code.
type TxType uint16

// String returns the string name of the transaction type.
// Only covers types used by invariant checks.
func (t TxType) String() string {
	switch t {
	case TypePayment:
		return "Payment"
	case TypeEscrowFinish:
		return "EscrowFinish"
	case TypeOfferCreate:
		return "OfferCreate"
	case TypeCheckCash:
		return "CheckCash"
	case TypeAccountDelete:
		return "AccountDelete"
	case TypeNFTokenMint:
		return "NFTokenMint"
	case TypeNFTokenBurn:
		return "NFTokenBurn"
	case TypeClawback:
		return "Clawback"
	case TypeAMMClawback:
		return "AMMClawback"
	case TypeAMMCreate:
		return "AMMCreate"
	case TypeAMMDeposit:
		return "AMMDeposit"
	case TypeAMMWithdraw:
		return "AMMWithdraw"
	case TypeAMMVote:
		return "AMMVote"
	case TypeAMMBid:
		return "AMMBid"
	case TypeAMMDelete:
		return "AMMDelete"
	case TypeMPTokenIssuanceCreate:
		return "MPTokenIssuanceCreate"
	case TypeMPTokenIssuanceDestroy:
		return "MPTokenIssuanceDestroy"
	case TypeMPTokenIssuanceSet:
		return "MPTokenIssuanceSet"
	case TypeMPTokenAuthorize:
		return "MPTokenAuthorize"
	case TypePermissionedDomainSet:
		return "PermissionedDomainSet"
	case TypeVaultCreate:
		return "VaultCreate"
	case TypeVaultDelete:
		return "VaultDelete"
	case TypeVaultDeposit:
		return "VaultDeposit"
	case TypeBatch:
		return "Batch"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// Transaction type constants used by invariant checks.
// These match the tx.Type constants exactly.
const (
	TypePayment                TxType = 0
	TypeEscrowFinish           TxType = 2
	TypeOfferCreate            TxType = 7
	TypeCheckCash              TxType = 17
	TypeAccountDelete          TxType = 21
	TypeNFTokenMint            TxType = 25
	TypeNFTokenBurn            TxType = 26
	TypeClawback               TxType = 30
	TypeAMMClawback            TxType = 31
	TypeAMMCreate              TxType = 35
	TypeAMMDeposit             TxType = 36
	TypeAMMWithdraw            TxType = 37
	TypeAMMVote                TxType = 38
	TypeAMMBid                 TxType = 39
	TypeAMMDelete              TxType = 40
	TypeMPTokenIssuanceCreate  TxType = 54
	TypeMPTokenIssuanceDestroy TxType = 55
	TypeMPTokenIssuanceSet     TxType = 56
	TypeMPTokenAuthorize       TxType = 57
	TypePermissionedDomainSet  TxType = 62
	TypeVaultCreate            TxType = 65
	TypeVaultDelete            TxType = 67
	TypeVaultDeposit           TxType = 68
	TypeBatch                  TxType = 71
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

// Asset represents an XRPL asset (currency + optional issuer).
type Asset struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}

// validLedgerEntryTypes is the set of valid ledger entry type names that may be
// created in the ledger. Matches rippled's LedgerEntryTypesMatch whitelist.
// Reference: rippled InvariantCheck.cpp lines 517-546
var validLedgerEntryTypes = map[string]bool{
	"AccountRoot":                     true,
	"Delegate":                        true,
	"DirectoryNode":                   true,
	"RippleState":                     true,
	"Ticket":                          true,
	"SignerList":                      true,
	"Offer":                           true,
	"LedgerHashes":                    true,
	"Amendments":                      true,
	"FeeSettings":                     true,
	"Escrow":                          true,
	"PayChannel":                      true,
	"Check":                           true,
	"DepositPreauth":                  true,
	"NegativeUNL":                     true,
	"NFTokenPage":                     true,
	"NFTokenOffer":                    true,
	"AMM":                             true,
	"Bridge":                          true,
	"XChainOwnedClaimID":              true,
	"XChainOwnedCreateAccountClaimID": true,
	"DID":                             true,
	"Oracle":                          true,
	"MPTokenIssuance":                 true,
	"MPToken":                         true,
	"Credential":                      true,
	"PermissionedDomain":              true,
	"Vault":                           true,
}

// misEncodedTypeAliases maps binary type codes that are incorrect due to a known
// codec bug (UInt16.FromJSON prefers transaction type codes over ledger entry type
// codes when names overlap) to the intended ledger entry type name.
var misEncodedTypeAliases = map[uint16]string{
	19: "DepositPreauth", // tx type 0x0013 written instead of SLE type 0x0070
}

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
func CheckInvariants(tx Transaction, result Result, fee uint64, txDeclaredFee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	txType := tx.TxType().String()
	checks := []func() *InvariantViolation{
		func() *InvariantViolation { return checkTransactionFee(fee, txDeclaredFee) },
		func() *InvariantViolation { return checkXRPBalances(entries) },
		func() *InvariantViolation { return checkXRPNotCreated(result, fee, entries) },
		func() *InvariantViolation { return checkAccountRootsNotDeleted(txType, result, entries) },
		func() *InvariantViolation { return checkLedgerEntryTypesMatch(entries) },
		func() *InvariantViolation { return checkNoXRPTrustLines(entries) },
		func() *InvariantViolation {
			return checkNoDeepFreezeTrustLinesWithoutFreeze(entries)
		},
		func() *InvariantViolation {
			return checkTransfersNotFrozen(tx, entries, view, rules)
		},
		func() *InvariantViolation { return checkNoBadOffers(entries) },
		func() *InvariantViolation { return checkNoZeroEscrow(entries) },
		func() *InvariantViolation { return checkValidNewAccountRoot(txType, entries) },
		func() *InvariantViolation {
			return checkNFTokenCountTracking(txType, result, entries)
		},
		func() *InvariantViolation {
			return checkValidClawback(tx, result, entries, view)
		},
		func() *InvariantViolation {
			return checkValidMPTIssuance(tx, result, entries)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDomain(tx, result, entries)
		},
		func() *InvariantViolation {
			return checkValidNFTokenPage(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkAccountRootsDeletedClean(entries, view, rules)
		},
		func() *InvariantViolation {
			return checkValidPermissionedDEX(tx, result, entries, view)
		},
		func() *InvariantViolation {
			return checkValidAMM(tx, result, entries, view, rules)
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


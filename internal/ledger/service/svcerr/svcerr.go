// Package svcerr defines the typed sentinel errors returned by the
// ledger service. Lives in its own leaf package so callers (RPC
// handlers, tests) can compare via errors.Is without taking a heavy
// dependency on internal/ledger/service.
package svcerr

import (
	"errors"
	"fmt"
)

var (
	// ErrAccountNotFound is returned when a queried account does not
	// exist in the current ledger.
	ErrAccountNotFound = errors.New("account not found")

	// ErrSrcAccountNotFound is returned when the deposit_authorized
	// source account is absent from the queried ledger.
	ErrSrcAccountNotFound = errors.New("source account not found")

	// ErrDstAccountNotFound is returned when the deposit_authorized
	// destination account is absent from the queried ledger.
	ErrDstAccountNotFound = errors.New("destination account not found")

	// ErrBadCredentials flags credential-validation failures. Wrapped
	// errors carry human-readable detail (e.g. "credentials are expired").
	ErrBadCredentials = errors.New("bad credentials")

	// ErrLedgerEntryNotFound is returned when a ledger_entry lookup
	// resolves to no matching SLE.
	ErrLedgerEntryNotFound = errors.New("entry not found")

	// ErrLedgerNotFound is returned when a ledger query targets a
	// ledger sequence/hash that is not present.
	ErrLedgerNotFound = errors.New("ledger not found")

	// ErrAccountMalformed wraps a malformed-address decode failure so
	// handlers can map it to rpcACT_MALFORMED via errors.Is.
	ErrAccountMalformed = errors.New("invalid account address")

	// ErrNoOpenLedger is returned when an operation requires an open
	// ledger but none is available (e.g. before the first close).
	ErrNoOpenLedger = errors.New("no open ledger")

	// ErrNoClosedLedger is returned when an operation requires the
	// last closed ledger but none has been produced yet.
	ErrNoClosedLedger = errors.New("no closed ledger")

	// ErrNotStandalone is returned when an operation is only valid in
	// standalone mode.
	ErrNotStandalone = errors.New("operation only valid in standalone mode")

	// ErrObjectNotFound is returned when a ledger lookup resolves to no
	// matching object (rippled rpcOBJECT_NOT_FOUND).
	ErrObjectNotFound = errors.New("object not found")

	// ErrInvalidMarker is returned when a paginated query supplies a
	// marker that is syntactically bad or refers to an entry in the
	// wrong scope (e.g. an offer in a different book). Maps to rippled's
	// invalid_field_error("marker") at AccountOffers.cpp:107-121.
	ErrInvalidMarker = errors.New("invalid marker")

	// ErrStaleMarker is returned when a paginated query supplies a
	// well-formed marker that pointed at an entry which has since been
	// removed (e.g. an offer consumed by a Payment between pages). It is
	// recoverable by retrying against a pinned ledger_index/ledger_hash.
	// Maps to rippled's rpcINVALID_PARAMS "object pointed to by the
	// marker does not exist" at AccountOffers.cpp:128-132.
	ErrStaleMarker = errors.New("stale marker")

	// ErrHighFee is the sentinel matched by errors.Is for any high-fee
	// failure. The fee-autofill path returns the structured HighFeeError
	// (which Is-matches this sentinel) so handlers can extract Fee/Limit
	// directly. Mirrors rippled TransactionSign.cpp getCurrentNetworkFee.
	ErrHighFee = errors.New("high fee")
)

// HighFeeError carries the structured payload of a fee-autofill rejection:
// the computed fee and the autofill ceiling that capped it. The Error()
// body matches rippled's wire message ("Fee of X exceeds the requested tx
// limit of Y", TransactionSign.cpp:870-873). errors.Is(err, ErrHighFee)
// matches via the Is method below.
type HighFeeError struct {
	Fee   uint64
	Limit uint64
}

func (e *HighFeeError) Error() string {
	return fmt.Sprintf("Fee of %d exceeds the requested tx limit of %d", e.Fee, e.Limit)
}

func (e *HighFeeError) Is(target error) bool {
	return target == ErrHighFee
}

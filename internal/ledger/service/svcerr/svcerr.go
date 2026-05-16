// Package svcerr defines the typed sentinel errors returned by the
// ledger service. Lives in its own leaf package so callers (RPC
// handlers, tests) can compare via errors.Is without taking a heavy
// dependency on internal/ledger/service.
package svcerr

import "errors"

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
	// marker that does not parse or does not match an existing entry.
	ErrInvalidMarker = errors.New("invalid marker")
)

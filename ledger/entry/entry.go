package entry

import "github.com/LeJamon/go-xrpl/protocol"

// Type represents a ledger entry type.
type Type = protocol.LedgerEntryType

// Pseudo-types used only as keylet constraints.
const (
	TypeAny   = protocol.LedgerEntryTypeAny
	TypeChild = protocol.LedgerEntryTypeChild
)

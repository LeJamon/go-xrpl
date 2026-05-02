package grpc

import (
	"encoding/hex"
	"errors"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/protocol"
)

// Common errors for gRPC handlers
var (
	ErrLedgerNotFound         = errors.New("ledger not found")
	ErrInvalidLedgerHash      = errors.New("invalid ledger hash")
	ErrInvalidLedgerIndex     = errors.New("invalid ledger index")
	ErrNoValidatedLedger      = errors.New("no validated ledger available")
	ErrNoClosedLedger         = errors.New("no closed ledger available")
	ErrNoCurrentLedger        = errors.New("no current ledger available")
	ErrInvalidMarker          = errors.New("invalid marker format")
	ErrEntryNotFound          = errors.New("ledger entry not found")
	ErrSerializationFailed    = errors.New("failed to serialize ledger object")
	ErrInvalidLedgerSpecifier = errors.New("invalid ledger specifier")
)

// LedgerSpecifier represents a way to identify a specific ledger.
// This mirrors the protobuf LedgerSpecifier message.
type LedgerSpecifier struct {
	// Shortcut is a named ledger reference (validated, current, closed)
	Shortcut string

	// Sequence is the ledger sequence number
	Sequence uint32

	// Hash is the ledger hash as a 32-byte array
	Hash [32]byte

	// HasSequence indicates if Sequence was explicitly set
	HasSequence bool

	// HasHash indicates if Hash was explicitly set
	HasHash bool
}

// LedgerShortcut constants matching rippled's gRPC API
const (
	LedgerShortcutValidated = "validated"
	LedgerShortcutCurrent   = "current"
	LedgerShortcutClosed    = "closed"
)

// ledgerFromSpecifier resolves a LedgerSpecifier to a concrete ledger.
// It follows the same resolution logic as rippled's gRPC handlers.
func ledgerFromSpecifier(spec *LedgerSpecifier, svc LedgerServiceInterface) (*ledger.Ledger, bool, error) {
	if spec == nil {
		// Default to validated ledger if no specifier provided
		l := svc.GetValidatedLedger()
		if l == nil {
			return nil, false, ErrNoValidatedLedger
		}
		return l, true, nil
	}

	// Priority: Hash > Sequence > Shortcut
	if spec.HasHash {
		l, err := svc.GetLedgerByHash(spec.Hash)
		if err != nil {
			return nil, false, ErrLedgerNotFound
		}
		return l, l.IsValidated(), nil
	}

	if spec.HasSequence {
		l, err := svc.GetLedgerBySequence(spec.Sequence)
		if err != nil {
			return nil, false, ErrLedgerNotFound
		}
		return l, l.IsValidated(), nil
	}

	// Handle shortcut
	switch spec.Shortcut {
	case LedgerShortcutValidated, "":
		l := svc.GetValidatedLedger()
		if l == nil {
			return nil, false, ErrNoValidatedLedger
		}
		return l, true, nil

	case LedgerShortcutCurrent:
		l := svc.GetOpenLedger()
		if l == nil {
			return nil, false, ErrNoCurrentLedger
		}
		return l, false, nil

	case LedgerShortcutClosed:
		l := svc.GetClosedLedger()
		if l == nil {
			return nil, false, ErrNoClosedLedger
		}
		validated := svc.GetValidatedLedger()
		isValidated := validated != nil && l.Hash() == validated.Hash()
		return l, isValidated, nil

	default:
		return nil, false, ErrInvalidLedgerSpecifier
	}
}

// serializeLedgerObject serializes a ledger entry (SLE) to its binary representation.
// The data is already in binary format from the ledger, so this mainly handles
// any additional processing needed for the gRPC response.
func serializeLedgerObject(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrSerializationFailed
	}
	// The data from the ledger is already in the canonical binary format
	// No additional serialization is needed
	return data, nil
}

// serializeLedgerHeader serializes a ledger header to its binary representation.
func serializeLedgerHeader(hdr header.LedgerHeader) ([]byte, error) {
	data, err := header.AddRaw(hdr, true)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// parseMarker parses a pagination marker from a hex string.
// Returns the 32-byte key and any error.
func parseMarker(markerStr string) ([32]byte, error) {
	var marker [32]byte
	if markerStr == "" {
		return marker, nil
	}

	if len(markerStr) != 64 {
		return marker, ErrInvalidMarker
	}

	decoded, err := hex.DecodeString(markerStr)
	if err != nil {
		return marker, ErrInvalidMarker
	}

	if len(decoded) != 32 {
		return marker, ErrInvalidMarker
	}

	copy(marker[:], decoded)
	return marker, nil
}

// formatMarker formats a 32-byte key as a hex string marker.
func formatMarker(key [32]byte) string {
	return hex.EncodeToString(key[:])
}

// formatHash formats a 32-byte hash as an uppercase hex string.
func formatHash(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}

// toRippleTime converts a time.Time to seconds since the Ripple epoch.
func toRippleTime(t time.Time) uint32 {
	return uint32(t.Unix() - protocol.RippleEpochUnix)
}

// getLedgerEntryType extracts the ledger entry type from serialized data.
// Returns the type code or 0 if it cannot be determined.
func getLedgerEntryType(data []byte) uint16 {
	if len(data) < 3 {
		return 0
	}
	// Check for LedgerEntryType field (type code 1, field code 1 = 0x11)
	if data[0] != 0x11 {
		return 0
	}
	return uint16(data[1])<<8 | uint16(data[2])
}

// getLedgerEntryTypeName returns the human-readable name for a ledger entry type.
func getLedgerEntryTypeName(typeCode uint16) string {
	switch typeCode {
	case 0x0061: // 'a'
		return "AccountRoot"
	case 0x0063: // 'c'
		return "Check"
	case 0x0064: // 'd'
		return "DirectoryNode"
	case 0x0066: // 'f'
		return "FeeSettings"
	case 0x0068: // 'h'
		return "Escrow"
	case 0x006E: // 'n'
		return "NFTokenPage"
	case 0x006F: // 'o'
		return "Offer"
	case 0x0070: // 'p'
		return "PayChannel"
	case 0x0072: // 'r'
		return "RippleState"
	case 0x0073: // 's'
		return "SignerList"
	case 0x0074: // 't'
		return "Ticket"
	case 0x0075: // 'u'
		return "NFTokenOffer"
	case 0x0078: // 'x'
		return "AMM"
	case 0x0041: // 'A'
		return "Amendments"
	case 0x004C: // 'L'
		return "LedgerHashes"
	case 0x004E: // 'N'
		return "NegativeUNL"
	case 0x0044: // 'D'
		return "DID"
	default:
		return "Unknown"
	}
}

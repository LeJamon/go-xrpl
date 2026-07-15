package state

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

// Field type code for UInt8 (not defined in account_root.go)
const (
	FieldTypeUInt8   = 16
	FieldTypeHash192 = 21
)

// MPTokenIssuanceData holds parsed fields of an MPTokenIssuance ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN_ISSUANCE
type MPTokenIssuanceData struct {
	Issuer            [20]byte
	Sequence          uint32
	OwnerNode         uint64
	OutstandingAmount uint64
	TransferFee       uint16
	AssetScale        uint8
	MaximumAmount     *uint64
	LockedAmount      *uint64
	MPTokenMetadata   string  // hex-encoded
	DomainID          *string // hex-encoded 32-byte hash, nil if not set
	ReferenceHolding  *string // hex-encoded 32-byte hash (vault share underlying), nil if not set
	Flags             uint32
	MutableFlags      uint32 // soeDEFAULT: CanMutate permission bits, 0 when absent

	// Threading fields. MPTokenIssuance is a threaded type, so these must
	// survive a parse→serialize round-trip — otherwise a re-serialize during
	// MPTokenIssuanceSet drops them, the bytes differ from the original, and a
	// no-op (e.g. locking an already-locked issuance) emits a spurious
	// ModifiedNode that rippled drops (*curNode == *origNode). Mirrors the
	// DirectoryNode threading-field fix. Reference: ApplyStateTable.cpp:156-157.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// MPTokenData holds parsed fields of an MPToken ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN
type MPTokenData struct {
	Account           [20]byte
	MPTokenIssuanceID [24]byte // Hash192 (24 bytes)
	OwnerNode         uint64
	MPTAmount         uint64
	LockedAmount      *uint64
	Flags             uint32

	// Threading fields — see MPTokenIssuanceData. Dropping them on round-trip
	// makes MPTokenIssuanceSet on a holder token (lock/unlock) emit a spurious
	// ModifiedNode for a no-op.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// ParseMPTokenIssuance parses an MPTokenIssuance ledger entry from binary data.
func ParseMPTokenIssuance(data []byte) (*MPTokenIssuanceData, error) {
	decoded := ledgerfields.New("MPTokenIssuance")
	if decoded == nil {
		return nil, fmt.Errorf("ledgerfields: MPTokenIssuance decoder is not registered")
	}
	if err := decoded.Decode(data); err != nil {
		return nil, err
	}
	wire, ok := decoded.(*ledgerfields.MPTokenIssuance)
	if !ok {
		return nil, fmt.Errorf("ledgerfields: MPTokenIssuance decoder has type %T", decoded)
	}

	issuance := &MPTokenIssuanceData{
		Sequence:          wire.Sequence,
		TransferFee:       uint16(wire.TransferFee),
		AssetScale:        uint8(wire.AssetScale),
		MPTokenMetadata:   strings.ToLower(wire.MPTokenMetadata),
		Flags:             wire.Flags,
		MutableFlags:      wire.MutableFlags,
		PreviousTxnLgrSeq: wire.PreviousTxnLgrSeq,
	}
	var err error
	if wire.Issuer != "" {
		issuance.Issuer, err = DecodeAccountID(wire.Issuer)
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPTokenIssuance Issuer: %w", err)
		}
	}
	if wire.OwnerNode != "" {
		issuance.OwnerNode, err = parseMPTUint64(wire.OwnerNode, 16, "OwnerNode")
		if err != nil {
			return nil, err
		}
	}
	if wire.OutstandingAmount != "" {
		issuance.OutstandingAmount, err = parseMPTUint64(wire.OutstandingAmount, 10, "OutstandingAmount")
		if err != nil {
			return nil, err
		}
	}
	issuance.MaximumAmount, err = parseMPTOptionalUint64(wire.MaximumAmount, "MaximumAmount")
	if err != nil {
		return nil, err
	}
	issuance.LockedAmount, err = parseMPTOptionalUint64(wire.LockedAmount, "LockedAmount")
	if err != nil {
		return nil, err
	}
	if wire.DomainID != "" {
		domainID := strings.ToLower(wire.DomainID)
		issuance.DomainID = &domainID
	}
	if wire.ReferenceHolding != "" {
		referenceHolding := strings.ToLower(wire.ReferenceHolding)
		issuance.ReferenceHolding = &referenceHolding
	}
	if err := decodeMPTFixedHex(wire.PreviousTxnID, issuance.PreviousTxnID[:], "PreviousTxnID"); err != nil {
		return nil, err
	}

	return issuance, nil
}

// SerializeMPTokenIssuance serializes an MPTokenIssuance to binary format.
func SerializeMPTokenIssuance(issuance *MPTokenIssuanceData) ([]byte, error) {
	issuerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(issuance.Issuer[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode issuer address: %w", err)
	}

	entry := &ledgerfields.MPTokenIssuance{}
	entry.SetFlags(issuance.Flags)
	entry.SetIssuer(issuerAddress)
	entry.SetSequence(issuance.Sequence)
	entry.SetOwnerNode(fmt.Sprintf("%x", issuance.OwnerNode))
	entry.SetOutstandingAmount(fmt.Sprintf("%d", issuance.OutstandingAmount))

	entry.SetTransferFee(issuance.TransferFee)
	entry.SetAssetScale(issuance.AssetScale)

	if issuance.MaximumAmount != nil {
		entry.SetMaximumAmount(fmt.Sprintf("%d", *issuance.MaximumAmount))
	}

	if issuance.LockedAmount != nil {
		entry.SetLockedAmount(fmt.Sprintf("%d", *issuance.LockedAmount))
	}

	if issuance.MPTokenMetadata != "" {
		entry.SetMPTokenMetadata(strings.ToUpper(issuance.MPTokenMetadata))
	}

	if issuance.DomainID != nil && *issuance.DomainID != "" {
		entry.SetDomainID(strings.ToUpper(*issuance.DomainID))
	}

	if issuance.ReferenceHolding != nil && *issuance.ReferenceHolding != "" {
		entry.SetReferenceHolding(strings.ToUpper(*issuance.ReferenceHolding))
	}

	entry.SetMutableFlags(issuance.MutableFlags)

	var zeroHash [32]byte
	if issuance.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(issuance.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(issuance.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

// ParseMPToken parses an MPToken ledger entry from binary data.
func ParseMPToken(data []byte) (*MPTokenData, error) {
	decoded := ledgerfields.New("MPToken")
	if decoded == nil {
		return nil, fmt.Errorf("ledgerfields: MPToken decoder is not registered")
	}
	if err := decoded.Decode(data); err != nil {
		return nil, err
	}
	wire, ok := decoded.(*ledgerfields.MPToken)
	if !ok {
		return nil, fmt.Errorf("ledgerfields: MPToken decoder has type %T", decoded)
	}

	token := &MPTokenData{
		Flags:             wire.Flags,
		PreviousTxnLgrSeq: wire.PreviousTxnLgrSeq,
	}
	var err error
	if wire.Account != "" {
		token.Account, err = DecodeAccountID(wire.Account)
		if err != nil {
			return nil, fmt.Errorf("failed to decode MPToken Account: %w", err)
		}
	}
	if err := decodeMPTFixedHex(wire.MPTokenIssuanceID, token.MPTokenIssuanceID[:], "MPTokenIssuanceID"); err != nil {
		return nil, err
	}
	if wire.OwnerNode != "" {
		token.OwnerNode, err = parseMPTUint64(wire.OwnerNode, 16, "OwnerNode")
		if err != nil {
			return nil, err
		}
	}
	if wire.MPTAmount != "" {
		token.MPTAmount, err = parseMPTUint64(wire.MPTAmount, 10, "MPTAmount")
		if err != nil {
			return nil, err
		}
	}
	token.LockedAmount, err = parseMPTOptionalUint64(wire.LockedAmount, "LockedAmount")
	if err != nil {
		return nil, err
	}
	if err := decodeMPTFixedHex(wire.PreviousTxnID, token.PreviousTxnID[:], "PreviousTxnID"); err != nil {
		return nil, err
	}

	return token, nil
}

func parseMPTUint64(value string, base int, field string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, base, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to decode %s: %w", field, err)
	}
	return parsed, nil
}

func parseMPTOptionalUint64(value, field string) (*uint64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseMPTUint64(value, 10, field)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func decodeMPTFixedHex(value string, destination []byte, field string) error {
	if value == "" {
		return nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return fmt.Errorf("failed to decode %s: %w", field, err)
	}
	if len(decoded) != len(destination) {
		return fmt.Errorf("failed to decode %s: got %d bytes, want %d", field, len(decoded), len(destination))
	}
	copy(destination, decoded)
	return nil
}

// SerializeMPToken serializes an MPToken to binary format.
func SerializeMPToken(token *MPTokenData) ([]byte, error) {
	accountAddress, err := addresscodec.EncodeAccountIDToClassicAddress(token.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}

	entry := &ledgerfields.MPToken{}
	entry.SetFlags(token.Flags)
	entry.SetAccount(accountAddress)
	entry.SetMPTokenIssuanceID(strings.ToUpper(hex.EncodeToString(token.MPTokenIssuanceID[:])))
	entry.SetOwnerNode(fmt.Sprintf("%x", token.OwnerNode))
	entry.SetMPTAmount(fmt.Sprintf("%d", token.MPTAmount))

	if token.LockedAmount != nil {
		entry.SetLockedAmount(fmt.Sprintf("%d", *token.LockedAmount))
	}

	var zeroHash [32]byte
	if token.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(token.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(token.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

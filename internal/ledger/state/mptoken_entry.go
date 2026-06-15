package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
	Flags             uint32

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
	issuance := &MPTokenIssuanceData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt8:
			if f.FieldCode == 5 { // AssetScale (nth=5)
				issuance.AssetScale = f.UInt8()
			}

		case stUInt16:
			if f.FieldCode == 4 { // TransferFee (nth=4)
				issuance.TransferFee = f.UInt16()
			}

		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				issuance.Flags = f.UInt32()
			case 4: // Sequence
				issuance.Sequence = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				issuance.PreviousTxnLgrSeq = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode (nth=4)
				issuance.OwnerNode = f.UInt64()
			case 24: // MaximumAmount (nth=24)
				v := f.UInt64()
				issuance.MaximumAmount = &v
			case 25: // OutstandingAmount (nth=25)
				issuance.OutstandingAmount = f.UInt64()
			case 29: // LockedAmount (nth=29)
				v := f.UInt64()
				issuance.LockedAmount = &v
			}

		case stAccountID:
			if f.FieldCode == 4 { // Issuer (nth=4)
				if id, ok := f.AccountID(); ok {
					issuance.Issuer = id
				}
			}

		case stHash256:
			switch f.FieldCode {
			case 5: // PreviousTxnID
				issuance.PreviousTxnID = f.Hash256()
			case 34: // DomainID (nth=34)
				domainHex := hex.EncodeToString(f.Value)
				issuance.DomainID = &domainHex
			}

		case stBlob:
			if f.FieldCode == 30 { // MPTokenMetadata (nth=30)
				issuance.MPTokenMetadata = hex.EncodeToString(f.VLBytes())
			}
		}
		return nil
	})
	if err != nil {
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

	jsonObj := map[string]any{
		"LedgerEntryType":   "MPTokenIssuance",
		"Flags":             issuance.Flags,
		"Issuer":            issuerAddress,
		"Sequence":          issuance.Sequence,
		"OwnerNode":         fmt.Sprintf("%X", issuance.OwnerNode),
		"OutstandingAmount": fmt.Sprintf("%d", issuance.OutstandingAmount),
	}

	if issuance.TransferFee > 0 {
		jsonObj["TransferFee"] = issuance.TransferFee
	}

	if issuance.AssetScale > 0 {
		jsonObj["AssetScale"] = issuance.AssetScale
	}

	if issuance.MaximumAmount != nil {
		jsonObj["MaximumAmount"] = fmt.Sprintf("%d", *issuance.MaximumAmount)
	}

	if issuance.LockedAmount != nil && *issuance.LockedAmount > 0 {
		jsonObj["LockedAmount"] = fmt.Sprintf("%d", *issuance.LockedAmount)
	}

	if issuance.MPTokenMetadata != "" {
		jsonObj["MPTokenMetadata"] = strings.ToUpper(issuance.MPTokenMetadata)
	}

	if issuance.DomainID != nil && *issuance.DomainID != "" {
		jsonObj["DomainID"] = strings.ToUpper(*issuance.DomainID)
	}

	var zeroHash [32]byte
	if issuance.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(issuance.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = issuance.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode MPTokenIssuance: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ParseMPToken parses an MPToken ledger entry from binary data.
func ParseMPToken(data []byte) (*MPTokenData, error) {
	token := &MPTokenData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				token.Flags = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				token.PreviousTxnLgrSeq = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode (nth=4)
				token.OwnerNode = f.UInt64()
			case 26: // MPTAmount (nth=26)
				token.MPTAmount = f.UInt64()
			case 29: // LockedAmount (nth=29)
				v := f.UInt64()
				token.LockedAmount = &v
			}

		case stAccountID:
			if f.FieldCode == 1 { // Account (nth=1)
				if id, ok := f.AccountID(); ok {
					token.Account = id
				}
			}

		case stHash192:
			if f.FieldCode == 1 { // MPTokenIssuanceID (nth=1)
				token.MPTokenIssuanceID = f.Hash192()
			}

		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				token.PreviousTxnID = f.Hash256()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return token, nil
}

// SerializeMPToken serializes an MPToken to binary format.
func SerializeMPToken(token *MPTokenData) ([]byte, error) {
	accountAddress, err := addresscodec.EncodeAccountIDToClassicAddress(token.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType":   "MPToken",
		"Flags":             token.Flags,
		"Account":           accountAddress,
		"MPTokenIssuanceID": strings.ToUpper(hex.EncodeToString(token.MPTokenIssuanceID[:])),
		"OwnerNode":         fmt.Sprintf("%X", token.OwnerNode),
	}

	// sfMPTAmount is soeDEFAULT on ltMPTOKEN (ledger_entries.macro), so rippled
	// omits it when zero; emitting MPTAmount:0 diverges the SLE state (account_hash).
	if token.MPTAmount != 0 {
		jsonObj["MPTAmount"] = fmt.Sprintf("%d", token.MPTAmount)
	}

	if token.LockedAmount != nil && *token.LockedAmount > 0 {
		jsonObj["LockedAmount"] = fmt.Sprintf("%d", *token.LockedAmount)
	}

	var zeroHash [32]byte
	if token.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(token.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = token.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode MPToken: %w", err)
	}

	return hex.DecodeString(hexStr)
}

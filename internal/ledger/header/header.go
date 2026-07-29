package header

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LCFNoConsensusTime Ledger close flags
const LCFNoConsensusTime uint8 = 0x01

const (
	// SizeBase matches rippled's serialized ledger header format exactly.
	// Reference: rippled LedgerHeader.cpp addRaw() lines 27-42.
	SizeBase = 4 + // LedgerIndex (uint32)
		8 + // Drops (uint64)
		32 + // ParentHash ([32]byte)
		32 + // TxHash ([32]byte)
		32 + // AccountHash ([32]byte)
		4 + // ParentCloseTime (uint32, XRPL epoch seconds)
		4 + // CloseTime (uint32, XRPL epoch seconds)
		1 + // CloseTimeResolution (uint8)
		1 // CloseFlags (uint8)
	// = 118 bytes

	SizeWithHash = SizeBase + 32 // + Hash ([32]byte) = 150 bytes
)

type LedgerHeader struct {
	LedgerIndex     uint32
	ParentCloseTime time.Time
	//
	// For closed ledgers
	//

	// Closed means "tx set already determined"
	Hash        [32]byte
	TxHash      [32]byte
	AccountHash [32]byte
	ParentHash  [32]byte
	Drops       uint64

	// If validated is false, it means "not yet validated."
	// Once validated is true, it will never be set false at a later time.
	Validated bool
	Accepted  bool
	// flags indicating how this ledger close took place
	CloseFlags uint8

	// the resolution for this ledger close time (2-120 seconds)
	CloseTimeResolution uint8

	// For closed ledgers, the time the ledger
	// closed. For open ledgers, the time the ledger
	// will close if there's no transactions.
	CloseTime time.Time
}

// AddRaw serializes a ledger header to bytes matching rippled's format.
// Reference: rippled LedgerHeader.cpp addRaw() — all times are uint32 XRPL
// epoch, closeTimeResolution is uint8.
func AddRaw(header LedgerHeader, includeHash bool) []byte {
	size := SizeBase
	if includeHash {
		size = SizeWithHash
	}
	out := appendRawBody(make([]byte, 0, size), header)
	if includeHash {
		out = append(out, header.Hash[:]...)
	}
	return out
}

func appendRawBody(out []byte, h LedgerHeader) []byte {
	out = binary.BigEndian.AppendUint32(out, h.LedgerIndex)
	out = binary.BigEndian.AppendUint64(out, h.Drops)
	out = append(out, h.ParentHash[:]...)
	out = append(out, h.TxHash[:]...)
	out = append(out, h.AccountHash[:]...)
	out = binary.BigEndian.AppendUint32(out, protocol.ToRippleTime(h.ParentCloseTime))
	out = binary.BigEndian.AppendUint32(out, protocol.ToRippleTime(h.CloseTime))
	out = append(out, h.CloseTimeResolution)
	return append(out, h.CloseFlags)
}

// CalculateHash computes SHA512-half over the ledger-master prefix and the
// same 118-byte body emitted by AddRaw.
func CalculateHash(h LedgerHeader) [32]byte {
	data := make([]byte, 0, len(protocol.HashPrefixLedgerMaster().Bytes())+SizeBase)
	data = append(data, protocol.HashPrefixLedgerMaster().Bytes()...)
	data = appendRawBody(data, h)
	return sha512half.Sum(data)
}

// GetCloseAgree returns true if there was consensus on the close time
func (h *LedgerHeader) GetCloseAgree() bool {
	return (h.CloseFlags & LCFNoConsensusTime) == 0
}

// DeserializeHeader deserializes a ledger header from a byte array.
// Format matches rippled's addRaw(): uint32 times, uint8 resolution.
func DeserializeHeader(data []byte, hasHash bool) (*LedgerHeader, error) {
	minSize := SizeBase
	if hasHash {
		minSize = SizeWithHash
	}

	if len(data) < minSize {
		return nil, errors.New("data too short for ledger header")
	}

	reader := bytes.NewReader(data)
	header := &LedgerHeader{}

	// Read sequence number (uint32)
	if err := binary.Read(reader, binary.BigEndian, &header.LedgerIndex); err != nil {
		return nil, err
	}

	// Read drops (uint64)
	if err := binary.Read(reader, binary.BigEndian, &header.Drops); err != nil {
		return nil, err
	}

	// Read hashes (3 x 32 bytes)
	if _, err := reader.Read(header.ParentHash[:]); err != nil {
		return nil, err
	}
	if _, err := reader.Read(header.TxHash[:]); err != nil {
		return nil, err
	}
	if _, err := reader.Read(header.AccountHash[:]); err != nil {
		return nil, err
	}

	// Read parent close time (uint32, XRPL epoch seconds)
	var parentCloseTime uint32
	if err := binary.Read(reader, binary.BigEndian, &parentCloseTime); err != nil {
		return nil, err
	}
	header.ParentCloseTime = protocol.FromRippleTime(parentCloseTime)

	// Read close time (uint32, XRPL epoch seconds)
	var closeTime uint32
	if err := binary.Read(reader, binary.BigEndian, &closeTime); err != nil {
		return nil, err
	}
	header.CloseTime = protocol.FromRippleTime(closeTime)

	// Read close time resolution (uint8)
	var closeTimeResolution uint8
	if err := binary.Read(reader, binary.BigEndian, &closeTimeResolution); err != nil {
		return nil, err
	}
	header.CloseTimeResolution = closeTimeResolution

	// Read close flags (uint8)
	if err := binary.Read(reader, binary.BigEndian, &header.CloseFlags); err != nil {
		return nil, err
	}

	// Optionally read hash
	if hasHash {
		if _, err := reader.Read(header.Hash[:]); err != nil {
			return nil, err
		}
	}

	return header, nil
}

// DeserializePrefixedHeader deserializes a ledger header prefixed with 4 bytes
func DeserializePrefixedHeader(data []byte, hasHash bool) (*LedgerHeader, error) {
	if len(data) < 4 {
		return nil, errors.New("data too short for prefixed header")
	}
	// Skip the first 4 bytes (prefix) and deserialize the rest
	return DeserializeHeader(data[4:], hasHash)
}

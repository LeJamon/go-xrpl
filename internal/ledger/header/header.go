package header

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"

	"github.com/LeJamon/goXRPLd/protocol"
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
	CloseTimeResolution uint32

	// For closed ledgers, the time the ledger
	// closed. For open ledgers, the time the ledger
	// will close if there's no transactions.
	CloseTime time.Time
}

// AddRaw serializes a ledger header to bytes matching rippled's format exactly.
// Reference: rippled LedgerHeader.cpp addRaw() — all times are uint32 XRPL epoch,
// closeTimeResolution is uint8.
func AddRaw(header LedgerHeader, includeHash bool) ([]byte, error) {
	size := SizeBase
	if includeHash {
		size = SizeWithHash
	}
	buf := bytes.NewBuffer(make([]byte, 0, size))

	if err := binary.Write(buf, binary.BigEndian, header.LedgerIndex); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, header.Drops); err != nil {
		return nil, err
	}

	buf.Write(header.ParentHash[:])
	buf.Write(header.TxHash[:])
	buf.Write(header.AccountHash[:])

	// Times as uint32 XRPL epoch seconds (matching rippled's s.add32())
	parentCloseTime := timeToXRPLEpoch(header.ParentCloseTime)
	if err := binary.Write(buf, binary.BigEndian, parentCloseTime); err != nil {
		return nil, err
	}
	closeTime := timeToXRPLEpoch(header.CloseTime)
	if err := binary.Write(buf, binary.BigEndian, closeTime); err != nil {
		return nil, err
	}

	// CloseTimeResolution as uint8 (matching rippled's s.add8())
	if err := binary.Write(buf, binary.BigEndian, uint8(header.CloseTimeResolution)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, header.CloseFlags); err != nil {
		return nil, err
	}

	if includeHash {
		buf.Write(header.Hash[:])
	}

	return buf.Bytes(), nil
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
	header.ParentCloseTime = xrplEpochToTime(parentCloseTime)

	// Read close time (uint32, XRPL epoch seconds)
	var closeTime uint32
	if err := binary.Read(reader, binary.BigEndian, &closeTime); err != nil {
		return nil, err
	}
	header.CloseTime = xrplEpochToTime(closeTime)

	// Read close time resolution (uint8)
	var closeTimeResolution uint8
	if err := binary.Read(reader, binary.BigEndian, &closeTimeResolution); err != nil {
		return nil, err
	}
	header.CloseTimeResolution = uint32(closeTimeResolution)

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

// timeToXRPLEpoch converts a time.Time to uint32 XRPL epoch seconds.
// Returns 0 for the zero time.
func timeToXRPLEpoch(t time.Time) uint32 {
	if t.IsZero() {
		return 0
	}
	secs := t.Unix() - protocol.RippleEpochUnix
	if secs < 0 {
		return 0
	}
	return uint32(secs)
}

// xrplEpochToTime converts uint32 XRPL epoch seconds to time.Time.
// Returns the zero time for 0.
func xrplEpochToTime(epoch uint32) time.Time {
	if epoch == 0 {
		return time.Time{}
	}
	return time.Unix(int64(epoch)+protocol.RippleEpochUnix, 0)
}

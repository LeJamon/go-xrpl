package header

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"
)

// LCFNoConsensusTime Ledger close flags
const LCFNoConsensusTime uint8 = 0x01
const (
	// SizeBase Calculate based on actual serialized format, not Go struct size
	SizeBase = 4 + // LedgerIndex (uint32)
		8 + // Drops (uint64)
		32 + // ParentHash ([32]byte)
		32 + // TxHash ([32]byte)
		32 + // AccountHash ([32]byte)
		8 + // ParentCloseTime (int64 as timestamp)
		8 + // CloseTime (int64 as timestamp)
		4 + // CloseTimeResolution (uint32)
		1 // CloseFlags (uint8)
	// = 129 bytes

	SizeWithHash = SizeBase + 32 // + Hash ([32]byte) = 161 bytes
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

func AddRaw(header LedgerHeader, includeHash bool) ([]byte, error) {
	size := SizeBase
	if includeHash {
		size = SizeWithHash
	}
	buf := bytes.NewBuffer(make([]byte, 0, size)) // pre-allocate capacity

	err := binary.Write(buf, binary.BigEndian, header.LedgerIndex)
	if err != nil {
		return nil, err
	}
	err = binary.Write(buf, binary.BigEndian, header.Drops)
	if err != nil {
		return nil, err
	}

	buf.Write(header.ParentHash[:])
	buf.Write(header.TxHash[:])
	buf.Write(header.AccountHash[:])

	err = binary.Write(buf, binary.BigEndian, header.ParentCloseTime.Unix())
	if err != nil {
		return nil, err
	}
	err = binary.Write(buf, binary.BigEndian, header.CloseTime.Unix())
	if err != nil {
		return nil, err
	}
	err = binary.Write(buf, binary.BigEndian, header.CloseTimeResolution)
	if err != nil {
		return nil, err
	}
	err = binary.Write(buf, binary.BigEndian, header.CloseFlags)
	if err != nil {
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

// DeserializeHeader deserializes a ledger header from a byte array
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

	// Read sequence number (32-bit)
	var seq uint32
	if err := binary.Read(reader, binary.BigEndian, &seq); err != nil {
		return nil, err
	}
	header.LedgerIndex = seq

	// Read drops (64-bit)
	if err := binary.Read(reader, binary.BigEndian, &header.Drops); err != nil {
		return nil, err
	}

	// Read parent hash (256-bit)
	if _, err := reader.Read(header.ParentHash[:]); err != nil {
		return nil, err
	}

	// Read tx hash (256-bit)
	if _, err := reader.Read(header.TxHash[:]); err != nil {
		return nil, err
	}

	// Read account hash (256-bit)
	if _, err := reader.Read(header.AccountHash[:]); err != nil {
		return nil, err
	}

	// Read parent close time (64-bit timestamp)
	var parentCloseTime int64
	if err := binary.Read(reader, binary.BigEndian, &parentCloseTime); err != nil {
		return nil, err
	}
	header.ParentCloseTime = time.Unix(parentCloseTime, 0)

	// Read close time (64-bit timestamp)
	var closeTime int64
	if err := binary.Read(reader, binary.BigEndian, &closeTime); err != nil {
		return nil, err
	}
	header.CloseTime = time.Unix(closeTime, 0)

	// Read close time resolution (32-bit)
	var closeTimeResolution uint32
	if err := binary.Read(reader, binary.BigEndian, &closeTimeResolution); err != nil {
		return nil, err
	}
	header.CloseTimeResolution = closeTimeResolution

	// Read close flags (8-bit)
	var closeFlags uint8
	if err := binary.Read(reader, binary.BigEndian, &closeFlags); err != nil {
		return nil, err
	}
	header.CloseFlags = closeFlags

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

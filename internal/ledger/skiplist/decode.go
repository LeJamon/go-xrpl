package skiplist

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// LedgerHashesFields holds the non-vector fields needed to validate and rewrite
// an internal LedgerHashes skip-list entry.
type LedgerHashesFields struct {
	Flags               uint32
	FirstLedgerSequence uint32
	LastLedgerSequence  uint32
	Sponsor             string
	hasFirst            bool
	hasLast             bool
	hasSponsor          bool
}

// HasFirstLedgerSequence reports whether FirstLedgerSequence was serialized.
func (f *LedgerHashesFields) HasFirstLedgerSequence() bool {
	return f != nil && f.hasFirst
}

// SetFirstLedgerSequence sets FirstLedgerSequence for the next rewrite.
func (f *LedgerHashesFields) SetFirstLedgerSequence(value uint32) {
	f.FirstLedgerSequence = value
	f.hasFirst = true
}

func decodeLedgerHashes(data []byte) (*LedgerHashesFields, [][32]byte, error) {
	fields := &LedgerHashesFields{}
	reader := ledgerHashesReader{data: data}
	seen := make(map[[2]int]struct{})
	var hashes [][32]byte
	var sawLedgerEntryType, sawFlags, sawHashes bool

	for reader.hasMore() {
		typeCode, fieldCode, err := reader.readFieldHeader()
		if err != nil {
			return nil, nil, err
		}
		fieldID := [2]int{typeCode, fieldCode}
		if _, exists := seen[fieldID]; exists {
			return nil, nil, fmt.Errorf("ledgerfields: LedgerHashes: duplicate field type=%d field=%d", typeCode, fieldCode)
		}
		seen[fieldID] = struct{}{}

		switch typeCode {
		case 1:
			value, err := reader.readUint16()
			if err != nil {
				return nil, nil, err
			}
			if fieldCode != 1 {
				return nil, nil, unknownLedgerHashesField(typeCode, fieldCode)
			}
			if value != 104 {
				return nil, nil, fmt.Errorf("ledgerfields: LedgerHashes: LedgerEntryType is %d, want 104", value)
			}
			sawLedgerEntryType = true
		case 2:
			value, err := reader.readUint32()
			if err != nil {
				return nil, nil, err
			}
			switch fieldCode {
			case 2:
				fields.Flags = value
				sawFlags = true
			case 26:
				fields.FirstLedgerSequence = value
				fields.hasFirst = true
			case 27:
				fields.LastLedgerSequence = value
				fields.hasLast = true
			default:
				return nil, nil, unknownLedgerHashesField(typeCode, fieldCode)
			}
		case 8:
			value, err := reader.readAccountID()
			if err != nil {
				return nil, nil, err
			}
			if fieldCode != 27 {
				return nil, nil, unknownLedgerHashesField(typeCode, fieldCode)
			}
			fields.Sponsor = value
			fields.hasSponsor = true
		case 19:
			value, err := reader.readVector256()
			if err != nil {
				return nil, nil, err
			}
			if fieldCode != 2 {
				return nil, nil, unknownLedgerHashesField(typeCode, fieldCode)
			}
			hashes = value
			sawHashes = true
		default:
			return nil, nil, unknownLedgerHashesField(typeCode, fieldCode)
		}
	}

	if !sawLedgerEntryType {
		return nil, nil, errors.New("ledgerfields: LedgerHashes: missing LedgerEntryType")
	}
	if !sawFlags {
		return nil, nil, errors.New("ledgerfields: LedgerHashes: required field Flags is missing")
	}
	if !sawHashes {
		return nil, nil, errors.New("ledgerfields: LedgerHashes: required field Hashes is missing")
	}
	return fields, hashes, nil
}

func unknownLedgerHashesField(typeCode, fieldCode int) error {
	return &ledgerfields.ErrUnknownField{
		EntryType: "LedgerHashes",
		TypeCode:  typeCode,
		FieldCode: fieldCode,
	}
}

type ledgerHashesReader struct {
	data []byte
	pos  int
}

func (r *ledgerHashesReader) hasMore() bool {
	return r.pos < len(r.data)
}

func (r *ledgerHashesReader) readFieldHeader() (typeCode, fieldCode int, err error) {
	if r.pos >= len(r.data) {
		return 0, 0, errors.New("ledgerfields: out of bounds reading field header")
	}
	header := r.data[r.pos]
	r.pos++
	typeCode = int(header >> 4)
	fieldCode = int(header & 0x0f)
	if typeCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended typeCode")
		}
		typeCode = int(r.data[r.pos])
		r.pos++
		if typeCode < 16 {
			return 0, 0, serdes.ErrInvalidTypecode
		}
	}
	if fieldCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended fieldCode")
		}
		fieldCode = int(r.data[r.pos])
		r.pos++
		if fieldCode < 16 {
			return 0, 0, serdes.ErrInvalidFieldcode
		}
	}
	return typeCode, fieldCode, nil
}

func (r *ledgerHashesReader) readUint16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt16")
	}
	value := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return value, nil
}

func (r *ledgerHashesReader) readUint32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt32")
	}
	value := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return value, nil
}

func (r *ledgerHashesReader) readUint8() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt8")
	}
	value := r.data[r.pos]
	r.pos++
	return value, nil
}

func (r *ledgerHashesReader) readVariableLength() (int, error) {
	b1, err := r.readUint8()
	if err != nil {
		return 0, err
	}
	switch {
	case b1 <= 192:
		return int(b1), nil
	case b1 <= 240:
		b2, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		return 193 + (int(b1)-193)*256 + int(b2), nil
	case b1 <= 254:
		b2, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		b3, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		return 12481 + (int(b1)-241)*65536 + int(b2)*256 + int(b3), nil
	default:
		return 0, errors.New("ledgerfields: invalid VL prefix")
	}
}

func (r *ledgerHashesReader) readAccountID() (string, error) {
	length, err := r.readVariableLength()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	if r.pos+length > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading AccountID")
	}
	value, err := addresscodec.Encode(r.data[r.pos:r.pos+length], []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	r.pos += length
	return value, err
}

func (r *ledgerHashesReader) readVector256() ([][32]byte, error) {
	length, err := r.readVariableLength()
	if err != nil {
		return nil, err
	}
	if length%32 != 0 {
		return nil, errors.New("ledgerfields: Vector256 length not a multiple of 32")
	}
	if r.pos+length > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Vector256")
	}
	hashes := make([][32]byte, length/32)
	for i := range hashes {
		copy(hashes[i][:], r.data[r.pos+i*32:r.pos+(i+1)*32])
	}
	r.pos += length
	return hashes, nil
}

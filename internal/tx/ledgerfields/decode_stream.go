package ledgerfields

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types"
)

// streamReader is a zero-allocation cursor over an XRPL binary blob. It is
// the minimum surface needed by the per-entry-type streaming decoders: read
// the field header (typeCode, fieldCode), peek raw bytes for fixed-width
// fields, and decode VL prefixes. Unlike serdes.BinaryParser this never
// copies bytes — fixed-width readers consume sub-slices of the original blob
// in-place and translate to the canonical decoded form (uppercase hex string
// for Hash*, decimal string for XRP drops, base58 for AccountID).
type streamReader struct {
	data []byte
	pos  int
}

func newStreamReader(data []byte) *streamReader { return &streamReader{data: data} }

func (r *streamReader) hasMore() bool { return r.pos < len(r.data) }

// readFieldHeader matches the format documented at
// codec/binarycodec/serdes/binary_parser.go:readFieldHeader.
func (r *streamReader) readFieldHeader() (typeCode, fieldCode int, err error) {
	if r.pos >= len(r.data) {
		return 0, 0, errors.New("ledgerfields: out of bounds reading field header")
	}
	b := r.data[r.pos]
	r.pos++
	typeCode = int(b >> 4)
	fieldCode = int(b & 0x0F)
	if typeCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended typeCode")
		}
		typeCode = int(r.data[r.pos])
		r.pos++
	}
	if fieldCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended fieldCode")
		}
		fieldCode = int(r.data[r.pos])
		r.pos++
	}
	return typeCode, fieldCode, nil
}

func (r *streamReader) readUint16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt16")
	}
	v := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *streamReader) readUint32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt32")
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *streamReader) readUint8() (byte, error) {
	if r.pos+1 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt8")
	}
	v := r.data[r.pos]
	r.pos++
	return v, nil
}

// readUint64Hex reads 8 bytes and returns the uppercase hex string — the
// canonical decoded form used by binarycodec.Decode for UInt64 fields.
func (r *streamReader) readUint64Hex() (string, error) {
	if r.pos+8 > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading UInt64")
	}
	s := upperHex(r.data[r.pos : r.pos+8])
	r.pos += 8
	return s, nil
}

// readHash returns an uppercase hex string of n bytes.
func (r *streamReader) readHash(n int) (string, error) {
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading Hash")
	}
	s := upperHex(r.data[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// readVariableLength decodes the 1-3 byte length prefix used for AccountID
// and Blob.
func (r *streamReader) readVariableLength() (int, error) {
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

// readAccountID reads a VL-prefixed 20-byte payload and base58-encodes it to
// the classic XRPL address — matching the value binarycodec.Decode would
// have produced.
func (r *streamReader) readAccountID() (string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading AccountID")
	}
	s, err := addresscodec.Encode(r.data[r.pos:r.pos+n], []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	r.pos += n
	return s, err
}

// readBlobHex reads a VL-prefixed blob and returns its uppercase hex
// representation.
func (r *streamReader) readBlobHex() (string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading Blob")
	}
	s := upperHex(r.data[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// readAmount returns the decimal-drops string for an XRP amount, or
// errUnsupportedAmount for IOU/MPT. Callers that need IOU/MPT support should
// call readAmountAny instead.
func (r *streamReader) readAmount() (any, error) {
	if r.pos >= len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Amount")
	}
	first := r.data[r.pos]
	if first&0x80 == 0 {
		if r.pos+8 > len(r.data) {
			return nil, errors.New("ledgerfields: out of bounds reading XRP Amount")
		}
		raw := binary.BigEndian.Uint64(r.data[r.pos:])
		r.pos += 8
		positive := raw&0x4000000000000000 != 0
		val := raw & 0x3FFFFFFFFFFFFFFF
		if !positive {
			return "-" + strconv.FormatUint(val, 10), nil
		}
		return strconv.FormatUint(val, 10), nil
	}
	return nil, errUnsupportedAmount
}

// readAmountAny decodes any Amount variant (XRP, IOU, MPT). XRP stays inline;
// IOU/MPT delegate to the binarycodec types.Amount decoder. Used by entry
// types whose Amount fields can legitimately be non-XRP (e.g. Offer.TakerPays).
func (r *streamReader) readAmountAny() (any, error) {
	if r.pos >= len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Amount")
	}
	first := r.data[r.pos]
	switch {
	case first&0x80 == 0:
		return r.readAmount()
	case first&0x20 != 0:
		return r.readAmountViaCodec(33) // MPT
	default:
		return r.readAmountViaCodec(48) // IOU
	}
}

func (r *streamReader) readAmountViaCodec(n int) (any, error) {
	if r.pos+n > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading IOU/MPT Amount")
	}
	p := serdes.NewBinaryParser(r.data[r.pos:r.pos+n], definitions.Get())
	amt := &types.Amount{}
	v, err := amt.ToJSON(p)
	r.pos += n
	return v, err
}

var errUnsupportedAmount = errors.New("ledgerfields: non-XRP amount in streaming decode")

// upperHex is hex.EncodeToString uppercased without the intermediate
// allocation produced by strings.ToUpper.
func upperHex(b []byte) string {
	const hextable = "0123456789ABCDEF"
	buf := make([]byte, len(b)*2)
	for i, v := range b {
		buf[i*2] = hextable[v>>4]
		buf[i*2+1] = hextable[v&0x0F]
	}
	return string(buf)
}

// skipField advances past the value of a field of the given type/code without
// decoding it — used for unknown or sMD_Never fields. Falls back via err if
// the type is one we don't know how to size.
func (r *streamReader) skipField(typeCode int) error {
	switch typeCode {
	case 1: // UInt16
		r.pos += 2
	case 2: // UInt32
		r.pos += 4
	case 3: // UInt64
		r.pos += 8
	case 4: // Hash128
		r.pos += 16
	case 5: // Hash256
		r.pos += 32
	case 6: // Amount
		if r.pos >= len(r.data) {
			return errors.New("ledgerfields: out of bounds skipping Amount")
		}
		if r.data[r.pos]&0x80 == 0 {
			r.pos += 8 // XRP
		} else if r.data[r.pos]&0x20 != 0 {
			r.pos += 33 // MPT
		} else {
			r.pos += 48 // IOU
		}
	case 7, 8, 19: // Blob, AccountID, Vector256 (VL-prefixed)
		n, err := r.readVariableLength()
		if err != nil {
			return err
		}
		r.pos += n
	case 16: // UInt8
		r.pos++
	case 17: // Hash160
		r.pos += 20
	default:
		return errors.New("ledgerfields: cannot skip unknown type code")
	}
	if r.pos > len(r.data) {
		return errors.New("ledgerfields: skip overran buffer")
	}
	return nil
}

// Compile-time check that hex.EncodeToString remains the reference impl —
// drop with -B usage if upperHex ever diverges.
var _ = hex.EncodeToString

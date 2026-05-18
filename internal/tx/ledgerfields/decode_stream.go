package ledgerfields

import (
	"encoding/binary"
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
// IOU/MPT delegate to the binarycodec types.Amount decoder. Order matches
// types/amount.go ToJSON: IOU first (bit 0x80), then MPT (bit 0x20), else XRP.
// Used by entry types whose Amount fields can legitimately be non-XRP (e.g.
// Offer.TakerPays).
func (r *streamReader) readAmountAny() (any, error) {
	if r.pos >= len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Amount")
	}
	first := r.data[r.pos]
	switch {
	case first&0x80 != 0:
		return r.readAmountViaCodec(48) // IOU
	case first&0x20 != 0:
		return r.readAmountViaCodec(33) // MPT
	default:
		return r.readAmount() // XRP
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

// readVector256 reads a VL-prefixed array of 32-byte hashes and returns
// the canonical decoded form ([]string of uppercase hex hashes) that
// binarycodec.Decode would produce. Used by ledger entries that carry
// Vector256 with sMD_default (LedgerHashes.Hashes, Amendments.Amendments).
func (r *streamReader) readVector256() ([]string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return nil, err
	}
	if n%32 != 0 {
		return nil, errors.New("ledgerfields: Vector256 length not a multiple of 32")
	}
	if r.pos+n > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Vector256")
	}
	out := make([]string, 0, n/32)
	end := r.pos + n
	for r.pos < end {
		out = append(out, upperHex(r.data[r.pos:r.pos+32]))
		r.pos += 32
	}
	return out, nil
}

// readSTObject and readSTArray drive a sub-parser over the underlying byte
// slice via binarycodec.types and resync r.pos by how many bytes the
// sub-parser consumed. The compound types' decoders contain rippled-faithful
// recursive logic (nested STObjects, ObjectEndMarker handling, enum string
// substitution) that is not worth re-implementing inline; the cost is one
// BinaryParser allocation per compound field, paid only on ledger entries
// that carry one (AMM, SignerList, NFTokenPage, Vault, …).
func (r *streamReader) readSTObject() (map[string]any, error) {
	v, err := r.decodeViaCodec(&types.STObject{}, -1)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("ledgerfields: STObject decode returned wrong type")
	}
	return m, nil
}

func (r *streamReader) readSTArray() ([]any, error) {
	v, err := r.decodeViaCodec(&types.STArray{}, -1)
	if err != nil {
		return nil, err
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("ledgerfields: STArray decode returned wrong type")
	}
	return a, nil
}

// readIssue reads an Issue (XRP / IOU / MPT shape, 20 / 40 / 44 bytes).
func (r *streamReader) readIssue() (any, error) {
	return r.decodeViaCodec(&types.Issue{}, -1)
}

// readXChainBridge reads a fixed-size 80-byte XChainBridge.
func (r *streamReader) readXChainBridge() (any, error) {
	const xChainBridgeLength = 80
	return r.decodeViaCodec(&types.XChainBridge{}, xChainBridgeLength)
}

// readNumber reads a 12-byte Number.
func (r *streamReader) readNumber() (any, error) {
	return r.decodeViaCodec(&types.Number{}, -1)
}

// decodeViaCodec builds a sub-parser positioned at r.pos, hands it to the
// supplied SerializedType to decode, then advances r.pos by the number of
// bytes the sub-parser consumed. vlen < 0 means "no explicit length"; the
// decoder figures out where the value ends from its own grammar (Issue uses
// internal markers, STObject/STArray scan for end markers).
func (r *streamReader) decodeViaCodec(st types.SerializedType, vlen int) (any, error) {
	sub := serdes.NewBinaryParser(r.data[r.pos:], definitions.Get())
	startRem := sub.Remaining()
	var v any
	var err error
	if vlen < 0 {
		v, err = st.ToJSON(sub)
	} else {
		v, err = st.ToJSON(sub, vlen)
	}
	if err != nil {
		return nil, err
	}
	consumed := startRem - sub.Remaining()
	r.pos += consumed
	return v, nil
}

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

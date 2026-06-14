package state

import "encoding/binary"

// Typed accessors for a Field decoded by WalkFields. WalkFields has already
// delimited Value by the field's serialized type, so each accessor reads a
// correctly-sized slice; the length guards defend only against a Field built by
// hand. The variable-length accessors strip the XRPL length prefix that Value
// retains for Blob/AccountID/Vector256 fields.

// UInt8 returns a UInt8 field value.
func (f Field) UInt8() uint8 {
	if len(f.Value) < 1 {
		return 0
	}
	return f.Value[0]
}

// UInt16 returns a UInt16 field value.
func (f Field) UInt16() uint16 {
	if len(f.Value) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(f.Value)
}

// UInt32 returns a UInt32 field value.
func (f Field) UInt32() uint32 {
	if len(f.Value) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(f.Value)
}

// UInt64 returns a UInt64 field value.
func (f Field) UInt64() uint64 {
	if len(f.Value) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(f.Value)
}

// Hash128 returns a 16-byte Hash128 field value.
func (f Field) Hash128() [16]byte {
	var h [16]byte
	copy(h[:], f.Value)
	return h
}

// Hash160 returns a 20-byte Hash160 field value.
func (f Field) Hash160() [20]byte {
	var h [20]byte
	copy(h[:], f.Value)
	return h
}

// Hash192 returns a 24-byte Hash192 field value (e.g. MPTokenIssuanceID).
func (f Field) Hash192() [24]byte {
	var h [24]byte
	copy(h[:], f.Value)
	return h
}

// Hash256 returns a 32-byte Hash256 field value.
func (f Field) Hash256() [32]byte {
	var h [32]byte
	copy(h[:], f.Value)
	return h
}

// VLBytes returns the payload of a variable-length field (Blob, AccountID,
// Vector256) with its XRPL length prefix removed. It returns nil when the
// prefix is malformed, which cannot happen for a Field produced by WalkFields.
func (f Field) VLBytes() []byte {
	_, prefixLen, err := readVLLength(f.Value, 0)
	if err != nil || prefixLen > len(f.Value) {
		return nil
	}
	return f.Value[prefixLen:]
}

// AccountID returns the 20-byte account of an AccountID field. ok is false when
// the payload is not exactly 20 bytes.
func (f Field) AccountID() (id [20]byte, ok bool) {
	p := f.VLBytes()
	if len(p) != 20 {
		return id, false
	}
	copy(id[:], p)
	return id, true
}

// Vector256 splits a Vector256 payload into its constituent 32-byte hashes.
func (f Field) Vector256() [][32]byte {
	p := f.VLBytes()
	n := len(p) / 32
	out := make([][32]byte, n)
	for i := range out {
		copy(out[i][:], p[i*32:])
	}
	return out
}

// xrpDrops decodes the drops carried by an 8-byte native Amount value, masking
// off the not-XRP (bit 63) and sign (bit 62) flags.
func xrpDrops(v []byte) uint64 {
	if len(v) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v) & 0x3FFFFFFFFFFFFFFF
}

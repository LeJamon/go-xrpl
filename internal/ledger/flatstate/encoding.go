package flatstate

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

var (
	descriptorMagic = [4]byte{'F', 'S', 'D', '1'}
	markerMagic     = [4]byte{'F', 'S', 'A', '1'}
	outboxMagic     = [4]byte{'F', 'S', 'O', '1'}
)

type advanceMarker struct {
	AttemptID   [16]byte
	WriterEpoch uint64
	Algorithm   uint32
	Source      Descriptor
	Target      Descriptor
	Expected    Hash
	Intent      string
}

// AdvanceMarker is the read-only recovery identity exposed by Marker.
type AdvanceMarker struct {
	AttemptID   [16]byte
	WriterEpoch uint64
	Algorithm   uint32
	Source      Descriptor
	Target      Descriptor
	Expected    Hash
	Intent      string
}

func (m advanceMarker) public() AdvanceMarker {
	return AdvanceMarker{
		AttemptID:   m.AttemptID,
		WriterEpoch: m.WriterEpoch,
		Algorithm:   m.Algorithm,
		Source:      m.Source,
		Target:      m.Target,
		Expected:    m.Expected,
		Intent:      m.Intent,
	}
}

func newAdvanceMarker(source *Descriptor, target Descriptor, intent string, epoch uint64) advanceMarker {
	marker := advanceMarker{
		WriterEpoch: epoch,
		Algorithm:   changeAlgorithm,
		Target:      target,
		Expected:    descriptorDigest(target),
		Intent:      intent,
	}
	if source != nil {
		marker.Source = *source
	}
	if _, err := randRead(marker.AttemptID[:]); err != nil {
		// Attempt IDs are recovery metadata, not semantic identity.  A
		// deterministic fallback keeps the marker writable even when the
		// operating system entropy source is temporarily unavailable.
		copy(marker.AttemptID[:], marker.Expected[:16])
		binary.BigEndian.PutUint64(marker.AttemptID[8:], epoch)
	}
	return marker
}

func descriptorDigest(descriptor Descriptor) Hash {
	return Hash(sha256.Sum256(encodeDescriptor(descriptor)))
}

func encodeDescriptor(descriptor Descriptor) []byte {
	result := make([]byte, descriptorEncodedSize)
	offset := 0
	copy(result[offset:], descriptorMagic[:])
	offset += 4
	binary.BigEndian.PutUint32(result[offset:], descriptorVersion)
	offset += 4
	binary.BigEndian.PutUint32(result[offset:], descriptor.Schema)
	offset += 4
	binary.BigEndian.PutUint64(result[offset:], descriptor.Epoch)
	offset += 8
	copy(result[offset:], descriptor.LedgerHash[:])
	offset += 32
	copy(result[offset:], descriptor.ParentHash[:])
	offset += 32
	binary.BigEndian.PutUint32(result[offset:], descriptor.Sequence)
	offset += 4
	copy(result[offset:], descriptor.StateRoot[:])
	offset += 32
	copy(result[offset:], descriptor.TxRoot[:])
	return result
}

func decodeDescriptor(value []byte) (Descriptor, error) {
	if len(value) != descriptorEncodedSize || !bytes.Equal(value[:4], descriptorMagic[:]) {
		return Descriptor{}, errors.New("invalid descriptor header or length")
	}
	offset := 4
	if binary.BigEndian.Uint32(value[offset:]) != descriptorVersion {
		return Descriptor{}, errors.New("unsupported descriptor version")
	}
	offset += 4
	descriptor := Descriptor{Schema: binary.BigEndian.Uint32(value[offset:])}
	offset += 4
	descriptor.Epoch = binary.BigEndian.Uint64(value[offset:])
	offset += 8
	copy(descriptor.LedgerHash[:], value[offset:offset+32])
	offset += 32
	copy(descriptor.ParentHash[:], value[offset:offset+32])
	offset += 32
	descriptor.Sequence = binary.BigEndian.Uint32(value[offset:])
	offset += 4
	copy(descriptor.StateRoot[:], value[offset:offset+32])
	offset += 32
	copy(descriptor.TxRoot[:], value[offset:offset+32])
	return descriptor, nil
}

func encodeAdvanceMarker(marker advanceMarker) []byte {
	intent := []byte(marker.Intent)
	result := make([]byte, 0, 4+4+16+8+4+len(encodeDescriptor(marker.Source))+len(encodeDescriptor(marker.Target))+32+4+len(intent)+32)
	result = append(result, markerMagic[:]...)
	var fixed [8]byte
	binary.BigEndian.PutUint32(fixed[:4], markerVersion)
	result = append(result, fixed[:4]...)
	result = append(result, marker.AttemptID[:]...)
	binary.BigEndian.PutUint64(fixed[:], marker.WriterEpoch)
	result = append(result, fixed[:]...)
	binary.BigEndian.PutUint32(fixed[:4], marker.Algorithm)
	result = append(result, fixed[:4]...)
	result = append(result, encodeDescriptor(marker.Source)...)
	result = append(result, encodeDescriptor(marker.Target)...)
	result = append(result, marker.Expected[:]...)
	binary.BigEndian.PutUint32(fixed[:4], uint32(len(intent)))
	result = append(result, fixed[:4]...)
	result = append(result, intent...)
	checksum := sha256.Sum256(result)
	result = append(result, checksum[:]...)
	return result
}

func decodeAdvanceMarker(value []byte) (advanceMarker, error) {
	if len(value) < 4+4+16+8+4+2*(4+4+8+32+32+4+32+32)+32+4+32 || !bytes.Equal(value[:4], markerMagic[:]) {
		return advanceMarker{}, errors.New("invalid marker header or length")
	}
	if len(value) > maxMarkerBytes {
		return advanceMarker{}, errors.New("marker exceeds size limit")
	}
	checksumOffset := len(value) - 32
	expectedChecksum := sha256.Sum256(value[:checksumOffset])
	if !bytes.Equal(expectedChecksum[:], value[checksumOffset:]) {
		return advanceMarker{}, errors.New("marker checksum mismatch")
	}
	offset := 4
	if binary.BigEndian.Uint32(value[offset:]) != markerVersion {
		return advanceMarker{}, errors.New("unsupported marker version")
	}
	offset += 4
	var marker advanceMarker
	copy(marker.AttemptID[:], value[offset:offset+16])
	offset += 16
	marker.WriterEpoch = binary.BigEndian.Uint64(value[offset:])
	offset += 8
	marker.Algorithm = binary.BigEndian.Uint32(value[offset:])
	offset += 4
	if marker.Algorithm != changeAlgorithm {
		return advanceMarker{}, errors.New("unsupported change derivation algorithm")
	}
	var err error
	marker.Source, err = decodeDescriptor(value[offset : offset+descriptorEncodedSize])
	if err != nil {
		return advanceMarker{}, fmt.Errorf("source descriptor: %w", err)
	}
	offset += descriptorEncodedSize
	marker.Target, err = decodeDescriptor(value[offset : offset+descriptorEncodedSize])
	if err != nil {
		return advanceMarker{}, fmt.Errorf("target descriptor: %w", err)
	}
	offset += descriptorEncodedSize
	copy(marker.Expected[:], value[offset:offset+32])
	offset += 32
	intentLength := binary.BigEndian.Uint32(value[offset:])
	offset += 4
	if intentLength > maxIntentBytes || uint64(intentLength) > uint64(len(value)-offset-32) {
		return advanceMarker{}, errors.New("invalid marker intent length")
	}
	marker.Intent = string(value[offset : offset+int(intentLength)])
	offset += int(intentLength)
	if offset != checksumOffset {
		return advanceMarker{}, errors.New("marker trailing bytes")
	}
	if marker.Expected != descriptorDigest(marker.Target) {
		return advanceMarker{}, errors.New("marker target digest mismatch")
	}
	return marker, nil
}

const descriptorEncodedSize = 4 + 4 + 4 + 8 + 32 + 32 + 4 + 32 + 32

func encodeOutbox(record OutboxRecord) ([]byte, error) {
	if len(record.Intent) > maxIntentBytes {
		return nil, fmt.Errorf("%w: publication intent is too large", ErrLimit)
	}
	if record.CreatedAt.IsZero() {
		return nil, errors.New("flatstate: outbox timestamp is required")
	}
	intent := []byte(record.Intent)
	result := make([]byte, 0, 4+4+32+descriptorEncodedSize+8+4+len(intent)+32)
	result = append(result, outboxMagic[:]...)
	var fixed [8]byte
	binary.BigEndian.PutUint32(fixed[:4], outboxVersion)
	result = append(result, fixed[:4]...)
	result = append(result, record.CommitID[:]...)
	result = append(result, encodeDescriptor(record.Descriptor)...)
	binary.BigEndian.PutUint64(fixed[:], uint64(record.CreatedAt.UTC().UnixNano()))
	result = append(result, fixed[:]...)
	binary.BigEndian.PutUint32(fixed[:4], uint32(len(intent)))
	result = append(result, fixed[:4]...)
	result = append(result, intent...)
	checksum := sha256.Sum256(result)
	result = append(result, checksum[:]...)
	if len(result) > maxOutboxRecordBytes {
		return nil, fmt.Errorf("%w: outbox record is too large", ErrLimit)
	}
	return result, nil
}

func encodeOutboxUnchecked(record OutboxRecord) []byte {
	encoded, _ := encodeOutbox(record)
	return encoded
}

func decodeOutbox(value []byte) (OutboxRecord, error) {
	minimum := 4 + 4 + 32 + descriptorEncodedSize + 8 + 4 + 32
	if len(value) < minimum || len(value) > maxOutboxRecordBytes || !bytes.Equal(value[:4], outboxMagic[:]) {
		return OutboxRecord{}, errors.New("invalid outbox header or length")
	}
	checksumOffset := len(value) - 32
	checksum := sha256.Sum256(value[:checksumOffset])
	if !bytes.Equal(checksum[:], value[checksumOffset:]) {
		return OutboxRecord{}, errors.New("outbox checksum mismatch")
	}
	offset := 4
	if binary.BigEndian.Uint32(value[offset:]) != outboxVersion {
		return OutboxRecord{}, errors.New("unsupported outbox version")
	}
	offset += 4
	var record OutboxRecord
	copy(record.CommitID[:], value[offset:offset+32])
	offset += 32
	var err error
	record.Descriptor, err = decodeDescriptor(value[offset : offset+descriptorEncodedSize])
	if err != nil {
		return OutboxRecord{}, fmt.Errorf("descriptor: %w", err)
	}
	offset += descriptorEncodedSize
	nanos := int64(binary.BigEndian.Uint64(value[offset:]))
	offset += 8
	intentLength := binary.BigEndian.Uint32(value[offset:])
	offset += 4
	if intentLength > maxIntentBytes || uint64(intentLength) > uint64(checksumOffset-offset) {
		return OutboxRecord{}, errors.New("invalid outbox intent length")
	}
	record.Intent = string(value[offset : offset+int(intentLength)])
	offset += int(intentLength)
	if offset != checksumOffset {
		return OutboxRecord{}, errors.New("outbox trailing bytes")
	}
	record.CreatedAt = time.Unix(0, nanos).UTC()
	if SemanticCommitID(record.Descriptor.LedgerHash, record.Intent) != record.CommitID {
		return OutboxRecord{}, errors.New("outbox commit ID mismatch")
	}
	return record, nil
}

func randRead(dst []byte) (int, error) {
	return rand.Read(dst)
}

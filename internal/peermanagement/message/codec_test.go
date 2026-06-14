package message

import (
	"bytes"
	"errors"
	"testing"
)

func TestHeaderEncodeDecodeUncompressed(t *testing.T) {
	tests := []struct {
		name        string
		payloadSize uint32
		msgType     MessageType
	}{
		{"ping", 10, TypePing},
		{"transaction", 1000, TypeTransaction},
		{"validation", 500, TypeValidation},
		{"max_size", MaxPayloadSize, TypeLedgerData},
		{"zero_size", 0, TypeEndpoints},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, HeaderSizeUncompressed)
			err := EncodeHeader(buf, tt.payloadSize, tt.msgType, AlgorithmNone, 0)
			if err != nil {
				t.Fatalf("EncodeHeader failed: %v", err)
			}

			header, err := DecodeHeader(buf)
			if err != nil {
				t.Fatalf("DecodeHeader failed: %v", err)
			}

			if header.PayloadSize != tt.payloadSize {
				t.Errorf("PayloadSize = %d, want %d", header.PayloadSize, tt.payloadSize)
			}
			if header.MessageType != tt.msgType {
				t.Errorf("MessageType = %d, want %d", header.MessageType, tt.msgType)
			}
			if header.Compressed {
				t.Error("Compressed = true, want false")
			}
		})
	}
}

func TestHeaderEncodeDecodeCompressed(t *testing.T) {
	tests := []struct {
		name             string
		payloadSize      uint32
		uncompressedSize uint32
		msgType          MessageType
	}{
		{"small", 50, 100, TypeTransaction},
		{"medium", 5000, 10000, TypeLedgerData},
		{"large", 100000, 500000, TypeManifests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, HeaderSizeCompressed)
			err := EncodeHeader(buf, tt.payloadSize, tt.msgType, AlgorithmLZ4, tt.uncompressedSize)
			if err != nil {
				t.Fatalf("EncodeHeader failed: %v", err)
			}

			header, err := DecodeHeader(buf)
			if err != nil {
				t.Fatalf("DecodeHeader failed: %v", err)
			}

			if header.PayloadSize != tt.payloadSize {
				t.Errorf("PayloadSize = %d, want %d", header.PayloadSize, tt.payloadSize)
			}
			if header.MessageType != tt.msgType {
				t.Errorf("MessageType = %d, want %d", header.MessageType, tt.msgType)
			}
			if !header.Compressed {
				t.Error("Compressed = false, want true")
			}
			if header.Algorithm != AlgorithmLZ4 {
				t.Errorf("Algorithm = %d, want %d", header.Algorithm, AlgorithmLZ4)
			}
			if header.UncompressedSize != tt.uncompressedSize {
				t.Errorf("UncompressedSize = %d, want %d", header.UncompressedSize, tt.uncompressedSize)
			}
		})
	}
}

func TestHeaderTooLarge(t *testing.T) {
	buf := make([]byte, HeaderSizeUncompressed)
	err := EncodeHeader(buf, MaxPayloadSize+1, TypePing, AlgorithmNone, 0)
	if err != ErrMessageTooLarge {
		t.Errorf("Expected ErrMessageTooLarge, got %v", err)
	}
}

func TestHeaderBufferTooSmall(t *testing.T) {
	buf := make([]byte, 4) // Too small
	err := EncodeHeader(buf, 100, TypePing, AlgorithmNone, 0)
	if err == nil {
		t.Error("Expected error for small buffer")
	}
}

func TestDecodeHeaderTruncated(t *testing.T) {
	// Too short buffer
	_, err := DecodeHeader([]byte{0x00, 0x00, 0x00})
	if err != ErrTruncatedMessage {
		t.Errorf("Expected ErrTruncatedMessage, got %v", err)
	}

	// Compressed header but short buffer
	compressed := make([]byte, HeaderSizeCompressed)
	EncodeHeader(compressed, 100, TypePing, AlgorithmLZ4, 200)
	_, err = DecodeHeader(compressed[:6])
	if err != ErrTruncatedMessage {
		t.Errorf("Expected ErrTruncatedMessage for truncated compressed header, got %v", err)
	}
}

// TestDecodeHeaderFramingMarker exercises the first-byte framing-marker
// invariants from rippled's parseMessageHeader: compressed frames must have
// clear reserved bits and the exact LZ4 algorithm nibble (0x90); uncompressed
// frames must have all six top bits clear.
func TestDecodeHeaderFramingMarker(t *testing.T) {
	tests := []struct {
		name      string
		firstByte byte
		wantErr   error
	}{
		{"uncompressed_zero_flags", 0x00, nil},
		{"uncompressed_payload_top_bits", 0x03, nil},
		{"uncompressed_reserved_0x40", 0x40, ErrInvalidHeader},
		{"uncompressed_reserved_0x04", 0x04, ErrInvalidHeader},
		{"uncompressed_reserved_0x08", 0x08, ErrInvalidHeader},
		{"compressed_lz4", 0x90, nil},
		{"compressed_reserved_0x04", 0x94, ErrInvalidHeader},
		{"compressed_reserved_0x08", 0x98, ErrInvalidHeader},
		{"compressed_reserved_0x0C", 0x9C, ErrInvalidHeader},
		{"compressed_bad_algo_0x80", 0x80, ErrUnknownCompression},
		{"compressed_bad_algo_0xB0", 0xB0, ErrUnknownCompression},
		{"compressed_bad_algo_0xD0", 0xD0, ErrUnknownCompression},
		{"compressed_bad_algo_0xF0", 0xF0, ErrUnknownCompression},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, HeaderSizeCompressed)
			buf[0] = tt.firstByte

			header, err := DecodeHeader(buf)
			if err != tt.wantErr {
				t.Fatalf("DecodeHeader(first byte %#02x) error = %v, want %v", tt.firstByte, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			wantCompressed := tt.firstByte&0x80 != 0
			if header.Compressed != wantCompressed {
				t.Errorf("Compressed = %v, want %v", header.Compressed, wantCompressed)
			}
		})
	}
}

func TestReadWriteMessage(t *testing.T) {
	tests := []struct {
		name    string
		msgType MessageType
		payload []byte
	}{
		{"empty", TypePing, []byte{}},
		{"small", TypeTransaction, []byte{1, 2, 3, 4, 5}},
		{"medium", TypeValidation, bytes.Repeat([]byte{0xAB}, 1000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			// Write message
			err := WriteMessage(&buf, tt.msgType, tt.payload)
			if err != nil {
				t.Fatalf("WriteMessage failed: %v", err)
			}

			// Read message
			header, payload, err := ReadMessage(&buf)
			if err != nil {
				t.Fatalf("ReadMessage failed: %v", err)
			}

			if header.MessageType != tt.msgType {
				t.Errorf("MessageType = %d, want %d", header.MessageType, tt.msgType)
			}
			if !bytes.Equal(payload, tt.payload) {
				t.Errorf("Payload mismatch")
			}
		})
	}
}

func TestReadWriteMessageCompressed(t *testing.T) {
	var buf bytes.Buffer
	payload := bytes.Repeat([]byte{0x42}, 100)
	compressed := []byte{0x01, 0x02, 0x03} // Fake compressed data for test

	err := WriteMessageCompressed(&buf, TypeTransaction, compressed, AlgorithmLZ4, uint32(len(payload)))
	if err != nil {
		t.Fatalf("WriteMessageCompressed failed: %v", err)
	}

	header, readPayload, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if header.MessageType != TypeTransaction {
		t.Errorf("MessageType = %d, want %d", header.MessageType, TypeTransaction)
	}
	if !header.Compressed {
		t.Error("Compressed = false, want true")
	}
	if header.UncompressedSize != uint32(len(payload)) {
		t.Errorf("UncompressedSize = %d, want %d", header.UncompressedSize, len(payload))
	}
	if !bytes.Equal(readPayload, compressed) {
		t.Error("Compressed payload mismatch")
	}
}

// compressedFrame builds a complete LZ4-flagged wire frame: a small
// on-wire payload with an arbitrary uncompressed-size claim. It lets the
// cap tests exercise ReadMessage's size gates without materializing a
// large payload.
func compressedFrame(t *testing.T, msgType MessageType, wirePayload []byte, uncompressedSize uint32) []byte {
	t.Helper()
	buf := make([]byte, HeaderSizeCompressed+len(wirePayload))
	if err := EncodeHeader(buf, uint32(len(wirePayload)), msgType, AlgorithmLZ4, uncompressedSize); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	copy(buf[HeaderSizeCompressed:], wirePayload)
	return buf
}

// TestReadMessageCaps covers the per-type cap table and the hard 64 MB
// protocol ceiling. Bulk response types may approach the ceiling;
// request-shaped types keep stricter hardening; nothing may exceed the
// ceiling.
func TestReadMessageCaps(t *testing.T) {
	const mib = 1024 * 1024
	wire := []byte{0x01, 0x02, 0x03}

	tests := []struct {
		name       string
		msgType    MessageType
		uncompSize uint32
		wantTooBig bool
	}{
		// Bulk response types now permit well beyond the old 16 MiB cap.
		{"ledgerdata_20mib_ok", TypeLedgerData, 20 * mib, false},
		{"getobjects_20mib_ok", TypeGetObjects, 20 * mib, false},
		{"transactions_20mib_ok", TypeTransactions, 20 * mib, false},
		{"vlcollection_20mib_ok", TypeValidatorListCollection, 20 * mib, false},
		// Request-shaped types keep their stricter hardening caps.
		{"ping_20mib_rejected", TypePing, 20 * mib, true},
		{"getledger_20mib_rejected", TypeGetLedger, 20 * mib, true},
		// The protocol ceiling is hard even for the most permissive type.
		{"ledgerdata_over_ceiling_rejected", TypeLedgerData, MaxMessageSize + 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := compressedFrame(t, tt.msgType, wire, tt.uncompSize)
			_, _, err := ReadMessage(bytes.NewReader(frame))
			if tt.wantTooBig {
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatalf("ReadMessage err = %v, want ErrMessageTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadMessage err = %v, want nil (cap should permit)", err)
			}
		})
	}
}

func TestHeaderSize(t *testing.T) {
	uncompressed := &Header{Compressed: false}
	if uncompressed.HeaderSize() != HeaderSizeUncompressed {
		t.Errorf("Uncompressed HeaderSize = %d, want %d", uncompressed.HeaderSize(), HeaderSizeUncompressed)
	}

	compressed := &Header{Compressed: true}
	if compressed.HeaderSize() != HeaderSizeCompressed {
		t.Errorf("Compressed HeaderSize = %d, want %d", compressed.HeaderSize(), HeaderSizeCompressed)
	}
}

func TestHeaderTotalSize(t *testing.T) {
	header := &Header{
		PayloadSize: 1000,
		Compressed:  false,
	}
	expected := HeaderSizeUncompressed + 1000
	if header.TotalSize() != expected {
		t.Errorf("TotalSize = %d, want %d", header.TotalSize(), expected)
	}
}

func TestMessageTypeString(t *testing.T) {
	tests := []struct {
		msgType MessageType
		want    string
	}{
		{TypePing, "mtPING"},
		{TypeManifests, "mtMANIFESTS"},
		{TypeEndpoints, "mtENDPOINTS"},
		{TypeTransaction, "mtTRANSACTION"},
		{TypeValidation, "mtVALIDATION"},
		{TypeUnknown, "mtUNKNOWN"},
		{MessageType(9999), "mtUNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.msgType.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

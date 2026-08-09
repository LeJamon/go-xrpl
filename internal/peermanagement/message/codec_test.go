package message

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/iotest"
)

var _ interface{ HeaderSize() int } = Header{}

func readTestMessage(r io.Reader) (*Header, []byte, error) {
	header, err := ReadHeader(r)
	if err != nil {
		return nil, nil, err
	}
	payload, err := ReadPayload(r, *header)
	if err != nil {
		return nil, nil, err
	}
	return header, payload, nil
}

func rawTestHeader(payloadSize uint32, msgType MessageType, algorithm CompressionAlgorithm, uncompressedSize uint32) []byte {
	size := HeaderSizeUncompressed
	if algorithm != AlgorithmNone {
		size = HeaderSizeCompressed
	}
	buf := make([]byte, size)
	firstFour := payloadSize
	if algorithm != AlgorithmNone {
		firstFour |= uint32(algorithm) << 24
	}
	binary.BigEndian.PutUint32(buf[:4], firstFour)
	binary.BigEndian.PutUint16(buf[4:6], uint16(msgType))
	if algorithm != AlgorithmNone {
		binary.BigEndian.PutUint32(buf[6:10], uncompressedSize)
	}
	return buf
}

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
			if header.UncompressedSize != tt.payloadSize {
				t.Errorf("UncompressedSize = %d, want %d (equals PayloadSize for uncompressed frames)", header.UncompressedSize, tt.payloadSize)
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
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Errorf("Expected ErrMessageTooLarge, got %v", err)
	}
}

func TestEncodeHeaderRejectsUnknownAlgorithmsWithoutMutation(t *testing.T) {
	for _, raw := range []CompressionAlgorithm{0x01, 0x10, 0x80, 0x94, 0xA0, 0xF0} {
		t.Run(fmt.Sprintf("%#02x", raw), func(t *testing.T) {
			buf := bytes.Repeat([]byte{0x5a}, HeaderSizeCompressed)
			before := append([]byte(nil), buf...)
			err := EncodeHeader(buf, 1, TypePing, raw, 1)
			if !errors.Is(err, ErrUnknownCompression) {
				t.Fatalf("EncodeHeader error = %v, want ErrUnknownCompression", err)
			}
			if !bytes.Equal(buf, before) {
				t.Fatal("EncodeHeader mutated destination")
			}
		})
	}
}

func TestHeaderClaimBoundariesAreSymmetric(t *testing.T) {
	tests := []struct {
		name             string
		payloadSize      uint32
		msgType          MessageType
		algorithm        CompressionAlgorithm
		uncompressedSize uint32
		wantErr          bool
	}{
		{"ping exact", 2048, TypePing, AlgorithmNone, 0, false},
		{"ping over", 2049, TypePing, AlgorithmNone, 0, true},
		{"ping compressed wire over", 2049, TypePing, AlgorithmLZ4, 100, true},
		{"ping compressed claim over", 100, TypePing, AlgorithmLZ4, 2049, true},
		{"proof exact", largeMsgMax, TypeProofPathResponse, AlgorithmLZ4, largeMsgMax, false},
		{"proof over", 100, TypeProofPathResponse, AlgorithmLZ4, largeMsgMax + 1, true},
		{"replay above proof", 100, TypeReplayDeltaResponse, AlgorithmLZ4, largeMsgMax + 1, false},
		{"replay universal exact", 100, TypeReplayDeltaResponse, AlgorithmLZ4, MaxMessageSize, false},
		{"replay universal over", 100, TypeReplayDeltaResponse, AlgorithmLZ4, MaxMessageSize + 1, true},
		{"wire representable exact", MaxPayloadSize, TypeReplayDeltaResponse, AlgorithmLZ4, MaxMessageSize, false},
		{"wire unrepresentable", MaxPayloadSize + 1, TypeReplayDeltaResponse, AlgorithmLZ4, MaxMessageSize, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buf := bytes.Repeat([]byte{0x5a}, HeaderSizeCompressed)
			before := append([]byte(nil), buf...)
			err := EncodeHeader(buf, test.payloadSize, test.msgType, test.algorithm, test.uncompressedSize)
			if test.wantErr {
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatalf("EncodeHeader error = %v, want ErrMessageTooLarge", err)
				}
				if !bytes.Equal(buf, before) {
					t.Fatal("EncodeHeader mutated destination")
				}
			} else if err != nil {
				t.Fatalf("EncodeHeader error = %v", err)
			}

			raw := rawTestHeader(test.payloadSize, test.msgType, test.algorithm, test.uncompressedSize)
			_, readErr := ReadHeader(bytes.NewReader(raw))
			if test.payloadSize > MaxPayloadSize {
				if !errors.Is(readErr, ErrInvalidHeader) {
					t.Fatalf("ReadHeader error = %v, want ErrInvalidHeader", readErr)
				}
				return
			}
			if test.wantErr && !errors.Is(readErr, ErrMessageTooLarge) {
				t.Fatalf("ReadHeader error = %v, want ErrMessageTooLarge", readErr)
			}
			if !test.wantErr && readErr != nil {
				t.Fatalf("ReadHeader error = %v", readErr)
			}
		})
	}
}

func TestBuildWireMessagePingBoundary(t *testing.T) {
	frame, err := BuildWireMessage(TypePing, make([]byte, 2048))
	if err != nil {
		t.Fatalf("BuildWireMessage exact boundary: %v", err)
	}
	if _, err := ReadHeader(bytes.NewReader(frame)); err != nil {
		t.Fatalf("ReadHeader exact boundary: %v", err)
	}
	if _, err := BuildWireMessage(TypePing, make([]byte, 2049)); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("BuildWireMessage over boundary error = %v, want ErrMessageTooLarge", err)
	}
}

func TestReplayAndProofOutboundBoundaries(t *testing.T) {
	payload := make([]byte, largeMsgMax+1)
	replayFrame, err := BuildWireMessage(TypeReplayDeltaResponse, payload)
	if err != nil {
		t.Fatalf("BuildWireMessage replay above proof limit: %v", err)
	}
	if _, err := ReadHeader(bytes.NewReader(replayFrame)); err != nil {
		t.Fatalf("ReadHeader replay above proof limit: %v", err)
	}
	if _, err := BuildWireMessage(TypeProofPathResponse, payload); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("BuildWireMessage proof above limit error = %v, want ErrMessageTooLarge", err)
	}

	replayFrame, err = EncodeFrame(&ReplayDeltaResponse{Transactions: [][]byte{payload}})
	if err != nil {
		t.Fatalf("EncodeFrame replay above proof limit: %v", err)
	}
	replayHeader, err := ReadHeader(bytes.NewReader(replayFrame))
	if err != nil {
		t.Fatal(err)
	}
	if replayHeader.PayloadSize <= largeMsgMax {
		t.Fatalf("replay protobuf payload = %d, want above %d", replayHeader.PayloadSize, largeMsgMax)
	}

	if _, err := EncodeFrame(&ProofPathResponse{MapType: LedgerMapTransaction, Path: [][]byte{payload}}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("EncodeFrame proof above limit error = %v, want ErrMessageTooLarge", err)
	}
}

func TestPartialReadsPreserveTruncationAndCause(t *testing.T) {
	cause := errors.New("reader failed")
	compressed := rawTestHeader(1, TypeTransaction, AlgorithmLZ4, 100)
	payloadHeader, err := BuildWireMessage(TypePing, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		read      func() error
		wantCause error
	}{
		{
			name: "base header EOF",
			read: func() error {
				_, err := ReadHeader(bytes.NewReader(nil))
				return err
			},
			wantCause: io.EOF,
		},
		{
			name: "compressed suffix unexpected EOF",
			read: func() error {
				_, err := ReadHeader(bytes.NewReader(compressed[:HeaderSizeUncompressed+2]))
				return err
			},
			wantCause: io.ErrUnexpectedEOF,
		},
		{
			name: "payload custom cause",
			read: func() error {
				reader := io.MultiReader(bytes.NewReader(payloadHeader[:HeaderSizeUncompressed+1]), iotest.ErrReader(cause))
				header, err := ReadHeader(reader)
				if err != nil {
					return err
				}
				_, err = ReadPayload(reader, *header)
				return err
			},
			wantCause: cause,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.read()
			if !errors.Is(err, ErrTruncatedMessage) || !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want ErrTruncatedMessage and %v", err, test.wantCause)
			}
		})
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
			if !errors.Is(err, tt.wantErr) {
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

func TestBuildAndReadFrame(t *testing.T) {
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
			frame, err := BuildWireMessage(tt.msgType, tt.payload)
			if err != nil {
				t.Fatalf("BuildWireMessage failed: %v", err)
			}

			header, payload, err := readTestMessage(bytes.NewReader(frame))
			if err != nil {
				t.Fatalf("readTestMessage failed: %v", err)
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

func TestReadCompressedMessage(t *testing.T) {
	payload := bytes.Repeat([]byte{0x42}, 100)
	compressed := []byte{0x01, 0x02, 0x03} // Fake compressed data for test
	frame := append(rawTestHeader(uint32(len(compressed)), TypeTransaction, AlgorithmLZ4, uint32(len(payload))), compressed...)
	header, readPayload, err := readTestMessage(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("readTestMessage failed: %v", err)
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
// cap tests exercise header size gates without materializing a
// large payload.
func compressedFrame(t *testing.T, msgType MessageType, wirePayload []byte, uncompressedSize uint32) []byte {
	t.Helper()
	return append(rawTestHeader(uint32(len(wirePayload)), msgType, AlgorithmLZ4, uncompressedSize), wirePayload...)
}

// TestReadFrameCaps covers the per-type cap table and the hard 64 MB
// protocol ceiling. Bulk response types may approach the ceiling;
// request-shaped types keep stricter hardening; nothing may exceed the
// ceiling.
func TestReadFrameCaps(t *testing.T) {
	const mib = 1024 * 1024
	wire := []byte{0x01, 0x02, 0x03}

	tests := []struct {
		name       string
		msgType    MessageType
		uncompSize uint32
		wantTooBig bool
	}{
		// Bulk response/broadcast types now permit well beyond the old
		// 16 MiB cap, up to the protocol ceiling (rippled has no per-type
		// cap on these).
		{"ledgerdata_20mib_ok", TypeLedgerData, 20 * mib, false},
		{"getobjects_20mib_ok", TypeGetObjects, 20 * mib, false},
		{"transactions_20mib_ok", TypeTransactions, 20 * mib, false},
		{"vlcollection_20mib_ok", TypeValidatorListCollection, 20 * mib, false},
		{"manifests_20mib_ok", TypeManifests, 20 * mib, false},
		{"validatorlist_20mib_ok", TypeValidatorList, 20 * mib, false},
		{"unknown_20mib_ok", MessageType(9999), 20 * mib, false},
		// Request-shaped types keep their stricter hardening caps.
		{"ping_20mib_rejected", TypePing, 20 * mib, true},
		{"getledger_20mib_rejected", TypeGetLedger, 20 * mib, true},
		// The protocol ceiling is hard even for the most permissive type.
		{"ledgerdata_over_ceiling_rejected", TypeLedgerData, MaxMessageSize + 1, true},
		{"unknown_over_ceiling_rejected", MessageType(9999), MaxMessageSize + 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := compressedFrame(t, tt.msgType, wire, tt.uncompSize)
			_, _, err := readTestMessage(bytes.NewReader(frame))
			if tt.wantTooBig {
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatalf("readTestMessage err = %v, want ErrMessageTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readTestMessage err = %v, want nil (cap should permit)", err)
			}
		})
	}
}

func TestReadHeaderDoesNotConsumePayload(t *testing.T) {
	const payloadSize = 4
	headerBytes := make([]byte, HeaderSizeUncompressed, HeaderSizeUncompressed+len("dataafter"))
	if err := EncodeHeader(headerBytes, payloadSize, TypeManifests, AlgorithmNone, 0); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	reader := bytes.NewReader(append(headerBytes, []byte("dataafter")...))

	header, err := ReadHeader(reader)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if got, want := reader.Len(), payloadSize+len("after"); got != want {
		t.Fatalf("bytes remaining after ReadHeader = %d, want %d", got, want)
	}

	payload, err := ReadPayload(reader, *header)
	if err != nil {
		t.Fatalf("ReadPayload: %v", err)
	}
	if !bytes.Equal(payload, []byte("data")) {
		t.Fatalf("ReadPayload = %q, want data", payload)
	}
	rest := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, rest); err != nil {
		t.Fatalf("read trailing bytes: %v", err)
	}
	if !bytes.Equal(rest, []byte("after")) {
		t.Fatalf("trailing bytes = %q, want after", rest)
	}
}

func TestReadHeaderAndPayloadRejectClaimsBeforeAllocation(t *testing.T) {
	headerBytes := rawTestHeader(mediumMsgMax+1, TypePing, AlgorithmNone, 0)
	reader := bytes.NewReader(append(headerBytes, 0x7f))

	if _, err := ReadHeader(reader); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("ReadHeader err = %v, want ErrMessageTooLarge", err)
	}
	if got := reader.Len(); got != 1 {
		t.Fatalf("ReadHeader consumed %d payload bytes, want 0", 1-got)
	}

	_, err := ReadPayload(bytes.NewReader(nil), Header{
		PayloadSize:      mediumMsgMax + 1,
		MessageType:      TypePing,
		UncompressedSize: mediumMsgMax + 1,
	})
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("ReadPayload err = %v, want ErrMessageTooLarge", err)
	}
}

func TestHeaderSize(t *testing.T) {
	if got := (Header{Compressed: false}).HeaderSize(); got != HeaderSizeUncompressed {
		t.Errorf("Uncompressed HeaderSize = %d, want %d", got, HeaderSizeUncompressed)
	}

	if got := (Header{Compressed: true}).HeaderSize(); got != HeaderSizeCompressed {
		t.Errorf("Compressed HeaderSize = %d, want %d", got, HeaderSizeCompressed)
	}
}

func TestHeaderTotalSize(t *testing.T) {
	header := Header{
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

func TestEncodeFrame(t *testing.T) {
	msg := &Ping{PType: PingTypePing, Seq: 42}
	frame, err := EncodeFrame(msg)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	payload, err := Encode(msg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want, err := BuildWireMessage(msg.Type(), payload)
	if err != nil {
		t.Fatalf("BuildWireMessage: %v", err)
	}
	if !bytes.Equal(frame, want) {
		t.Fatalf("EncodeFrame = %x, want %x", frame, want)
	}
	header, err := DecodeHeader(frame)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if header.MessageType != TypePing {
		t.Fatalf("message type = %v, want %v", header.MessageType, TypePing)
	}
}

func TestBuildWireMessage(t *testing.T) {
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	frame, err := BuildWireMessage(TypeManifests, payload)
	if err != nil {
		t.Fatalf("BuildWireMessage: %v", err)
	}
	header, err := DecodeHeader(frame)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if header.MessageType != TypeManifests || header.PayloadSize != uint32(len(payload)) || header.Compressed {
		t.Fatalf("unexpected header: %+v", header)
	}
	if !bytes.Equal(frame[HeaderSizeUncompressed:], payload) {
		t.Fatalf("payload = %x, want %x", frame[HeaderSizeUncompressed:], payload)
	}
	payload[0] = 0
	if frame[HeaderSizeUncompressed] != 0xde {
		t.Fatal("wire message aliases input payload")
	}
}

func BenchmarkBuildWireMessage(b *testing.B) {
	payload := make([]byte, 1<<20)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if _, err := BuildWireMessage(TypeLedgerData, payload); err != nil {
			b.Fatal(err)
		}
	}
}

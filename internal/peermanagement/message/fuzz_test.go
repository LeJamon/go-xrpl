package message

import (
	"bytes"
	"testing"
)

// allMessageTypes lists every known MessageType for protobuf decode fuzzing.
var allMessageTypes = []MessageType{
	TypeManifests,
	TypePing,
	TypeCluster,
	TypeEndpoints,
	TypeTransaction,
	TypeGetLedger,
	TypeLedgerData,
	TypeProposeLedger,
	TypeStatusChange,
	TypeHaveSet,
	TypeValidation,
	TypeGetObjects,
	TypeValidatorList,
	TypeSquelch,
	TypeValidatorListCollection,
	TypeProofPathReq,
	TypeProofPathResponse,
	TypeReplayDeltaReq,
	TypeReplayDeltaResponse,
	TypeHaveTransactions,
	TypeTransactions,
}

// mustEncodeHeader encodes a header into a new buffer, panicking on failure.
// Used only during seed corpus construction (before Fuzz loop).
func mustEncodeHeader(payloadSize uint32, msgType MessageType, algo CompressionAlgorithm, uncompressedSize uint32) []byte {
	size := HeaderSizeUncompressed
	if algo != AlgorithmNone {
		size = HeaderSizeCompressed
	}
	buf := make([]byte, size)
	if err := EncodeHeader(buf, payloadSize, msgType, algo, uncompressedSize); err != nil {
		panic("mustEncodeHeader: " + err.Error())
	}
	return buf
}

// FuzzDecodeHeader feeds arbitrary bytes into DecodeHeader.
// It must never panic. On success it validates invariants and round-trips.
func FuzzDecodeHeader(f *testing.F) {
	// Seed: empty bytes
	f.Add([]byte{})
	// Seed: 5 bytes (too short for a header)
	f.Add([]byte{0x00, 0x01, 0x02, 0x03, 0x04})
	// Seed: valid 6-byte uncompressed header (payload=100, type=Ping)
	f.Add(mustEncodeHeader(100, TypePing, AlgorithmNone, 0))
	// Seed: valid 10-byte compressed header (payload=50, type=Transaction, LZ4, uncompressed=200)
	f.Add(mustEncodeHeader(50, TypeTransaction, AlgorithmLZ4, 200))
	// Seed: compression bit set with invalid algorithm bits (algorithm=2)
	f.Add([]byte{0xA0, 0x00, 0x00, 0x32, 0x00, 0x1E, 0x00, 0x00, 0x00, 0xC8})
	// Seed: max payload size at 26-bit limit
	f.Add(mustEncodeHeader(MaxPayloadSize, TypeLedgerData, AlgorithmNone, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := DecodeHeader(data)
		if err != nil {
			return
		}

		// Invariant: payload size must be within limits.
		if h.PayloadSize > MaxPayloadSize {
			t.Fatalf("PayloadSize %d exceeds MaxPayloadSize %d", h.PayloadSize, MaxPayloadSize)
		}

		// Invariant: compressed headers must use LZ4.
		if h.Compressed && h.Algorithm != AlgorithmLZ4 {
			t.Fatalf("Compressed=true but Algorithm=%d (expected AlgorithmLZ4=%d)", h.Algorithm, AlgorithmLZ4)
		}

		// Round-trip: encode the decoded header, then decode again and compare.
		rtBufSize := HeaderSizeUncompressed
		algo := AlgorithmNone
		if h.Compressed {
			rtBufSize = HeaderSizeCompressed
			algo = h.Algorithm
		}
		rtBuf := make([]byte, rtBufSize)
		if err := EncodeHeader(rtBuf, h.PayloadSize, h.MessageType, algo, h.UncompressedSize); err != nil {
			// EncodeHeader may reject values that DecodeHeader tolerates (e.g. size > MaxPayloadSize).
			// Since we already checked PayloadSize <= MaxPayloadSize, this should not happen.
			t.Fatalf("EncodeHeader round-trip failed: %v", err)
		}

		h2, err := DecodeHeader(rtBuf)
		if err != nil {
			t.Fatalf("DecodeHeader round-trip failed: %v", err)
		}

		if h.PayloadSize != h2.PayloadSize {
			t.Fatalf("PayloadSize mismatch: %d vs %d", h.PayloadSize, h2.PayloadSize)
		}
		if h.MessageType != h2.MessageType {
			t.Fatalf("MessageType mismatch: %d vs %d", h.MessageType, h2.MessageType)
		}
		if h.Compressed != h2.Compressed {
			t.Fatalf("Compressed mismatch: %v vs %v", h.Compressed, h2.Compressed)
		}
		if h.Compressed {
			if h.Algorithm != h2.Algorithm {
				t.Fatalf("Algorithm mismatch: %d vs %d", h.Algorithm, h2.Algorithm)
			}
			if h.UncompressedSize != h2.UncompressedSize {
				t.Fatalf("UncompressedSize mismatch: %d vs %d", h.UncompressedSize, h2.UncompressedSize)
			}
		}
	})
}

// FuzzReadMessage wraps arbitrary bytes in a reader and calls ReadMessage.
// It must never panic. On success it validates payload length matches the header.
func FuzzReadMessage(f *testing.F) {
	// Seed: empty
	f.Add([]byte{})

	// Seed: valid uncompressed message (header payload=4, type=Ping, then 4 bytes payload)
	{
		hdr := make([]byte, HeaderSizeUncompressed, HeaderSizeUncompressed+4)
		_ = EncodeHeader(hdr, 4, TypePing, AlgorithmNone, 0)
		msg := append(hdr, 0xDE, 0xAD, 0xBE, 0xEF)
		f.Add(msg)
	}

	// Seed: truncated — header claims payload=100 but no payload bytes follow
	{
		hdr := make([]byte, HeaderSizeUncompressed)
		_ = EncodeHeader(hdr, 100, TypePing, AlgorithmNone, 0)
		f.Add(hdr)
	}

	// Seed: valid compressed message (header + some payload)
	{
		hdr := make([]byte, HeaderSizeCompressed, HeaderSizeCompressed+3)
		_ = EncodeHeader(hdr, 3, TypeTransaction, AlgorithmLZ4, 100)
		msg := append(hdr, 0x01, 0x02, 0x03)
		f.Add(msg)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Quick pre-check: if header parses and claims >1MB payload, skip to prevent OOM.
		if len(data) >= HeaderSizeUncompressed {
			if h, err := DecodeHeader(data); err == nil && h.PayloadSize > 1<<20 {
				t.Skip("skipping large payload to prevent OOM")
			}
		}

		r := bytes.NewReader(data)
		header, payload, err := ReadMessage(r)
		if err != nil {
			return
		}

		// Invariant: payload length must equal header's PayloadSize.
		if uint32(len(payload)) != header.PayloadSize {
			t.Fatalf("payload length %d != header.PayloadSize %d", len(payload), header.PayloadSize)
		}
	})
}

// FuzzDecodeProto feeds arbitrary bytes to Decode for each message type.
// It must never panic. On success it verifies Encode does not panic.
func FuzzDecodeProto(f *testing.F) {
	// Seed: Ping with empty payload
	f.Add(uint8(0), []byte{})

	// Seed: valid Ping protobuf bytes
	{
		ping := &Ping{PType: PingTypePong, Seq: 1}
		encoded, err := Encode(ping)
		if err == nil {
			f.Add(uint8(0), encoded)
		}
	}

	// Seed: different type selector with empty payload
	f.Add(uint8(1), []byte{})

	// Seed: garbage bytes for Ping type
	f.Add(uint8(0), []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA, 0xF9, 0xF8})

	// Seed: valid Transaction protobuf bytes
	{
		tx := &Transaction{RawTransaction: []byte{1, 2, 3}, Status: TxStatusNew}
		encoded, err := Encode(tx)
		if err == nil {
			f.Add(uint8(4), encoded)
		}
	}

	f.Fuzz(func(t *testing.T, selector uint8, payload []byte) {
		msgType := allMessageTypes[int(selector)%len(allMessageTypes)]

		msg, err := Decode(msgType, payload)
		if err != nil {
			return
		}

		// Verify Encode does not panic on the successfully decoded message.
		_, _ = Encode(msg)
	})
}

// FuzzHeaderRoundTrip constructs headers from structured fuzzer inputs and
// verifies encode/decode round-trip integrity.
func FuzzHeaderRoundTrip(f *testing.F) {
	// Seed: uncompressed Ping
	f.Add(uint32(100), uint16(3), uint8(0), uint32(0))
	// Seed: compressed Transaction
	f.Add(uint32(50), uint16(30), uint8(1), uint32(200))
	// Seed: zero values
	f.Add(uint32(0), uint16(0), uint8(0), uint32(0))
	// Seed: max payload uncompressed
	f.Add(uint32(MaxPayloadSize), uint16(41), uint8(0), uint32(0))
	// Seed: compressed with large uncompressed size
	f.Add(uint32(1000), uint16(32), uint8(1), uint32(50000))

	f.Fuzz(func(t *testing.T, payloadSize uint32, msgType uint16, algorithm uint8, uncompressedSize uint32) {
		if payloadSize > MaxPayloadSize {
			t.Skip("payload size exceeds maximum")
		}
		if algorithm > 1 {
			t.Skip("fuzz input maps only 0 and 1 to valid algorithms")
		}

		algo := AlgorithmNone
		if algorithm == 1 {
			algo = AlgorithmLZ4
		}
		mt := MessageType(msgType)

		bufSize := HeaderSizeUncompressed
		if algo != AlgorithmNone {
			bufSize = HeaderSizeCompressed
		}
		buf := make([]byte, bufSize)

		if err := EncodeHeader(buf, payloadSize, mt, algo, uncompressedSize); err != nil {
			t.Fatalf("EncodeHeader failed: %v", err)
		}

		h, err := DecodeHeader(buf)
		if err != nil {
			t.Fatalf("DecodeHeader failed after successful EncodeHeader: %v", err)
		}

		if h.PayloadSize != payloadSize {
			t.Fatalf("PayloadSize: got %d, want %d", h.PayloadSize, payloadSize)
		}
		if h.MessageType != mt {
			t.Fatalf("MessageType: got %d, want %d", h.MessageType, mt)
		}

		expectCompressed := algo != AlgorithmNone
		if h.Compressed != expectCompressed {
			t.Fatalf("Compressed: got %v, want %v", h.Compressed, expectCompressed)
		}
		if h.Compressed {
			if h.Algorithm != algo {
				t.Fatalf("Algorithm: got %d, want %d", h.Algorithm, algo)
			}
			if h.UncompressedSize != uncompressedSize {
				t.Fatalf("UncompressedSize: got %d, want %d", h.UncompressedSize, uncompressedSize)
			}
		}
	})
}

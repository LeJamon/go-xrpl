package message

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"fmt"
	"testing"

	"github.com/pierrec/lz4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============== Compression Roundtrip Tests ==============
// Reference: rippled src/test/overlay/compression_test.cpp

// Compression constants (matching rippled)
const (
	minCompressibleSize = 70
)

// compressLZ4 compresses data using LZ4.
// Returns the compressed data or nil if compression wouldn't save space.
func compressLZ4(data []byte) ([]byte, error) {
	if len(data) < minCompressibleSize {
		return nil, nil
	}

	maxSize := lz4.CompressBlockBound(len(data))
	compressed := make([]byte, maxSize)

	n, err := lz4.CompressBlock(data, compressed, nil)
	if err != nil {
		return nil, err
	}

	if n == 0 || n >= len(data) {
		return nil, nil
	}

	return compressed[:n], nil
}

// decompressLZ4 decompresses LZ4 compressed data.
func decompressLZ4(compressed []byte, uncompressedSize int) ([]byte, error) {
	decompressed := make([]byte, uncompressedSize)
	n, err := lz4.UncompressBlock(compressed, decompressed)
	if err != nil {
		return nil, err
	}

	if n != uncompressedSize {
		return nil, fmt.Errorf("decompression size mismatch: expected %d, got %d", uncompressedSize, n)
	}

	return decompressed, nil
}

// buildManifests creates test manifests similar to rippled compression_test.cpp
// Reference: rippled compression_test.cpp buildManifests()
func buildManifests(n int) *Manifests {
	manifests := &Manifests{
		List:    make([]Manifest, n),
		History: false,
	}

	for i := 0; i < n; i++ {
		// Generate realistic manifest data
		// In rippled this includes: sfSequence, sfPublicKey, sfSigningPubKey, sfDomain, sfMasterSignature, sfSignature
		// We simulate this with random bytes of appropriate size
		stObject := make([]byte, 200+i%50) // Variable size manifest
		rand.Read(stObject)

		// Add some structure to make it more realistic
		stObject[0] = byte(i % 256)                                     // Sequence-like byte
		stObject[1] = 0xED                                              // ed25519 key prefix
		copy(stObject[2:35], randomBytes(33))                           // Public key
		copy(stObject[35:68], randomBytes(33))                          // Signing key
		copy(stObject[68:100], []byte(fmt.Sprintf("example%d.com", i))) // Domain

		manifests.List[i] = Manifest{STObject: stObject}
	}

	return manifests
}

// buildEndpoints creates test endpoints similar to rippled compression_test.cpp
// Reference: rippled compression_test.cpp buildEndpoints()
func buildEndpoints(n int) *Endpoints {
	endpoints := &Endpoints{
		Version:     2,
		EndpointsV2: make([]Endpointv2, n),
	}

	for i := 0; i < n; i++ {
		endpoints.EndpointsV2[i] = Endpointv2{
			Endpoint: fmt.Sprintf("10.0.1.%d", i%256),
			Hops:     uint32(i % 4),
		}
	}

	return endpoints
}

// buildTransaction creates a test transaction similar to rippled compression_test.cpp
// Reference: rippled compression_test.cpp buildTransaction()
func buildTransaction() *Transaction {
	// Generate realistic transaction data (serialized transaction)
	rawTx := make([]byte, 300)
	rand.Read(rawTx)

	// Add some structure - transaction type prefix, account IDs, etc.
	rawTx[0] = 0x53 // Transaction type byte
	rawTx[1] = 0x54 // Type: Payment

	return &Transaction{
		RawTransaction:   rawTx,
		Status:           TxStatusNew,
		ReceiveTimestamp: 1234567890,
		Deferred:         true,
	}
}

// buildLedgerData creates test ledger data similar to rippled compression_test.cpp
// Reference: rippled compression_test.cpp buildLedgerData()
func buildLedgerData(n int) *LedgerData {
	hash := sha512Half([]byte{0x12, 0x34, 0x56, 0x78, 0x9a})

	ledgerData := &LedgerData{
		LedgerHash:    hash,
		LedgerSeq:     123456789,
		InfoType:      LedgerInfoAsNode,
		Nodes:         make([]LedgerNode, n),
		RequestCookie: 123456789,
		Error:         ReplyErrorNoLedger,
	}

	for i := 0; i < n; i++ {
		// Simulate ledger node data (like serialized LedgerInfo)
		nodeData := make([]byte, 100+i%50)
		rand.Read(nodeData)

		// Add sequence-like structure
		nodeData[0] = byte(i >> 24)
		nodeData[1] = byte(i >> 16)
		nodeData[2] = byte(i >> 8)
		nodeData[3] = byte(i)

		ledgerData.Nodes[i] = LedgerNode{
			NodeData: nodeData,
			NodeID:   sha512Half([]byte{byte(i)}),
		}
	}

	return ledgerData
}

// buildGetObjectByHash creates test get objects request
// Reference: rippled compression_test.cpp buildGetObjectByHash()
func buildGetObjectByHash(n int) *GetObjectByHash {
	hash := sha512Half([]byte{0x12, 0x34, 0x56, 0x78, 0x9a})

	getObject := &GetObjectByHash{
		ObjType:    ObjectTypeTransaction,
		Query:      true,
		Seq:        123456789,
		LedgerHash: hash,
		Fat:        true,
		Objects:    make([]IndexedObject, n),
	}

	for i := 0; i < n; i++ {
		objectHash := sha512Half([]byte{byte(i)})
		getObject.Objects[i] = IndexedObject{
			Hash:      objectHash,
			NodeID:    objectHash,
			Index:     nil,
			Data:      nil,
			LedgerSeq: uint32(i),
		}
	}

	return getObject
}

// buildValidatorList creates test validator list
// Reference: rippled compression_test.cpp buildValidatorList()
func buildValidatorList() *ValidatorList {
	manifest := make([]byte, 200)
	rand.Read(manifest)
	manifest[0] = 0xED // ed25519 prefix

	blob := make([]byte, 1000)
	rand.Read(blob)

	signature := make([]byte, 64)
	rand.Read(signature)

	return &ValidatorList{
		Manifest:  manifest,
		Blob:      blob,
		Signature: signature,
		Version:   3,
	}
}

// Helper functions

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func sha512Half(data []byte) []byte {
	h := sha512.Sum512(data)
	return h[:32]
}

// ============== Test Cases ==============

// TestCompressionRoundtrip_Manifests tests manifest compression roundtrip
// Reference: rippled compression_test.cpp - testManifests()
func TestCompressionRoundtrip_Manifests(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"10_manifests", 10},
		{"50_manifests", 50},
		{"100_manifests", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildManifests(tt.count)
			testCompressionRoundtrip(t, msg)
		})
	}
}

// TestCompressionRoundtrip_Endpoints tests endpoints compression roundtrip
// Reference: rippled compression_test.cpp - testEndpoints()
func TestCompressionRoundtrip_Endpoints(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"10_endpoints", 10},
		{"50_endpoints", 50},
		{"100_endpoints", 100},
		{"200_endpoints", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildEndpoints(tt.count)
			testCompressionRoundtrip(t, msg)
		})
	}
}

// TestCompressionRoundtrip_Transaction tests transaction compression roundtrip
// Reference: rippled compression_test.cpp - testTransaction()
func TestCompressionRoundtrip_Transaction(t *testing.T) {
	msg := buildTransaction()
	testCompressionRoundtrip(t, msg)
}

// TestCompressionRoundtrip_LedgerData tests ledger data compression roundtrip
// Reference: rippled compression_test.cpp - testLedgerData()
func TestCompressionRoundtrip_LedgerData(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"10_nodes", 10},
		{"50_nodes", 50},
		{"100_nodes", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildLedgerData(tt.count)
			testCompressionRoundtrip(t, msg)
		})
	}
}

// TestCompressionRoundtrip_GetObjectByHash tests get objects compression roundtrip
// Reference: rippled compression_test.cpp - testGetObjectByHash()
func TestCompressionRoundtrip_GetObjectByHash(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"10_objects", 10},
		{"50_objects", 50},
		{"100_objects", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildGetObjectByHash(tt.count)
			testCompressionRoundtrip(t, msg)
		})
	}
}

// TestCompressionRoundtrip_ValidatorList tests validator list compression roundtrip
// Reference: rippled compression_test.cpp - testValidatorList()
func TestCompressionRoundtrip_ValidatorList(t *testing.T) {
	msg := buildValidatorList()
	testCompressionRoundtrip(t, msg)
}

// testCompressionRoundtrip performs compression roundtrip test for any message type
func testCompressionRoundtrip(t *testing.T, msg Message) {
	t.Helper()

	// 1. Encode the message
	original, err := Encode(msg)
	require.NoError(t, err, "Failed to encode message")
	require.NotEmpty(t, original, "Encoded message should not be empty")

	// 2. Compress the message
	compressed, err := compressLZ4(original)
	require.NoError(t, err, "Failed to compress message")

	// Skip compression tests if data is too small or incompressible
	if compressed == nil {
		t.Logf("Message too small or incompressible (size: %d bytes)", len(original))
		return
	}

	// 3. Verify compression reduces size
	compressionRatio := float64(len(compressed)) / float64(len(original))
	t.Logf("Original: %d bytes, Compressed: %d bytes, Ratio: %.2f%%",
		len(original), len(compressed), compressionRatio*100)

	assert.Less(t, len(compressed), len(original),
		"Compressed size should be less than original")

	// 4. Decompress
	decompressed, err := decompressLZ4(compressed, len(original))
	require.NoError(t, err, "Failed to decompress message")

	// 5. Verify decompressed matches original
	assert.Equal(t, original, decompressed,
		"Decompressed data should match original")

	// 6. Decode the decompressed message
	decoded, err := Decode(msg.Type(), decompressed)
	require.NoError(t, err, "Failed to decode decompressed message")
	require.NotNil(t, decoded, "Decoded message should not be nil")

	// 7. Verify message type matches
	assert.Equal(t, msg.Type(), decoded.Type(),
		"Decoded message type should match original")
}

// TestCompressionMultiBuffer tests compression with multi-buffer scenarios
// Reference: rippled compression_test.cpp - simulates network chunked reads
func TestCompressionMultiBuffer(t *testing.T) {
	// Create a large message that will compress well
	// Endpoints compress much better than random data
	msg := buildEndpoints(500)

	original, err := Encode(msg)
	require.NoError(t, err)

	compressed, err := compressLZ4(original)
	require.NoError(t, err)
	require.NotNil(t, compressed, "Message should be compressible")

	// Test reading in chunks of various sizes
	chunkSizes := []int{16, 64, 128, 256, 512, 1024}

	for _, chunkSize := range chunkSizes {
		t.Run(fmt.Sprintf("chunk_%d", chunkSize), func(t *testing.T) {
			// Simulate reading the compressed data in chunks
			var chunks [][]byte
			for i := 0; i < len(compressed); i += chunkSize {
				end := i + chunkSize
				if end > len(compressed) {
					end = len(compressed)
				}
				chunks = append(chunks, compressed[i:end])
			}

			// Reassemble the chunks
			var reassembled bytes.Buffer
			for _, chunk := range chunks {
				reassembled.Write(chunk)
			}

			// Decompress the reassembled data
			decompressed, err := decompressLZ4(reassembled.Bytes(), len(original))
			require.NoError(t, err)

			// Verify
			assert.Equal(t, original, decompressed)
		})
	}
}

// TestCompressionHeaderRoundtrip tests encoding/decoding compressed message headers
func TestCompressionHeaderRoundtrip(t *testing.T) {
	tests := []struct {
		name             string
		payloadSize      uint32
		msgType          MessageType
		algorithm        CompressionAlgorithm
		uncompressedSize uint32
	}{
		{
			name:             "uncompressed_manifests",
			payloadSize:      1000,
			msgType:          TypeManifests,
			algorithm:        AlgorithmNone,
			uncompressedSize: 0,
		},
		{
			name:             "compressed_manifests",
			payloadSize:      800,
			msgType:          TypeManifests,
			algorithm:        AlgorithmLZ4,
			uncompressedSize: 1000,
		},
		{
			name:             "compressed_endpoints",
			payloadSize:      500,
			msgType:          TypeEndpoints,
			algorithm:        AlgorithmLZ4,
			uncompressedSize: 2000,
		},
		{
			name:             "compressed_transaction",
			payloadSize:      200,
			msgType:          TypeTransaction,
			algorithm:        AlgorithmLZ4,
			uncompressedSize: 300,
		},
		{
			name:             "compressed_ledger_data",
			payloadSize:      5000,
			msgType:          TypeLedgerData,
			algorithm:        AlgorithmLZ4,
			uncompressedSize: 10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Allocate buffer for header
			bufSize := HeaderSizeUncompressed
			if tt.algorithm != AlgorithmNone {
				bufSize = HeaderSizeCompressed
			}
			buf := make([]byte, bufSize)

			// Encode header
			err := EncodeHeader(buf, tt.payloadSize, tt.msgType, tt.algorithm, tt.uncompressedSize)
			require.NoError(t, err)

			// Decode header
			header, err := DecodeHeader(buf)
			require.NoError(t, err)

			// Verify
			assert.Equal(t, tt.payloadSize, header.PayloadSize)
			assert.Equal(t, tt.msgType, header.MessageType)
			assert.Equal(t, tt.algorithm != AlgorithmNone, header.Compressed)

			if header.Compressed {
				assert.Equal(t, tt.algorithm, header.Algorithm)
				assert.Equal(t, tt.uncompressedSize, header.UncompressedSize)
			}
		})
	}
}

// TestCompressionMinSize tests that small messages are not compressed
func TestCompressionMinSize(t *testing.T) {
	// Create a message smaller than minCompressibleSize
	smallData := make([]byte, 50)
	rand.Read(smallData)

	compressed, err := compressLZ4(smallData)
	require.NoError(t, err)
	assert.Nil(t, compressed, "Small data should not be compressed")

	// Create a message at the threshold
	thresholdData := make([]byte, minCompressibleSize)
	rand.Read(thresholdData)

	// This may or may not compress depending on content
	_, err = compressLZ4(thresholdData)
	require.NoError(t, err)
}

// TestCompressionIncompressible tests handling of incompressible data
func TestCompressionIncompressible(t *testing.T) {
	// Create random (incompressible) data
	randomData := make([]byte, 1000)
	rand.Read(randomData)

	compressed, err := compressLZ4(randomData)
	require.NoError(t, err)

	// Random data typically doesn't compress well
	// Either it returns nil (won't save space) or compressed >= original
	if compressed != nil {
		t.Logf("Random data compressed: original=%d, compressed=%d", len(randomData), len(compressed))
	} else {
		t.Log("Random data not compressed (as expected)")
	}
}

// TestCompressionHighlyCompressible tests highly compressible data
func TestCompressionHighlyCompressible(t *testing.T) {
	// Create highly compressible data (repeated patterns)
	repeatableData := bytes.Repeat([]byte("XRPL_COMPRESS_TEST_"), 100)

	compressed, err := compressLZ4(repeatableData)
	require.NoError(t, err)
	require.NotNil(t, compressed, "Repetitive data should be compressible")

	ratio := float64(len(compressed)) / float64(len(repeatableData))
	t.Logf("Highly compressible: original=%d, compressed=%d, ratio=%.2f%%",
		len(repeatableData), len(compressed), ratio*100)

	// Should achieve significant compression
	assert.Less(t, ratio, 0.5, "Highly compressible data should compress to < 50%%")

	// Verify roundtrip
	decompressed, err := decompressLZ4(compressed, len(repeatableData))
	require.NoError(t, err)
	assert.Equal(t, repeatableData, decompressed)
}

// TestCompressionFullMessageFlow tests the full message encode -> compress -> send -> decompress -> decode flow
func TestCompressionFullMessageFlow(t *testing.T) {
	// 1. Create a message
	msg := buildEndpoints(100)

	// 2. Encode to protobuf
	encoded, err := Encode(msg)
	require.NoError(t, err)

	// 3. Compress
	compressed, err := compressLZ4(encoded)
	require.NoError(t, err)
	require.NotNil(t, compressed)

	// 4. Create wire message with header
	var wireMsg bytes.Buffer

	// Write compressed header
	header := make([]byte, HeaderSizeCompressed)
	err = EncodeHeader(header, uint32(len(compressed)), msg.Type(), AlgorithmLZ4, uint32(len(encoded)))
	require.NoError(t, err)
	wireMsg.Write(header)
	wireMsg.Write(compressed)

	// 5. Read from wire
	wireData := wireMsg.Bytes()

	// 6. Parse header
	parsedHeader, err := DecodeHeader(wireData)
	require.NoError(t, err)
	assert.True(t, parsedHeader.Compressed)
	assert.Equal(t, AlgorithmLZ4, parsedHeader.Algorithm)
	assert.Equal(t, msg.Type(), parsedHeader.MessageType)

	// 7. Extract payload
	payload := wireData[HeaderSizeCompressed : HeaderSizeCompressed+int(parsedHeader.PayloadSize)]

	// 8. Decompress
	decompressed, err := decompressLZ4(payload, int(parsedHeader.UncompressedSize))
	require.NoError(t, err)

	// 9. Decode message
	decoded, err := Decode(parsedHeader.MessageType, decompressed)
	require.NoError(t, err)

	// 10. Verify
	decodedEndpoints, ok := decoded.(*Endpoints)
	require.True(t, ok)
	assert.Equal(t, msg.Version, decodedEndpoints.Version)
	assert.Len(t, decodedEndpoints.EndpointsV2, len(msg.EndpointsV2))
}

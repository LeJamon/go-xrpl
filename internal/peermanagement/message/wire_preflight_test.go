package message

import (
	"errors"
	"testing"
	"unsafe"

	peerproto "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

func repeatedMessageField(field protowire.Number, count int, entry []byte) []byte {
	wire := make([]byte, 0, count*(len(entry)+2))
	for range count {
		wire = protowire.AppendTag(wire, field, protowire.BytesType)
		wire = protowire.AppendBytes(wire, entry)
	}
	return wire
}

func requireWireLimit(t *testing.T, err error, reason WireLimitReason, limit, observed int) {
	t.Helper()
	require.ErrorIs(t, err, ErrWireLimit)
	var limitErr *WireLimitError
	require.ErrorAs(t, err, &limitErr)
	require.Equal(t, reason, limitErr.Reason)
	require.Equal(t, limit, limitErr.Limit)
	require.Equal(t, observed, limitErr.Observed)
}

func TestPreflightCollectionLimitsInclusive(t *testing.T) {
	tests := []struct {
		name    string
		msgType MessageType
		field   protowire.Number
		limit   int
		reason  WireLimitReason
		prefix  []byte
	}{
		{name: "transactions", msgType: TypeTransactions, field: 1, limit: maxTransactions, reason: WireLimitTransactions},
		{name: "get objects transactions", msgType: TypeGetObjects, field: 6, limit: maxTransactions, reason: WireLimitGetObjectTransactions, prefix: enumField(1, uint64(ObjectTypeTransactions))},
		{name: "ledger data", msgType: TypeLedgerData, field: 4, limit: maxLedgerDataNodes, reason: WireLimitLedgerData},
		{name: "endpoints", msgType: TypeEndpoints, field: 3, limit: maxEndpoints, reason: WireLimitEndpoints},
		{name: "validator blobs", msgType: TypeValidatorListCollection, field: 3, limit: maxValidatorBlobs, reason: WireLimitValidatorBlobs},
		{name: "manifests", msgType: TypeManifests, field: 1, limit: maxManifests, reason: WireLimitManifests},
		{name: "cluster nodes", msgType: TypeCluster, field: 1, limit: maxClusterNodes, reason: WireLimitClusterNodes},
		{name: "load sources", msgType: TypeCluster, field: 2, limit: maxLoadSources, reason: WireLimitLoadSources},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			atLimit := append(append([]byte(nil), test.prefix...), repeatedMessageField(test.field, test.limit, nil)...)
			require.NoError(t, Preflight(test.msgType, atLimit))

			overLimit := append(atLimit, repeatedMessageField(test.field, 1, nil)...)
			requireWireLimit(t, Preflight(test.msgType, overLimit), test.reason, test.limit, test.limit+1)
		})
	}
}

func TestPreflightClusterLimitsAreIndependent(t *testing.T) {
	wire := repeatedMessageField(1, maxClusterNodes, nil)
	wire = append(wire, repeatedMessageField(2, maxLoadSources, nil)...)
	require.NoError(t, Preflight(TypeCluster, wire))

	overNodes := append(append([]byte(nil), wire...), repeatedMessageField(1, 1, nil)...)
	requireWireLimit(t, Preflight(TypeCluster, overNodes), WireLimitClusterNodes, maxClusterNodes, maxClusterNodes+1)

	overSources := append(append([]byte(nil), wire...), repeatedMessageField(2, 1, nil)...)
	requireWireLimit(t, Preflight(TypeCluster, overSources), WireLimitLoadSources, maxLoadSources, maxLoadSources+1)
}

func enumField(field protowire.Number, value uint64) []byte {
	wire := protowire.AppendTag(nil, field, protowire.VarintType)
	return protowire.AppendVarint(wire, value)
}

func TestPreflightGetObjectsUsesLastRecognizedType(t *testing.T) {
	objects10001 := repeatedMessageField(6, maxTransactions+1, nil)
	objects12288 := repeatedMessageField(6, maxGetObjects, nil)
	unknown := uint64(99)

	tests := []struct {
		name   string
		types  []uint64
		wire   []byte
		limit  int
		reject bool
	}{
		{name: "absent type uses general cap", wire: objects12288},
		{name: "unknown type uses general cap", types: []uint64{unknown}, wire: objects12288},
		{name: "transactions then unknown stays transactions", types: []uint64{uint64(ObjectTypeTransactions), unknown}, wire: objects10001, limit: maxTransactions, reject: true},
		{name: "transactions then ledger uses general cap", types: []uint64{uint64(ObjectTypeTransactions), uint64(ObjectTypeLedger)}, wire: objects12288},
		{name: "ledger unknown transactions uses transactions cap", types: []uint64{uint64(ObjectTypeLedger), unknown, uint64(ObjectTypeTransactions)}, wire: objects10001, limit: maxTransactions, reject: true},
		{name: "unknown transactions unknown stays transactions", types: []uint64{unknown, uint64(ObjectTypeTransactions), unknown}, wire: objects10001, limit: maxTransactions, reject: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wire []byte
			for _, value := range test.types {
				wire = append(wire, enumField(1, value)...)
			}
			wire = append(wire, test.wire...)
			err := Preflight(TypeGetObjects, wire)
			if !test.reject {
				require.NoError(t, err)
				return
			}
			requireWireLimit(t, err, WireLimitGetObjectTransactions, test.limit, maxTransactions+1)
		})
	}

	overGeneral := repeatedMessageField(6, maxGetObjects+1, nil)
	requireWireLimit(t, Preflight(TypeGetObjects, overGeneral), WireLimitGetObjects, maxGetObjects, maxGetObjects+1)
}

func TestPreflightMalformedWire(t *testing.T) {
	nestedTruncatedFixed32 := protowire.AppendTag(nil, 100, protowire.Fixed32Type)
	nestedTruncatedFixed32 = append(nestedTruncatedFixed32, 1, 2, 3)
	nestedFixed := protowire.AppendTag(nil, 1, protowire.BytesType)
	nestedFixed = protowire.AppendBytes(nestedFixed, nestedTruncatedFixed32)

	tests := []struct {
		name    string
		msgType MessageType
		wire    []byte
	}{
		{name: "zero tag", msgType: TypePing, wire: []byte{0}},
		{name: "invalid wire type", msgType: TypePing, wire: []byte{0x0e}},
		{name: "overflowing tag", msgType: TypePing, wire: []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}},
		{name: "overflowing varint", msgType: TypePing, wire: append([]byte{0x08}, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}...)},
		{name: "truncated fixed32", msgType: TypePing, wire: []byte{0x15, 1, 2, 3}},
		{name: "truncated fixed64", msgType: TypePing, wire: []byte{0x11, 1, 2, 3, 4, 5, 6, 7}},
		{name: "truncated length", msgType: TypeTransactions, wire: []byte{0x0a, 0x02, 0x08}},
		{name: "malformed nested tag", msgType: TypeTransactions, wire: []byte{0x0a, 0x01, 0x00}},
		{name: "malformed nested fixed", msgType: TypeTransactions, wire: nestedFixed},
		{name: "unexpected end group", msgType: TypePing, wire: []byte{0x0c}},
		{name: "mismatched end group", msgType: TypePing, wire: []byte{0x0b, 0x14}},
		{name: "unterminated group", msgType: TypePing, wire: []byte{0x0b}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Preflight(test.msgType, test.wire)
			require.ErrorIs(t, err, ErrMalformedWire)
			require.NotErrorIs(t, err, ErrWireLimit)
		})
	}
}

func TestPreflightMalformedCapCrossingFieldIsMalformed(t *testing.T) {
	wire := repeatedMessageField(3, maxEndpoints, nil)
	wire = protowire.AppendTag(wire, 3, protowire.BytesType)
	wire = protowire.AppendVarint(wire, 4)
	wire = append(wire, 0)

	err := Preflight(TypeEndpoints, wire)
	require.ErrorIs(t, err, ErrMalformedWire)
	require.NotErrorIs(t, err, ErrWireLimit)
}

func nestedGroups(depth int) []byte {
	wire := make([]byte, 0, depth*2)
	for range depth {
		wire = protowire.AppendTag(wire, 1, protowire.StartGroupType)
	}
	for range depth {
		wire = protowire.AppendTag(wire, 1, protowire.EndGroupType)
	}
	return wire
}

func TestPreflightDepthLimitInclusive(t *testing.T) {
	require.NoError(t, Preflight(TypePing, nestedGroups(maxWireDepth-1)))
	requireWireLimit(t, Preflight(TypePing, nestedGroups(maxWireDepth)), WireLimitDepth, maxWireDepth, maxWireDepth+1)
}

func repeatedVarintFields(count int) []byte {
	wire := make([]byte, 0, count*3)
	for range count {
		wire = protowire.AppendTag(wire, 100, protowire.VarintType)
		wire = protowire.AppendVarint(wire, 0)
	}
	return wire
}

func TestPreflightFieldCountLimitInclusive(t *testing.T) {
	require.NoError(t, Preflight(TypePing, repeatedVarintFields(maxWireFieldCount)))
	requireWireLimit(t, Preflight(TypePing, repeatedVarintFields(maxWireFieldCount+1)), WireLimitFieldCount, maxWireFieldCount, maxWireFieldCount+1)
}

func TestPreflightUnknownBytesAreOpaque(t *testing.T) {
	opaque := append([]byte{0x00, 0x0b}, nestedGroups(maxWireDepth+1)...)
	wire := protowire.AppendTag(nil, 100, protowire.BytesType)
	wire = protowire.AppendBytes(wire, opaque)
	require.NoError(t, Preflight(TypePing, wire))
}

func TestPreflightKnownWrongWireTypeIsUnknownCompatible(t *testing.T) {
	wire := protowire.AppendTag(nil, 1, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 0)
	require.NoError(t, Preflight(TypeTransactions, wire))
	decoded, err := Decode(TypeTransactions, wire)
	require.NoError(t, err)
	require.Empty(t, decoded.(*Transactions).Transactions)
}

func TestPreflightValidInputAllocations(t *testing.T) {
	representative := repeatedMessageField(3, 16, nil)
	atCap := repeatedMessageField(1, maxTransactions, nil)

	var err error
	require.Zero(t, testing.AllocsPerRun(1_000, func() {
		err = Preflight(TypeEndpoints, representative)
	}))
	require.NoError(t, err)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		err = Preflight(TypeTransactions, atCap)
	}))
	require.NoError(t, err)
}

func TestDecodeClassifiesPreflightErrors(t *testing.T) {
	_, err := Decode(TypePing, []byte{0})
	require.ErrorIs(t, err, ErrMalformedWire)

	_, err = Decode(TypeEndpoints, repeatedMessageField(3, maxEndpoints+1, nil))
	requireWireLimit(t, err, WireLimitEndpoints, maxEndpoints, maxEndpoints+1)
}

func TestEncodeAppliesWireCollectionPolicy(t *testing.T) {
	_, err := Encode(&Endpoints{EndpointsV2: make([]Endpointv2, maxEndpoints)})
	require.NoError(t, err)
	_, err = Encode(&Endpoints{EndpointsV2: make([]Endpointv2, maxEndpoints+1)})
	requireWireLimit(t, err, WireLimitEndpoints, maxEndpoints, maxEndpoints+1)

	transactions := make([]Transaction, maxTransactions+1)
	for i := range transactions {
		transactions[i].Status = TxStatusNew
	}
	_, err = Encode(&Transactions{Transactions: transactions})
	requireWireLimit(t, err, WireLimitTransactions, maxTransactions, maxTransactions+1)

	_, err = Encode(&Manifests{List: make([]Manifest, maxManifests+1)})
	requireWireLimit(t, err, WireLimitManifests, maxManifests, maxManifests+1)
}

func TestMaximumAcceptedCollectionStructuralMemoryEnvelope(t *testing.T) {
	domainInput := &GetObjectByHash{
		ObjType: ObjectTypeUnknown,
		Objects: make([]IndexedObject, maxGetObjects),
	}
	wire, err := Encode(domainInput)
	require.NoError(t, err)

	generated := &peerproto.TMGetObjectByHash{}
	err = (pb.UnmarshalOptions{RecursionLimit: maxWireDepth, DiscardUnknown: true}).Unmarshal(wire, generated)
	require.NoError(t, err)
	require.Len(t, generated.Objects, maxGetObjects)

	decoded, err := Decode(TypeGetObjects, wire)
	require.NoError(t, err)
	domain := decoded.(*GetObjectByHash)
	require.Len(t, domain.Objects, maxGetObjects)

	const allocationHeader = uintptr(16)
	generatedObjectBytes := uintptr(len(generated.Objects)) *
		(unsafe.Sizeof(peerproto.TMIndexedObject{}) + allocationHeader)
	generatedSliceBytes := uintptr(cap(generated.Objects)) * unsafe.Sizeof((*peerproto.TMIndexedObject)(nil))
	domainSliceBytes := uintptr(cap(domain.Objects)) * unsafe.Sizeof(IndexedObject{})
	// A compressed frame can overlap its decompressed payload, an enum-filtered
	// copy, and protobuf-owned known bytes until domain conversion completes.
	maxWireBytes := uintptr(MaxPayloadSize + HeaderSizeCompressed)
	maxDecompressedBytes := uintptr(MaxMessageSize)
	maxNormalizedBytes := uintptr(MaxMessageSize)
	maxGeneratedKnownBytes := uintptr(MaxMessageSize)
	// Capacity slack and per-field overhead cover allocator rounding, byte-field
	// allocation metadata, optional scalar pointees, and protobuf bookkeeping.
	maxGeneratedCapacitySlack := maxGeneratedKnownBytes / 8
	maxPerFieldHeapOverhead := uintptr(maxWireFieldCount) * 64
	const maxTopLevelHeapOverhead = uintptr(1 << 20)
	peakStructuralBytes := maxWireBytes + maxDecompressedBytes + maxNormalizedBytes +
		maxGeneratedKnownBytes + maxGeneratedCapacitySlack + maxPerFieldHeapOverhead +
		maxTopLevelHeapOverhead + generatedObjectBytes + generatedSliceBytes + domainSliceBytes

	const envelope = uintptr(280 << 20)
	require.LessOrEqual(t, peakStructuralBytes, envelope)
	t.Logf("maximum accepted get-objects frame: wire=%d decompressed=%d normalized=%d generated=%d allocator=%d domain=%d peak envelope=%d/%d bytes",
		maxWireBytes, maxDecompressedBytes, maxNormalizedBytes,
		maxGeneratedKnownBytes+generatedObjectBytes+generatedSliceBytes,
		maxGeneratedCapacitySlack+maxPerFieldHeapOverhead+maxTopLevelHeapOverhead,
		domainSliceBytes, peakStructuralBytes, envelope)
}

func FuzzPreflight(f *testing.F) {
	f.Add(uint8(0), []byte{})
	f.Add(uint8(3), []byte{0})
	f.Add(uint8(7), nestedGroups(maxWireDepth))
	types := []MessageType{
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
	f.Fuzz(func(t *testing.T, selector uint8, wire []byte) {
		msgType := types[int(selector)%len(types)]
		first := Preflight(msgType, wire)
		second := Preflight(msgType, wire)
		if first == nil {
			require.NoError(t, second)
			return
		}
		require.Error(t, second)
		firstLimit := errors.Is(first, ErrWireLimit)
		firstMalformed := errors.Is(first, ErrMalformedWire)
		require.True(t, firstLimit || firstMalformed)
		require.Equal(t, firstLimit, errors.Is(second, ErrWireLimit))
		require.Equal(t, firstMalformed, errors.Is(second, ErrMalformedWire))
		if firstLimit {
			var firstDetail, secondDetail *WireLimitError
			require.ErrorAs(t, first, &firstDetail)
			require.ErrorAs(t, second, &secondDetail)
			require.Equal(t, firstDetail, secondDetail)
		}
	})
}

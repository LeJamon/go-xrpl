package message

import (
	"bytes"
	"errors"
	"math"
	"math/rand"
	"testing"
	"unsafe"

	wire "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
)

func TestLedgerDataFrameParity(t *testing.T) {
	depth := uint32(64)
	cases := map[string]*LedgerData{
		"base": {
			LedgerHash: bytes.Repeat([]byte{0xaa}, 32),
			LedgerSeq:  7,
			InfoType:   LedgerInfoBase,
		},
		"legacy node": {
			LedgerHash: bytes.Repeat([]byte{0xbb}, 32),
			LedgerSeq:  8,
			InfoType:   LedgerInfoAsNode,
			Nodes: []LedgerNode{{
				NodeData: []byte{1, 2, 3},
				NodeID:   bytes.Repeat([]byte{0x11}, 32),
			}},
		},
		"txset node": {
			LedgerHash: bytes.Repeat([]byte{0xcc}, 32),
			LedgerSeq:  9,
			InfoType:   LedgerInfoTsCandidate,
			Nodes: []LedgerNode{{
				NodeData: []byte{4, 5, 6},
				ID:       bytes.Repeat([]byte{0x22}, 32),
			}},
		},
		"depth node": {
			LedgerHash: bytes.Repeat([]byte{0xdd}, 32),
			LedgerSeq:  10,
			InfoType:   LedgerInfoAsNode,
			Nodes: []LedgerNode{{
				NodeData: []byte{7, 8, 9},
				Depth:    &depth,
			}},
		},
		"presence": {
			LedgerHash:       []byte{},
			LedgerSeq:        11,
			InfoType:         LedgerInfoTxNode,
			Nodes:            []LedgerNode{{NodeData: nil, NodeID: []byte{}, ID: []byte{}}},
			RequestCookieSet: true,
			Error:            ReplyErrorNoLedger,
			ErrorSet:         true,
		},
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			want, err := EncodeFrame(msg)
			if err != nil {
				t.Fatalf("EncodeFrame: %v", err)
			}
			gotSize, err := LedgerDataFrameSize(msg)
			if err != nil {
				t.Fatalf("LedgerDataFrameSize: %v", err)
			}
			if gotSize != len(want) {
				t.Fatalf("frame size = %d, EncodeFrame length = %d", gotSize, len(want))
			}
			got, err := EncodeLedgerDataFrame(msg, len(want))
			if err != nil {
				t.Fatalf("EncodeLedgerDataFrame: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("frame differs from EncodeFrame:\n got  %x\n want %x", got, want)
			}
			if len(got) != cap(got) {
				t.Fatalf("frame capacity = %d, length = %d; MarshalAppend allocated beyond exact frame", cap(got), len(got))
			}
		})
	}
}

func TestLedgerDataFrameSizeIsAllocationFree(t *testing.T) {
	msg := &LedgerData{
		LedgerHash: bytes.Repeat([]byte{1}, 32),
		LedgerSeq:  3,
		InfoType:   LedgerInfoAsNode,
		Nodes: []LedgerNode{{
			NodeData: bytes.Repeat([]byte{2}, 128),
			Depth:    func() *uint32 { value := uint32(4); return &value }(),
		}},
	}
	want, err := LedgerDataFrameSize(msg)
	if err != nil {
		t.Fatalf("LedgerDataFrameSize: %v", err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		got, err := LedgerDataFrameSize(msg)
		if err != nil || got != want {
			t.Fatalf("LedgerDataFrameSize = (%d, %v), want (%d, nil)", got, err, want)
		}
	})
	if allocs != 0 {
		t.Fatalf("LedgerDataFrameSize allocations = %v, want 0", allocs)
	}
}

func TestLedgerDataFrameRejectsBeforeEncoding(t *testing.T) {
	msg := &LedgerData{
		LedgerHash: bytes.Repeat([]byte{1}, 32),
		LedgerSeq:  3,
		InfoType:   LedgerInfoAsNode,
		Nodes:      []LedgerNode{{NodeData: bytes.Repeat([]byte{2}, 128)}},
	}
	frameSize, err := LedgerDataFrameSize(msg)
	if err != nil {
		t.Fatalf("LedgerDataFrameSize: %v", err)
	}
	for _, maxFrameBytes := range []int{frameSize - 1, 0, -1} {
		got, err := EncodeLedgerDataFrame(msg, maxFrameBytes)
		if got != nil {
			t.Fatalf("maxFrameBytes=%d returned a frame", maxFrameBytes)
		}
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("maxFrameBytes=%d error = %v, want ErrMessageTooLarge", maxFrameBytes, err)
		}
	}
}

func TestLedgerDataFrameRejectsNilMessage(t *testing.T) {
	var msg *LedgerData
	if _, err := LedgerDataFrameSize(msg); !errors.Is(err, ErrNilMessage) {
		t.Fatalf("LedgerDataFrameSize(nil) error = %v, want ErrNilMessage", err)
	}
	if got, err := EncodeLedgerDataFrame(msg, MaxMessageSize+HeaderSizeUncompressed); got != nil || !errors.Is(err, ErrNilMessage) {
		t.Fatalf("EncodeLedgerDataFrame(nil) = (%v, %v), want nil frame and ErrNilMessage", got, err)
	}
}

func TestLedgerDataFrameRejectsInvalidMessageBeforeAllocation(t *testing.T) {
	depth := uint32(2)
	cases := map[string]*LedgerData{
		"too many nodes": {
			InfoType: LedgerInfoBase,
			Nodes:    make([]LedgerNode, maxLedgerDataNodes+1),
		},
		"invalid required enum": {
			InfoType: LedgerInfoType(99),
		},
		"invalid optional enum": {
			InfoType: LedgerInfoBase,
			ErrorSet: true,
		},
		"both oneof alternatives": {
			InfoType: LedgerInfoBase,
			Nodes:    []LedgerNode{{ID: []byte{1}, Depth: &depth}},
		},
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LedgerDataFrameSize(msg); err == nil {
				t.Fatal("LedgerDataFrameSize succeeded for invalid message")
			}
			if got, err := EncodeLedgerDataFrame(msg, MaxMessageSize+HeaderSizeUncompressed); got != nil || err == nil {
				t.Fatalf("EncodeLedgerDataFrame = (%v, %v), want nil frame and error", got, err)
			}
		})
	}
}

func TestLedgerDataFrameProtocolSizeBoundaries(t *testing.T) {
	maxPayload := uint64(MaxPayloadSize)
	frameSize, err := ledgerDataFrameSizeForPayload(maxPayload)
	if err != nil {
		t.Fatalf("maximum payload rejected: %v", err)
	}
	if want := MaxPayloadSize + HeaderSizeUncompressed; frameSize != want {
		t.Fatalf("maximum frame size = %d, want %d", frameSize, want)
	}
	for _, payloadSize := range []uint64{maxPayload + 1, math.MaxUint64} {
		if _, err := ledgerDataFrameSizeForPayload(payloadSize); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("payload size %d error = %v, want ErrMessageTooLarge", payloadSize, err)
		}
	}
	if _, err := addLedgerDataBytesSize(0, 1, math.MaxUint64); err == nil {
		t.Fatal("overflowing bytes field size was accepted")
	}
	if _, err := addLedgerDataSize(math.MaxUint64, 1); err == nil {
		t.Fatal("overflowing aggregate size was accepted")
	}
}

func TestLedgerDataEncodingWorkspaceBytesBounds(t *testing.T) {
	maxOneof := maxUintptr(unsafe.Sizeof(wire.TMLedgerNode_Id{}), unsafe.Sizeof(wire.TMLedgerNode_Depth{}))
	perNode := unsafe.Sizeof(wire.TMLedgerNode{}) + maxOneof + unsafe.Sizeof((*wire.TMLedgerNode)(nil))
	if uint64(perNode) > ledgerDataWorkspacePerNode {
		t.Fatalf("per-node generated wrapper storage = %d, estimate = %d", perNode, ledgerDataWorkspacePerNode)
	}
	outer := unsafe.Sizeof(wire.TMLedgerData{})
	scalarPointers := 3*unsafe.Sizeof(uint32(0)) + unsafe.Sizeof(wire.TMLedgerInfoType(0)) + unsafe.Sizeof(wire.TMReplyError(0))
	if uint64(outer+scalarPointers) > ledgerDataWorkspaceBase {
		t.Fatalf("outer generated wrapper storage = %d, estimate = %d", outer+scalarPointers, ledgerDataWorkspaceBase)
	}
}

func TestLedgerDataFrameRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(2079))
	for iteration := 0; iteration < 256; iteration++ {
		msg := randomLedgerData(rng)
		want, err := EncodeFrame(msg)
		if err != nil {
			t.Fatalf("iteration %d EncodeFrame: %v", iteration, err)
		}
		gotSize, err := LedgerDataFrameSize(msg)
		if err != nil {
			t.Fatalf("iteration %d LedgerDataFrameSize: %v", iteration, err)
		}
		if gotSize != len(want) {
			t.Fatalf("iteration %d frame size = %d, want %d", iteration, gotSize, len(want))
		}
		got, err := EncodeLedgerDataFrame(msg, len(want))
		if err != nil {
			t.Fatalf("iteration %d EncodeLedgerDataFrame: %v", iteration, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("iteration %d frame differs from EncodeFrame", iteration)
		}
	}
}

func FuzzLedgerDataFrameParity(f *testing.F) {
	f.Add(uint8(0), uint32(0), uint8(0), uint8(0), uint8(0), uint32(0), false, uint8(0), false)
	f.Add(uint8(32), uint32(600000), uint8(2), uint8(4), uint8(1), uint32(17), true, uint8(1), true)
	f.Add(uint8(255), uint32(math.MaxUint32), uint8(3), uint8(8), uint8(2), uint32(math.MaxUint32), true, uint8(3), true)
	f.Fuzz(func(t *testing.T, hashLen uint8, ledgerSeq uint32, infoType uint8, nodeCount uint8, reference uint8, cookie uint32, cookieSet bool, replyError uint8, errorSet bool) {
		msg := fuzzLedgerData(hashLen, ledgerSeq, infoType, nodeCount, reference, cookie, cookieSet, replyError, errorSet)
		want, err := EncodeFrame(msg)
		if err != nil {
			t.Fatalf("EncodeFrame: %v", err)
		}
		gotSize, err := LedgerDataFrameSize(msg)
		if err != nil {
			t.Fatalf("LedgerDataFrameSize: %v", err)
		}
		if gotSize != len(want) {
			t.Fatalf("frame size = %d, want %d", gotSize, len(want))
		}
		got, err := EncodeLedgerDataFrame(msg, len(want))
		if err != nil {
			t.Fatalf("EncodeLedgerDataFrame: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame differs from EncodeFrame")
		}
	})
}

func randomLedgerData(rng *rand.Rand) *LedgerData {
	msg := fuzzLedgerData(
		uint8(rng.Intn(64)),
		rng.Uint32(),
		uint8(rng.Intn(4)),
		uint8(rng.Intn(8)),
		uint8(rng.Intn(5)),
		rng.Uint32(),
		rng.Intn(2) == 0,
		uint8(rng.Intn(4)),
		rng.Intn(2) == 0,
	)
	return msg
}

func fuzzLedgerData(hashLen uint8, ledgerSeq uint32, infoType uint8, nodeCount uint8, reference uint8, cookie uint32, cookieSet bool, replyError uint8, errorSet bool) *LedgerData {
	msg := &LedgerData{
		LedgerHash: bytes.Repeat([]byte{0xaa}, int(hashLen)),
		LedgerSeq:  ledgerSeq,
		InfoType:   LedgerInfoType(infoType % 4),
		Nodes:      make([]LedgerNode, int(nodeCount%8)),
	}
	if cookieSet {
		msg.RequestCookieSet = true
		msg.RequestCookie = cookie
	}
	if errorSet {
		msg.ErrorSet = true
		msg.Error = ReplyError(replyError%3 + 1)
	}
	for i := range msg.Nodes {
		dataLen := (int(hashLen) + i*17) % 257
		msg.Nodes[i].NodeData = bytes.Repeat([]byte{byte(i + 1)}, dataLen)
		switch reference % 5 {
		case 0:
			msg.Nodes[i].NodeID = []byte{}
		case 1:
			msg.Nodes[i].NodeID = bytes.Repeat([]byte{byte(i + 2)}, 32)
		case 2:
			msg.Nodes[i].ID = []byte{}
		case 3:
			msg.Nodes[i].ID = bytes.Repeat([]byte{byte(i + 3)}, 32)
		default:
			depth := uint32(i) * 8
			msg.Nodes[i].Depth = &depth
		}
	}
	return msg
}

func maxUintptr(values ...uintptr) uintptr {
	var max uintptr
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

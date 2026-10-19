package message

import (
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

const (
	// These values deliberately include allocator slack for the generated
	// protobuf message wrappers. The caller's LedgerData value, node slice,
	// referenced node bytes, and output frame are accounted for separately.
	ledgerDataWorkspaceBase       = uint64(512)
	ledgerDataWorkspacePerNode    = uint64(192)
	ledgerDataWorkspaceSaturating = uint64(math.MaxUint64)
)

// LedgerDataEncodingWorkspaceBytes returns a conservative estimate for the
// generated protobuf wrappers needed to encode nodeCount LedgerData nodes.
// It includes the generated outer and per-node messages, oneof wrappers, and
// the generated node-pointer slice. It excludes the caller's LedgerData and
// node slice, referenced node bytes, and the output frame.
func LedgerDataEncodingWorkspaceBytes(nodeCount int) uint64 {
	if nodeCount <= 0 {
		return ledgerDataWorkspaceBase
	}
	count := uint64(nodeCount)
	if count > (ledgerDataWorkspaceSaturating-ledgerDataWorkspaceBase)/ledgerDataWorkspacePerNode {
		return ledgerDataWorkspaceSaturating
	}
	return ledgerDataWorkspaceBase + count*ledgerDataWorkspacePerNode
}

// LedgerDataFrameSize returns the exact uncompressed wire-frame size for m.
// It performs the LedgerData node and protocol frame checks without creating
// generated protobuf wrappers or an output buffer.
func LedgerDataFrameSize(m *LedgerData) (int, error) {
	payloadSize, err := ledgerDataPayloadSize(m)
	if err != nil {
		return 0, err
	}
	return ledgerDataFrameSizeForPayload(payloadSize)
}

func ledgerDataFrameSizeForPayload(payloadSize uint64) (int, error) {
	if payloadSize > uint64(MaxPayloadSize) || payloadSize > uint64(MaxPayloadSizeForType(TypeLedgerData)) {
		return 0, fmt.Errorf("%w: LedgerData payload size %d", ErrMessageTooLarge, payloadSize)
	}
	header, err := newHeader(uint32(payloadSize), TypeLedgerData, AlgorithmNone, 0)
	if err != nil {
		return 0, err
	}
	return header.TotalSize(), nil
}

// EncodeLedgerDataFrame encodes m as an uncompressed LedgerData frame. The
// maxFrameBytes limit is checked before generated protobuf wrappers or the
// output frame are allocated. A non-positive limit therefore rejects every
// non-empty frame; callers that do not have an additional limit should pass
// the protocol maximum frame size.
func EncodeLedgerDataFrame(m *LedgerData, maxFrameBytes int) ([]byte, error) {
	frameSize, err := LedgerDataFrameSize(m)
	if err != nil {
		return nil, err
	}
	if frameSize > maxFrameBytes {
		return nil, fmt.Errorf("%w: LedgerData frame size %d exceeds limit %d", ErrMessageTooLarge, frameSize, maxFrameBytes)
	}

	codec, ok := codecs[TypeLedgerData]
	if !ok {
		return nil, fmt.Errorf("unknown message type: %d", TypeLedgerData)
	}
	pmsg, err := codec.encode(m)
	if err != nil {
		return nil, err
	}
	if err := validateEnums(pmsg.ProtoReflect(), false); err != nil {
		return nil, err
	}
	payloadSize := pb.Size(pmsg)
	if payloadSize != frameSize-HeaderSizeUncompressed {
		return nil, fmt.Errorf(
			"LedgerData size mismatch: measured %d, preflighted %d",
			payloadSize,
			frameSize-HeaderSizeUncompressed,
		)
	}
	header, err := newHeader(uint32(payloadSize), TypeLedgerData, AlgorithmNone, 0)
	if err != nil {
		return nil, err
	}

	frame := make([]byte, HeaderSizeUncompressed, frameSize)
	encodeHeader(frame, header)
	frame, err = (pb.MarshalOptions{}).MarshalAppend(frame, pmsg)
	if err != nil {
		return nil, err
	}
	if len(frame) != frameSize {
		return nil, fmt.Errorf("LedgerData frame size mismatch: encoded %d, expected %d", len(frame), frameSize)
	}
	if err := Preflight(TypeLedgerData, frame[HeaderSizeUncompressed:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func ledgerDataPayloadSize(m *LedgerData) (uint64, error) {
	if m == nil {
		return 0, ErrNilMessage
	}
	if len(m.Nodes) > maxLedgerDataNodes {
		return 0, wireLimit(WireLimitLedgerData, maxLedgerDataNodes, len(m.Nodes))
	}
	if !validLedgerInfoType(m.InfoType) {
		return 0, fmt.Errorf("invalid required enum protocol.TMLedgerData.type: %d", m.InfoType)
	}
	if m.HasError() && !validReplyError(m.Error) {
		return 0, fmt.Errorf("invalid optional enum protocol.TMLedgerData.error: %d", m.Error)
	}

	var size uint64
	var err error
	if size, err = addLedgerDataBytesField(size, protowire.Number(1), len(m.LedgerHash)); err != nil {
		return 0, err
	}
	if size, err = addLedgerDataVarintField(size, protowire.Number(2), uint64(m.LedgerSeq)); err != nil {
		return 0, err
	}
	if size, err = addLedgerDataVarintField(size, protowire.Number(3), uint64(m.InfoType)); err != nil {
		return 0, err
	}
	for _, n := range m.Nodes {
		if n.ID != nil && n.Depth != nil {
			return 0, fmt.Errorf("ledger node has both id and depth")
		}
		nodeSize := uint64(0)
		if nodeSize, err = addLedgerDataBytesField(nodeSize, protowire.Number(1), len(n.NodeData)); err != nil {
			return 0, err
		}
		if n.NodeID != nil {
			if nodeSize, err = addLedgerDataBytesField(nodeSize, protowire.Number(2), len(n.NodeID)); err != nil {
				return 0, err
			}
		}
		if n.ID != nil {
			if nodeSize, err = addLedgerDataBytesField(nodeSize, protowire.Number(3), len(n.ID)); err != nil {
				return 0, err
			}
		} else if n.Depth != nil {
			if nodeSize, err = addLedgerDataVarintField(nodeSize, protowire.Number(4), uint64(*n.Depth)); err != nil {
				return 0, err
			}
		}
		if size, err = addLedgerDataBytesSize(size, protowire.Number(4), nodeSize); err != nil {
			return 0, err
		}
	}
	if m.HasRequestCookie() {
		if size, err = addLedgerDataVarintField(size, protowire.Number(5), uint64(m.RequestCookie)); err != nil {
			return 0, err
		}
	}
	if m.HasError() {
		if size, err = addLedgerDataVarintField(size, protowire.Number(6), uint64(m.Error)); err != nil {
			return 0, err
		}
	}
	return size, nil
}

func addLedgerDataBytesField(total uint64, number protowire.Number, length int) (uint64, error) {
	if length < 0 {
		return 0, fmt.Errorf("negative LedgerData field length: %d", length)
	}
	return addLedgerDataBytesSize(total, number, uint64(length))
}

func addLedgerDataBytesSize(total uint64, number protowire.Number, length uint64) (uint64, error) {
	tagSize := uint64(protowire.SizeTag(number))
	lengthSize := uint64(protowire.SizeVarint(length))
	if length > math.MaxUint64-tagSize-lengthSize {
		return 0, fmt.Errorf("LedgerData field size overflows uint64")
	}
	fieldSize := tagSize + lengthSize + length
	return addLedgerDataSize(total, fieldSize)
}

func addLedgerDataVarintField(total uint64, number protowire.Number, value uint64) (uint64, error) {
	fieldSize := uint64(protowire.SizeTag(number)) + uint64(protowire.SizeVarint(value))
	return addLedgerDataSize(total, fieldSize)
}

func addLedgerDataSize(total, addition uint64) (uint64, error) {
	if addition > math.MaxUint64-total {
		return 0, fmt.Errorf("LedgerData size overflows uint64")
	}
	return total + addition, nil
}

func validLedgerInfoType(value LedgerInfoType) bool {
	switch value {
	case LedgerInfoBase, LedgerInfoTxNode, LedgerInfoAsNode, LedgerInfoTsCandidate:
		return true
	default:
		return false
	}
}

func validReplyError(value ReplyError) bool {
	switch value {
	case ReplyErrorNoLedger, ReplyErrorNoNode, ReplyErrorBadRequest:
		return true
	default:
		return false
	}
}

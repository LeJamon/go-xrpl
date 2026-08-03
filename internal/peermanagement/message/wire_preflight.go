package message

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	maxWireDepth       = 16
	maxWireFieldCount  = 131_072
	maxTransactions    = 10_000
	maxGetObjects      = 12_288
	maxLedgerDataNodes = 12_288
	maxEndpoints       = 1_023
	maxValidatorBlobs  = 5
	maxManifests       = 100
	maxClusterNodes    = 1_024
	maxLoadSources     = 1_024
)

var (
	ErrWireLimit     = errors.New("protobuf wire resource limit")
	ErrMalformedWire = errors.New("malformed protobuf wire")
)

type WireLimitReason string

const (
	WireLimitDepth          WireLimitReason = "nesting-depth"
	WireLimitFieldCount     WireLimitReason = "field-count"
	WireLimitTransactions   WireLimitReason = "transactions"
	WireLimitGetObjects     WireLimitReason = "get-objects"
	WireLimitLedgerData     WireLimitReason = "ledger-data-nodes"
	WireLimitEndpoints      WireLimitReason = "endpoints"
	WireLimitValidatorBlobs WireLimitReason = "validator-blobs"
	WireLimitManifests      WireLimitReason = "manifests"
	WireLimitClusterNodes   WireLimitReason = "cluster-nodes"
	WireLimitLoadSources    WireLimitReason = "load-sources"
)

type WireLimitError struct {
	Reason   WireLimitReason
	Limit    int
	Observed int
}

func (e *WireLimitError) Error() string {
	return fmt.Sprintf("%s: %s: got %d, limit %d", ErrWireLimit, e.Reason, e.Observed, e.Limit)
}

func (e *WireLimitError) Unwrap() error {
	return ErrWireLimit
}

var wireDescriptors = func() map[MessageType]protoreflect.MessageDescriptor {
	descriptors := make(map[MessageType]protoreflect.MessageDescriptor, len(codecs))
	for msgType, codec := range codecs {
		descriptors[msgType] = codec.newProto().ProtoReflect().Descriptor()
	}
	return descriptors
}()

type wireScanState struct {
	msgType               MessageType
	fields                int
	collection            int
	loadSources           int
	getObjects            int
	getObjectTransactions bool
}

func Preflight(msgType MessageType, data []byte) error {
	descriptor, ok := wireDescriptors[msgType]
	if !ok {
		return fmt.Errorf("unknown message type: %d", msgType)
	}
	state := wireScanState{msgType: msgType}
	consumed, err := state.scanMessage(data, descriptor, 1, 0)
	if err != nil {
		return err
	}
	if consumed != len(data) {
		return malformedWire(consumed, "unexpected end group")
	}
	if msgType == TypeGetObjects {
		limit := maxGetObjects
		if state.getObjectTransactions {
			limit = maxTransactions
		}
		if state.getObjects > limit {
			return wireLimit(WireLimitGetObjects, limit, state.getObjects)
		}
	}
	return nil
}

func (s *wireScanState) scanMessage(
	data []byte,
	descriptor protoreflect.MessageDescriptor,
	depth int,
	endGroup protowire.Number,
) (int, error) {
	for offset := 0; offset < len(data); {
		fieldOffset := offset
		number, wireType, tagLen := protowire.ConsumeTag(data[offset:])
		if tagLen < 0 || number < protowire.MinValidNumber || number > protowire.MaxValidNumber {
			return 0, malformedWire(fieldOffset, "invalid tag")
		}
		offset += tagLen

		if wireType == protowire.EndGroupType {
			if endGroup == 0 || number != endGroup {
				return 0, malformedWire(fieldOffset, "unexpected end group")
			}
			return offset, nil
		}

		s.fields++
		if s.fields > maxWireFieldCount {
			return 0, wireLimit(WireLimitFieldCount, maxWireFieldCount, s.fields)
		}

		var field protoreflect.FieldDescriptor
		if descriptor != nil {
			field = descriptor.Fields().ByNumber(number)
		}
		value := data[offset:]
		valueLen, err := s.scanValue(value, field, number, wireType, depth)
		if err != nil {
			return 0, malformedWireAt(err, fieldOffset)
		}
		if depth == 1 {
			if err := s.observeRootField(field, wireType, value); err != nil {
				return 0, malformedWireAt(err, fieldOffset)
			}
		}
		offset += valueLen
	}
	if endGroup != 0 {
		return 0, malformedWire(len(data), "unterminated group")
	}
	return len(data), nil
}

func (s *wireScanState) scanValue(
	data []byte,
	field protoreflect.FieldDescriptor,
	number protowire.Number,
	wireType protowire.Type,
	depth int,
) (int, error) {
	switch wireType {
	case protowire.VarintType:
		_, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return 0, protowire.ParseError(n)
		}
		return n, nil
	case protowire.Fixed32Type:
		if len(data) < 4 {
			return 0, errors.New("truncated fixed32")
		}
		return 4, nil
	case protowire.Fixed64Type:
		if len(data) < 8 {
			return 0, errors.New("truncated fixed64")
		}
		return 8, nil
	case protowire.BytesType:
		value, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return 0, protowire.ParseError(n)
		}
		if field != nil && field.Kind() == protoreflect.MessageKind {
			if depth >= maxWireDepth {
				return 0, wireLimit(WireLimitDepth, maxWireDepth, depth+1)
			}
			if _, err := s.scanMessage(value, field.Message(), depth+1, 0); err != nil {
				return 0, err
			}
		}
		return n, nil
	case protowire.StartGroupType:
		if depth >= maxWireDepth {
			return 0, wireLimit(WireLimitDepth, maxWireDepth, depth+1)
		}
		var nested protoreflect.MessageDescriptor
		if field != nil && field.Kind() == protoreflect.GroupKind {
			nested = field.Message()
		}
		return s.scanMessage(data, nested, depth+1, number)
	default:
		return 0, errors.New("invalid wire type")
	}
}

func (s *wireScanState) observeRootField(
	field protoreflect.FieldDescriptor,
	wireType protowire.Type,
	data []byte,
) error {
	if field == nil {
		return nil
	}
	if s.msgType == TypeGetObjects && field.Number() == 1 &&
		field.Kind() == protoreflect.EnumKind && wireType == protowire.VarintType {
		value, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		number := protoreflect.EnumNumber(value)
		if field.Enum().Values().ByNumber(number) != nil {
			s.getObjectTransactions = number == protoreflect.EnumNumber(proto.TMGetObjectByHash_otTRANSACTIONS)
		}
	}
	if wireType != protowire.BytesType || !field.IsList() || field.Kind() != protoreflect.MessageKind {
		return nil
	}

	var reason WireLimitReason
	limit := 0
	switch s.msgType {
	case TypeTransactions:
		if field.Number() == 1 {
			reason, limit = WireLimitTransactions, maxTransactions
		}
	case TypeGetObjects:
		if field.Number() == 6 {
			s.getObjects++
			return nil
		}
	case TypeLedgerData:
		if field.Number() == 4 {
			reason, limit = WireLimitLedgerData, maxLedgerDataNodes
		}
	case TypeEndpoints:
		if field.Number() == 3 {
			reason, limit = WireLimitEndpoints, maxEndpoints
		}
	case TypeValidatorListCollection:
		if field.Number() == 3 {
			reason, limit = WireLimitValidatorBlobs, maxValidatorBlobs
		}
	case TypeManifests:
		if field.Number() == 1 {
			reason, limit = WireLimitManifests, maxManifests
		}
	case TypeCluster:
		switch field.Number() {
		case 1:
			reason, limit = WireLimitClusterNodes, maxClusterNodes
		case 2:
			reason, limit = WireLimitLoadSources, maxLoadSources
		}
	}
	if limit == 0 {
		return nil
	}
	count := s.collectionCount(reason) + 1
	s.setCollectionCount(reason, count)
	if count > limit {
		return wireLimit(reason, limit, count)
	}
	return nil
}

func (s *wireScanState) collectionCount(reason WireLimitReason) int {
	if reason == WireLimitLoadSources {
		return s.loadSources
	}
	return s.collection
}

func (s *wireScanState) setCollectionCount(reason WireLimitReason, count int) {
	if reason == WireLimitLoadSources {
		s.loadSources = count
		return
	}
	s.collection = count
}

func wireLimit(reason WireLimitReason, limit, observed int) error {
	return &WireLimitError{Reason: reason, Limit: limit, Observed: observed}
}

func malformedWire(offset int, reason string) error {
	return fmt.Errorf("%w at byte %d: %s", ErrMalformedWire, offset, reason)
}

func malformedWireAt(err error, offset int) error {
	if errors.Is(err, ErrWireLimit) || errors.Is(err, ErrMalformedWire) {
		return err
	}
	return malformedWire(offset, err.Error())
}

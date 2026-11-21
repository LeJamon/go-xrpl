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
	maxGetLedgerNodes  = 12_288
	maxLedgerDataNodes = 12_288
	maxEndpoints       = 1_023
	maxValidatorBlobs  = 5
	maxClusterNodes    = 1_024
	maxLoadSources     = 1_024
)

var (
	ErrWireLimit            = errors.New("protobuf wire resource limit")
	ErrMalformedWire        = errors.New("malformed protobuf wire")
	ErrMissingRequiredField = errors.New("protobuf required field missing")
)

type WireLimitReason string

const (
	WireLimitDepth                 WireLimitReason = "nesting-depth"
	WireLimitFieldCount            WireLimitReason = "field-count"
	WireLimitTransactions          WireLimitReason = "transactions"
	WireLimitGetObjects            WireLimitReason = "get-objects"
	WireLimitGetObjectTransactions WireLimitReason = "get-object-transactions"
	WireLimitGetLedger             WireLimitReason = "get-ledger-nodes"
	WireLimitLedgerData            WireLimitReason = "ledger-data-nodes"
	WireLimitEndpoints             WireLimitReason = "endpoints"
	WireLimitValidatorBlobs        WireLimitReason = "validator-blobs"
	WireLimitClusterNodes          WireLimitReason = "cluster-nodes"
	WireLimitLoadSources           WireLimitReason = "load-sources"
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

// collectionLimit bounds the element count of one repeated root message
// field.
type collectionLimit struct {
	field  protowire.Number
	reason WireLimitReason
	max    int
}

// maxCollectionLimits is the most collection limits one type may declare.
const maxCollectionLimits = 2

var wireDescriptors = func() (descriptors [len(codecs)]protoreflect.MessageDescriptor) {
	for msgType, codec := range codecs {
		if codec != nil {
			descriptors[msgType] = codec.newProto().ProtoReflect().Descriptor()
		}
	}
	return descriptors
}()

type wireScanState struct {
	msgType               MessageType
	collections           []collectionLimit
	collectionCounts      [maxCollectionLimits]int
	deferEndpointLimit    bool
	endpointCount         int
	validateRequired      bool
	fields                int
	getObjects            int
	getObjectTransactions bool
	getLedgerNodes        int
	getLedgerInfoType     protoreflect.EnumNumber
}

func Preflight(msgType MessageType, data []byte) error {
	_, err := scanWirePayload(msgType, data, false, false)
	return err
}

func validateWirePayload(msgType MessageType, data []byte) error {
	_, err := scanWirePayload(msgType, data, true, false)
	return err
}

type wirePayloadInfo struct {
	endpointCount int
}

func scanWirePayload(
	msgType MessageType,
	data []byte,
	validateRequired bool,
	deferEndpointLimit bool,
) (wirePayloadInfo, error) {
	codec, ok := codecFor(msgType)
	if !ok {
		return wirePayloadInfo{}, fmt.Errorf("unknown message type: %d", msgType)
	}
	descriptor := wireDescriptors[msgType]
	state := wireScanState{
		msgType:            msgType,
		collections:        codec.collections,
		deferEndpointLimit: deferEndpointLimit,
		validateRequired:   validateRequired,
	}
	consumed, err := state.scanMessage(data, descriptor, 1, 0)
	if err != nil {
		return wirePayloadInfo{}, err
	}
	if consumed != len(data) {
		return wirePayloadInfo{}, malformedWire(consumed, "unexpected end group")
	}
	if msgType == TypeGetObjects {
		limit := maxGetObjects
		reason := WireLimitGetObjects
		if state.getObjectTransactions {
			limit = maxTransactions
			reason = WireLimitGetObjectTransactions
		}
		if state.getObjects > limit {
			return wirePayloadInfo{}, wireLimit(reason, limit, state.getObjects)
		}
	}
	if msgType == TypeGetLedger && state.getLedgerInfoType != protoreflect.EnumNumber(proto.TMLedgerInfoType_liBASE) &&
		state.getLedgerNodes > maxGetLedgerNodes {
		return wirePayloadInfo{}, wireLimit(WireLimitGetLedger, maxGetLedgerNodes, state.getLedgerNodes)
	}
	return wirePayloadInfo{endpointCount: state.endpointCount}, nil
}

func (s *wireScanState) scanMessage(
	data []byte,
	descriptor protoreflect.MessageDescriptor,
	depth int,
	endGroup protowire.Number,
) (int, error) {
	required, err := newRequiredFieldSet(descriptor, s.validateRequired)
	if err != nil {
		return 0, err
	}
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
			if err := required.check(); err != nil {
				return 0, err
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
		required.observe(field, wireType, value)
		offset += valueLen
	}
	if endGroup != 0 {
		return 0, malformedWire(len(data), "unterminated group")
	}
	if err := required.check(); err != nil {
		return 0, err
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
	if (s.msgType == TypeGetObjects || s.msgType == TypeGetLedger) && field.Number() == 1 &&
		field.Kind() == protoreflect.EnumKind && wireType == protowire.VarintType {
		value, n := protowire.ConsumeVarint(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		number := protoreflect.EnumNumber(value)
		if field.Enum().Values().ByNumber(number) != nil {
			switch s.msgType {
			case TypeGetObjects:
				s.getObjectTransactions = number == protoreflect.EnumNumber(proto.TMGetObjectByHash_otTRANSACTIONS)
			case TypeGetLedger:
				s.getLedgerInfoType = number
			}
		}
	}
	if s.msgType == TypeGetLedger && field.Number() == 5 && wireType == protowire.BytesType {
		s.getLedgerNodes++
	}
	if wireType != protowire.BytesType || !field.IsList() || field.Kind() != protoreflect.MessageKind {
		return nil
	}

	// The GetObjects object count is bounded after the scan because its
	// limit depends on the object type, which may follow the objects.
	if s.msgType == TypeGetObjects && field.Number() == 6 {
		s.getObjects++
		return nil
	}
	for i, limit := range s.collections {
		if limit.field != field.Number() {
			continue
		}
		s.collectionCounts[i]++
		if count := s.collectionCounts[i]; count > limit.max {
			if limit.reason == WireLimitEndpoints && s.deferEndpointLimit {
				s.endpointCount = count
				return nil
			}
			return wireLimit(limit.reason, limit.max, count)
		}
		if limit.reason == WireLimitEndpoints {
			s.endpointCount = s.collectionCounts[i]
		}
		return nil
	}
	return nil
}

type requiredFieldSet struct {
	descriptor protoreflect.MessageDescriptor
	numbers    protoreflect.FieldNumbers
	seen       uint64
}

func newRequiredFieldSet(descriptor protoreflect.MessageDescriptor, enabled bool) (requiredFieldSet, error) {
	if !enabled || descriptor == nil {
		return requiredFieldSet{}, nil
	}
	numbers := descriptor.RequiredNumbers()
	if numbers.Len() > 64 {
		return requiredFieldSet{}, fmt.Errorf("message %s declares %d required fields; wire admission supports at most 64", descriptor.FullName(), numbers.Len())
	}
	return requiredFieldSet{descriptor: descriptor, numbers: numbers}, nil
}

func (r *requiredFieldSet) observe(
	field protoreflect.FieldDescriptor,
	wireType protowire.Type,
	data []byte,
) {
	if field == nil || r.numbers == nil || field.Cardinality() != protoreflect.Required {
		return
	}
	for i := 0; i < r.numbers.Len(); i++ {
		required := r.descriptor.Fields().ByNumber(r.numbers.Get(i))
		if required.Number() != field.Number() || fieldWireType(required) != wireType {
			continue
		}
		if required.Kind() == protoreflect.EnumKind {
			value, n := protowire.ConsumeVarint(data)
			if n < 0 || required.Enum().Values().ByNumber(protoreflect.EnumNumber(value)) == nil {
				return
			}
		}
		r.seen |= uint64(1) << uint(i)
		return
	}
}

func (r *requiredFieldSet) check() error {
	if r.numbers == nil {
		return nil
	}
	for i := 0; i < r.numbers.Len(); i++ {
		if r.seen&(uint64(1)<<uint(i)) == 0 {
			field := r.descriptor.Fields().ByNumber(r.numbers.Get(i))
			return fmt.Errorf("%w: %s", ErrMissingRequiredField, field.FullName())
		}
	}
	return nil
}

func fieldWireType(field protoreflect.FieldDescriptor) protowire.Type {
	switch field.Kind() {
	case protoreflect.BoolKind,
		protoreflect.EnumKind,
		protoreflect.Int32Kind,
		protoreflect.Int64Kind,
		protoreflect.Sint32Kind,
		protoreflect.Sint64Kind,
		protoreflect.Uint32Kind,
		protoreflect.Uint64Kind:
		return protowire.VarintType
	case protoreflect.Fixed32Kind,
		protoreflect.FloatKind,
		protoreflect.Sfixed32Kind:
		return protowire.Fixed32Type
	case protoreflect.Fixed64Kind,
		protoreflect.DoubleKind,
		protoreflect.Sfixed64Kind:
		return protowire.Fixed64Type
	case protoreflect.StringKind,
		protoreflect.BytesKind,
		protoreflect.MessageKind:
		return protowire.BytesType
	case protoreflect.GroupKind:
		return protowire.StartGroupType
	default:
		return protowire.Type(-1)
	}
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

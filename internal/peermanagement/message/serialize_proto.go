package message

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// msgCodec bundles the three operations every message type needs:
// proto constructor, encoder, decoder. A missing table entry surfaces
// as a single "unknown message type" at both encode and decode.
type msgCodec struct {
	newProto func() pb.Message
	encode   func(Message) (pb.Message, error)
	decode   func(pb.Message) (Message, error)
}

// assertMessage type-asserts msg to the concrete type expected for its
// MessageType. A mismatch — a Message whose Type() is registered but
// whose Go type differs — returns an error instead of panicking.
func assertMessage[T Message](msg Message) (T, error) {
	m, ok := msg.(T)
	if !ok {
		return m, fmt.Errorf("message type %d: unexpected concrete type %T", msg.Type(), msg)
	}
	return m, nil
}

func requiredBytes(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func validateEnums(message protoreflect.Message, normalizeOptional bool) error {
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() == protoreflect.EnumKind && message.Has(field) {
			value := message.Get(field).Enum()
			if field.Enum().Values().ByNumber(value) == nil {
				if field.Cardinality() == protoreflect.Required {
					return fmt.Errorf("invalid required enum %s: %d", field.FullName(), value)
				}
				if !normalizeOptional {
					return fmt.Errorf("invalid optional enum %s: %d", field.FullName(), value)
				}
				message.Clear(field)
			}
		}

		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		if field.Cardinality() == protoreflect.Repeated {
			list := message.Get(field).List()
			for j := 0; j < list.Len(); j++ {
				if err := validateEnums(list.Get(j).Message(), normalizeOptional); err != nil {
					return err
				}
			}
		} else if message.Has(field) {
			if err := validateEnums(message.Get(field).Message(), normalizeOptional); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeEnumWire(data []byte, descriptor protoreflect.MessageDescriptor) ([]byte, error) {
	var normalized []byte
	for offset := 0; offset < len(data); {
		fieldStart := offset
		number, wireType, tagLen := protowire.ConsumeTag(data[offset:])
		if tagLen < 0 {
			return nil, protowire.ParseError(tagLen)
		}
		offset += tagLen

		valueLen := protowire.ConsumeFieldValue(number, wireType, data[offset:])
		if valueLen < 0 {
			return nil, protowire.ParseError(valueLen)
		}
		fieldEnd := offset + valueLen
		field := descriptor.Fields().ByNumber(number)

		drop := false
		var nested []byte
		nestedChanged := false
		if field != nil && field.Kind() == protoreflect.EnumKind && wireType == protowire.VarintType {
			value, n := protowire.ConsumeVarint(data[offset:])
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			drop = field.Enum().Values().ByNumber(protoreflect.EnumNumber(value)) == nil
		} else if field != nil && field.Kind() == protoreflect.MessageKind && wireType == protowire.BytesType {
			value, n := protowire.ConsumeBytes(data[offset:])
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			var err error
			nested, err = normalizeEnumWire(value, field.Message())
			if err != nil {
				return nil, err
			}
			nestedChanged = len(nested) != len(value)
		}

		if drop || nestedChanged {
			if normalized == nil {
				normalized = make([]byte, 0, len(data))
				normalized = append(normalized, data[:fieldStart]...)
			}
			if nestedChanged {
				normalized = append(normalized, data[fieldStart:offset]...)
				normalized = protowire.AppendBytes(normalized, nested)
			}
		} else if normalized != nil {
			normalized = append(normalized, data[fieldStart:fieldEnd]...)
		}
		offset = fieldEnd
	}
	if normalized == nil {
		return data, nil
	}
	return normalized, nil
}

// codecs is the per-MessageType registry. Order in this map carries
// no semantics — keep it sorted by MessageType constant order for
// reviewer sanity.
var codecs = map[MessageType]msgCodec{
	TypeManifests: {
		newProto: func() pb.Message { return &proto.TMManifests{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Manifests](msg)
			if err != nil {
				return nil, err
			}
			list := make([]*proto.TMManifest, len(m.List))
			for i, manifest := range m.List {
				list[i] = &proto.TMManifest{Stobject: requiredBytes(manifest.STObject)}
			}
			out := &proto.TMManifests{List: list}
			if m.History {
				out.History = pb.Bool(true)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMManifests)
			list := make([]Manifest, len(p.List))
			for i, m := range p.List {
				list[i] = Manifest{STObject: m.Stobject}
			}
			return &Manifests{List: list, History: p.GetHistory()}, nil
		},
	},
	TypePing: {
		newProto: func() pb.Message { return &proto.TMPing{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Ping](msg)
			if err != nil {
				return nil, err
			}
			pingType := proto.TMPingPingType(m.PType)
			out := &proto.TMPing{Type: &pingType}
			if m.HasSeq() {
				out.Seq = pb.Uint32(m.Seq)
			}
			if m.HasPingTime() {
				out.PingTime = pb.Uint64(m.PingTime)
			}
			if m.HasNetTime() {
				out.NetTime = pb.Uint64(m.NetTime)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMPing)
			return &Ping{
				PType:       PingType(p.GetType()),
				Seq:         p.GetSeq(),
				SeqSet:      p.Seq != nil,
				PingTime:    p.GetPingTime(),
				PingTimeSet: p.PingTime != nil,
				NetTime:     p.GetNetTime(),
				NetTimeSet:  p.NetTime != nil,
			}, nil
		},
	},
	TypeCluster: {
		newProto: func() pb.Message { return &proto.TMCluster{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Cluster](msg)
			if err != nil {
				return nil, err
			}
			nodes := make([]*proto.TMClusterNode, len(m.ClusterNodes))
			for i, n := range m.ClusterNodes {
				node := &proto.TMClusterNode{
					PublicKey:  pb.String(n.PublicKey),
					ReportTime: pb.Uint32(n.ReportTime),
					NodeLoad:   pb.Uint32(n.NodeLoad),
				}
				if n.NodeName != "" {
					node.NodeName = pb.String(n.NodeName)
				}
				if n.Address != "" {
					node.Address = pb.String(n.Address)
				}
				nodes[i] = node
			}
			sources := make([]*proto.TMLoadSource, len(m.LoadSources))
			for i, s := range m.LoadSources {
				source := &proto.TMLoadSource{
					Name: pb.String(s.Name),
					Cost: pb.Uint32(s.Cost),
				}
				if s.Count != 0 {
					source.Count = pb.Uint32(s.Count)
				}
				sources[i] = source
			}
			return &proto.TMCluster{ClusterNodes: nodes, LoadSources: sources}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMCluster)
			nodes := make([]ClusterNode, len(p.ClusterNodes))
			for i, n := range p.ClusterNodes {
				nodes[i] = ClusterNode{
					PublicKey:  n.GetPublicKey(),
					ReportTime: n.GetReportTime(),
					NodeLoad:   n.GetNodeLoad(),
					NodeName:   n.GetNodeName(),
					Address:    n.GetAddress(),
				}
			}
			sources := make([]LoadSource, len(p.LoadSources))
			for i, s := range p.LoadSources {
				sources[i] = LoadSource{
					Name:  s.GetName(),
					Cost:  s.GetCost(),
					Count: s.GetCount(),
				}
			}
			return &Cluster{ClusterNodes: nodes, LoadSources: sources}, nil
		},
	},
	TypeEndpoints: {
		newProto: func() pb.Message { return &proto.TMEndpoints{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Endpoints](msg)
			if err != nil {
				return nil, err
			}
			eps := make([]*proto.TMEndpoints_TMEndpointv2, len(m.EndpointsV2))
			for i, ep := range m.EndpointsV2 {
				eps[i] = &proto.TMEndpoints_TMEndpointv2{
					Endpoint: pb.String(ep.Endpoint),
					Hops:     pb.Uint32(ep.Hops),
				}
			}
			return &proto.TMEndpoints{Version: pb.Uint32(m.Version), EndpointsV2: eps}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMEndpoints)
			eps := make([]Endpointv2, len(p.EndpointsV2))
			for i, ep := range p.EndpointsV2 {
				eps[i] = Endpointv2{
					Endpoint: ep.GetEndpoint(),
					Hops:     ep.GetHops(),
				}
			}
			return &Endpoints{Version: p.GetVersion(), EndpointsV2: eps}, nil
		},
	},
	TypeTransaction: {
		newProto: func() pb.Message { return &proto.TMTransaction{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Transaction](msg)
			if err != nil {
				return nil, err
			}
			txStatus := proto.TransactionStatus(m.Status)
			out := &proto.TMTransaction{
				RawTransaction: requiredBytes(m.RawTransaction),
				Status:         &txStatus,
			}
			if m.ReceiveTimestamp != 0 {
				out.ReceiveTimestamp = pb.Uint64(m.ReceiveTimestamp)
			}
			if m.Deferred {
				out.Deferred = pb.Bool(true)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMTransaction)
			return &Transaction{
				RawTransaction:   p.GetRawTransaction(),
				Status:           TransactionStatus(p.GetStatus()),
				ReceiveTimestamp: p.GetReceiveTimestamp(),
				Deferred:         p.GetDeferred(),
			}, nil
		},
	},
	TypeGetLedger: {
		newProto: func() pb.Message { return &proto.TMGetLedger{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*GetLedger](msg)
			if err != nil {
				return nil, err
			}
			itype := proto.TMLedgerInfoType(m.InfoType)
			out := &proto.TMGetLedger{
				Itype:      &itype,
				LedgerHash: m.LedgerHash,
				NodeIDs:    m.NodeIDs,
			}
			if m.HasLType() {
				ltype := proto.TMLedgerType(m.LType)
				out.Ltype = &ltype
			}
			if m.HasLedgerSeq() {
				out.LedgerSeq = pb.Uint32(m.LedgerSeq)
			}
			if m.HasRequestCookie() {
				out.RequestCookie = pb.Uint64(m.RequestCookie)
			}
			if m.QueryType != nil {
				qt := proto.TMQueryType(*m.QueryType)
				out.QueryType = &qt
			}
			if m.HasQueryDepth() {
				out.QueryDepth = pb.Uint32(m.QueryDepth)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMGetLedger)
			g := &GetLedger{
				InfoType:         LedgerInfoType(p.GetItype()),
				LType:            LedgerType(p.GetLtype()),
				LTypeSet:         p.Ltype != nil,
				LedgerHash:       p.GetLedgerHash(),
				LedgerSeq:        p.GetLedgerSeq(),
				LedgerSeqSet:     p.LedgerSeq != nil,
				NodeIDs:          p.GetNodeIDs(),
				RequestCookie:    p.GetRequestCookie(),
				RequestCookieSet: p.RequestCookie != nil,
				QueryDepth:       p.GetQueryDepth(),
				QueryDepthSet:    p.QueryDepth != nil,
			}
			if p.QueryType != nil {
				qt := LedgerQueryType(*p.QueryType)
				g.QueryType = &qt
			}
			return g, nil
		},
	},
	TypeLedgerData: {
		newProto: func() pb.Message { return &proto.TMLedgerData{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*LedgerData](msg)
			if err != nil {
				return nil, err
			}
			nodes := make([]*proto.TMLedgerNode, len(m.Nodes))
			for i, n := range m.Nodes {
				nodes[i] = &proto.TMLedgerNode{
					Nodedata: requiredBytes(n.NodeData),
					Nodeid:   n.NodeID,
				}
			}
			ledgerInfoType := proto.TMLedgerInfoType(m.InfoType)
			out := &proto.TMLedgerData{
				LedgerHash: requiredBytes(m.LedgerHash),
				LedgerSeq:  pb.Uint32(m.LedgerSeq),
				Type:       &ledgerInfoType,
				Nodes:      nodes,
			}
			if m.HasRequestCookie() {
				out.RequestCookie = pb.Uint32(m.RequestCookie)
			}
			if m.HasError() {
				replyError := proto.TMReplyError(m.Error)
				out.Error = &replyError
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMLedgerData)
			nodes := make([]LedgerNode, len(p.Nodes))
			for i, n := range p.Nodes {
				nodes[i] = LedgerNode{
					NodeData: n.GetNodedata(),
					NodeID:   n.GetNodeid(),
				}
			}
			out := &LedgerData{
				LedgerHash:       p.GetLedgerHash(),
				LedgerSeq:        p.GetLedgerSeq(),
				InfoType:         LedgerInfoType(p.GetType()),
				Nodes:            nodes,
				RequestCookie:    p.GetRequestCookie(),
				RequestCookieSet: p.RequestCookie != nil,
				ErrorSet:         p.Error != nil,
			}
			if p.Error != nil {
				out.Error = ReplyError(*p.Error)
			}
			return out, nil
		},
	},
	TypeProposeLedger: {
		newProto: func() pb.Message { return &proto.TMProposeSet{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ProposeSet](msg)
			if err != nil {
				return nil, err
			}
			return &proto.TMProposeSet{
				ProposeSeq:          pb.Uint32(m.ProposeSeq),
				CurrentTxHash:       requiredBytes(m.CurrentTxHash),
				NodePubKey:          requiredBytes(m.NodePubKey),
				CloseTime:           pb.Uint32(m.CloseTime),
				Signature:           requiredBytes(m.Signature),
				Previousledger:      requiredBytes(m.PreviousLedger),
				AddedTransactions:   m.AddedTransactions,
				RemovedTransactions: m.RemovedTransactions,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMProposeSet)
			return &ProposeSet{
				ProposeSeq:          p.GetProposeSeq(),
				CurrentTxHash:       p.GetCurrentTxHash(),
				NodePubKey:          p.GetNodePubKey(),
				CloseTime:           p.GetCloseTime(),
				Signature:           p.GetSignature(),
				PreviousLedger:      p.GetPreviousledger(),
				AddedTransactions:   p.GetAddedTransactions(),
				RemovedTransactions: p.GetRemovedTransactions(),
			}, nil
		},
	},
	TypeStatusChange: {
		newProto: func() pb.Message { return &proto.TMStatusChange{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*StatusChange](msg)
			if err != nil {
				return nil, err
			}
			out := &proto.TMStatusChange{
				LedgerHash:         m.LedgerHash,
				LedgerHashPrevious: m.LedgerHashPrevious,
				FirstSeq:           m.FirstSeq,
				LastSeq:            m.LastSeq,
			}
			if m.HasNewStatus() {
				status := proto.NodeStatus(m.NewStatus)
				out.NewStatus = &status
			}
			if m.HasNewEvent() {
				event := proto.NodeEvent(m.NewEvent)
				out.NewEvent = &event
			}
			if m.HasLedgerSeq() {
				out.LedgerSeq = pb.Uint32(m.LedgerSeq)
			}
			if m.HasNetworkTime() {
				out.NetworkTime = pb.Uint64(m.NetworkTime)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMStatusChange)
			out := &StatusChange{
				NewStatusSet:       p.NewStatus != nil,
				NewEventSet:        p.NewEvent != nil,
				LedgerSeq:          p.GetLedgerSeq(),
				LedgerSeqSet:       p.LedgerSeq != nil,
				LedgerHash:         p.GetLedgerHash(),
				LedgerHashPrevious: p.GetLedgerHashPrevious(),
				NetworkTime:        p.GetNetworkTime(),
				NetworkTimeSet:     p.NetworkTime != nil,
				FirstSeq:           p.FirstSeq,
				LastSeq:            p.LastSeq,
			}
			if p.NewStatus != nil {
				out.NewStatus = NodeStatus(*p.NewStatus)
			}
			if p.NewEvent != nil {
				out.NewEvent = NodeEvent(*p.NewEvent)
			}
			return out, nil
		},
	},
	TypeHaveSet: {
		newProto: func() pb.Message { return &proto.TMHaveTransactionSet{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*HaveTransactionSet](msg)
			if err != nil {
				return nil, err
			}
			txSetStatus := proto.TxSetStatus(m.Status)
			return &proto.TMHaveTransactionSet{
				Status: &txSetStatus,
				Hash:   requiredBytes(m.Hash),
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMHaveTransactionSet)
			return &HaveTransactionSet{
				Status: TxSetStatus(p.GetStatus()),
				Hash:   p.GetHash(),
			}, nil
		},
	},
	TypeValidation: {
		newProto: func() pb.Message { return &proto.TMValidation{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Validation](msg)
			if err != nil {
				return nil, err
			}
			return &proto.TMValidation{Validation: requiredBytes(m.Validation)}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMValidation)
			return &Validation{Validation: p.GetValidation()}, nil
		},
	},
	TypeGetObjects: {
		newProto: func() pb.Message { return &proto.TMGetObjectByHash{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*GetObjectByHash](msg)
			if err != nil {
				return nil, err
			}
			objects := make([]*proto.TMIndexedObject, len(m.Objects))
			for i, o := range m.Objects {
				objects[i] = &proto.TMIndexedObject{
					Hash:   o.Hash,
					NodeID: o.NodeID,
					Index:  o.Index,
					Data:   o.Data,
				}
				if o.LedgerSeq != 0 {
					objects[i].LedgerSeq = pb.Uint32(o.LedgerSeq)
				}
			}
			objType := proto.TMGetObjectByHash_ObjectType(m.ObjType)
			out := &proto.TMGetObjectByHash{
				Type:       &objType,
				Query:      pb.Bool(m.Query),
				LedgerHash: m.LedgerHash,
				Objects:    objects,
			}
			if m.Fat {
				out.Fat = pb.Bool(true)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMGetObjectByHash)
			objects := make([]IndexedObject, len(p.Objects))
			for i, o := range p.Objects {
				objects[i] = IndexedObject{
					Hash:      o.GetHash(),
					NodeID:    o.GetNodeID(),
					Index:     o.GetIndex(),
					Data:      o.GetData(),
					LedgerSeq: o.GetLedgerSeq(),
				}
			}
			return &GetObjectByHash{
				ObjType:    ObjectType(p.GetType()),
				Query:      p.GetQuery(),
				LedgerHash: p.GetLedgerHash(),
				Fat:        p.GetFat(),
				Objects:    objects,
			}, nil
		},
	},
	TypeValidatorList: {
		newProto: func() pb.Message { return &proto.TMValidatorList{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ValidatorList](msg)
			if err != nil {
				return nil, err
			}
			return &proto.TMValidatorList{
				Manifest:  requiredBytes(m.Manifest),
				Blob:      requiredBytes(m.Blob),
				Signature: requiredBytes(m.Signature),
				Version:   pb.Uint32(m.Version),
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMValidatorList)
			return &ValidatorList{
				Manifest:  p.GetManifest(),
				Blob:      p.GetBlob(),
				Signature: p.GetSignature(),
				Version:   p.GetVersion(),
			}, nil
		},
	},
	TypeSquelch: {
		newProto: func() pb.Message { return &proto.TMSquelch{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Squelch](msg)
			if err != nil {
				return nil, err
			}
			out := &proto.TMSquelch{
				Squelch:         pb.Bool(m.Squelch),
				ValidatorPubKey: requiredBytes(m.ValidatorPubKey),
			}
			if m.SquelchDuration != 0 {
				out.SquelchDuration = pb.Uint32(m.SquelchDuration)
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMSquelch)
			return &Squelch{
				Squelch:         p.GetSquelch(),
				ValidatorPubKey: p.GetValidatorPubKey(),
				SquelchDuration: p.GetSquelchDuration(),
			}, nil
		},
	},
	TypeValidatorListCollection: {
		newProto: func() pb.Message { return &proto.TMValidatorListCollection{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ValidatorListCollection](msg)
			if err != nil {
				return nil, err
			}
			blobs := make([]*proto.ValidatorBlobInfo, len(m.Blobs))
			for i, b := range m.Blobs {
				blobs[i] = &proto.ValidatorBlobInfo{
					Manifest:  b.Manifest,
					Blob:      requiredBytes(b.Blob),
					Signature: requiredBytes(b.Signature),
				}
			}
			return &proto.TMValidatorListCollection{
				Version:  pb.Uint32(m.Version),
				Manifest: requiredBytes(m.Manifest),
				Blobs:    blobs,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMValidatorListCollection)
			blobs := make([]ValidatorBlobInfo, len(p.Blobs))
			for i, b := range p.Blobs {
				blobs[i] = ValidatorBlobInfo{
					Manifest:  b.GetManifest(),
					Blob:      b.GetBlob(),
					Signature: b.GetSignature(),
				}
			}
			return &ValidatorListCollection{
				Version:  p.GetVersion(),
				Manifest: p.GetManifest(),
				Blobs:    blobs,
			}, nil
		},
	},
	TypeProofPathReq: {
		newProto: func() pb.Message { return &proto.TMProofPathRequest{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ProofPathRequest](msg)
			if err != nil {
				return nil, err
			}
			mapType := proto.TMLedgerMapType(m.MapType)
			return &proto.TMProofPathRequest{
				Key:        requiredBytes(m.Key),
				LedgerHash: requiredBytes(m.LedgerHash),
				Type:       &mapType,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMProofPathRequest)
			return &ProofPathRequest{
				Key:        p.GetKey(),
				LedgerHash: p.GetLedgerHash(),
				MapType:    LedgerMapType(p.GetType()),
			}, nil
		},
	},
	TypeProofPathResponse: {
		newProto: func() pb.Message { return &proto.TMProofPathResponse{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ProofPathResponse](msg)
			if err != nil {
				return nil, err
			}
			mapType := proto.TMLedgerMapType(m.MapType)
			out := &proto.TMProofPathResponse{
				Key:          requiredBytes(m.Key),
				LedgerHash:   requiredBytes(m.LedgerHash),
				Type:         &mapType,
				LedgerHeader: m.LedgerHeader,
				Path:         m.Path,
			}
			if m.HasError() {
				replyError := proto.TMReplyError(m.Error)
				out.Error = &replyError
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMProofPathResponse)
			out := &ProofPathResponse{
				Key:          p.GetKey(),
				LedgerHash:   p.GetLedgerHash(),
				MapType:      LedgerMapType(p.GetType()),
				LedgerHeader: p.GetLedgerHeader(),
				Path:         p.GetPath(),
				ErrorSet:     p.Error != nil,
			}
			if p.Error != nil {
				out.Error = ReplyError(*p.Error)
			}
			return out, nil
		},
	},
	TypeReplayDeltaReq: {
		newProto: func() pb.Message { return &proto.TMReplayDeltaRequest{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ReplayDeltaRequest](msg)
			if err != nil {
				return nil, err
			}
			return &proto.TMReplayDeltaRequest{LedgerHash: requiredBytes(m.LedgerHash)}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMReplayDeltaRequest)
			return &ReplayDeltaRequest{LedgerHash: p.GetLedgerHash()}, nil
		},
	},
	TypeReplayDeltaResponse: {
		newProto: func() pb.Message { return &proto.TMReplayDeltaResponse{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*ReplayDeltaResponse](msg)
			if err != nil {
				return nil, err
			}
			out := &proto.TMReplayDeltaResponse{
				LedgerHash:   requiredBytes(m.LedgerHash),
				LedgerHeader: m.LedgerHeader,
				Transaction:  m.Transactions,
			}
			if m.HasError() {
				replyError := proto.TMReplyError(m.Error)
				out.Error = &replyError
			}
			return out, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMReplayDeltaResponse)
			out := &ReplayDeltaResponse{
				LedgerHash:   p.GetLedgerHash(),
				LedgerHeader: p.GetLedgerHeader(),
				Transactions: p.GetTransaction(),
				ErrorSet:     p.Error != nil,
			}
			if p.Error != nil {
				out.Error = ReplyError(*p.Error)
			}
			return out, nil
		},
	},
	TypeHaveTransactions: {
		newProto: func() pb.Message { return &proto.TMHaveTransactions{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*HaveTransactions](msg)
			if err != nil {
				return nil, err
			}
			return &proto.TMHaveTransactions{Hashes: m.Hashes}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMHaveTransactions)
			return &HaveTransactions{Hashes: p.GetHashes()}, nil
		},
	},
	TypeTransactions: {
		newProto: func() pb.Message { return &proto.TMTransactions{} },
		encode: func(msg Message) (pb.Message, error) {
			m, err := assertMessage[*Transactions](msg)
			if err != nil {
				return nil, err
			}
			txs := make([]*proto.TMTransaction, len(m.Transactions))
			for i, tx := range m.Transactions {
				txStatus := proto.TransactionStatus(tx.Status)
				txs[i] = &proto.TMTransaction{
					RawTransaction: requiredBytes(tx.RawTransaction),
					Status:         &txStatus,
				}
				if tx.ReceiveTimestamp != 0 {
					txs[i].ReceiveTimestamp = pb.Uint64(tx.ReceiveTimestamp)
				}
				if tx.Deferred {
					txs[i].Deferred = pb.Bool(true)
				}
			}
			return &proto.TMTransactions{Transactions: txs}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMTransactions)
			txs := make([]Transaction, len(p.Transactions))
			for i, tx := range p.Transactions {
				txs[i] = Transaction{
					RawTransaction:   tx.GetRawTransaction(),
					Status:           TransactionStatus(tx.GetStatus()),
					ReceiveTimestamp: tx.GetReceiveTimestamp(),
					Deferred:         tx.GetDeferred(),
				}
			}
			return &Transactions{Transactions: txs}, nil
		},
	},
}

// Encode encodes a message to bytes using protobuf.
func Encode(msg Message) ([]byte, error) {
	c, ok := codecs[msg.Type()]
	if !ok {
		return nil, fmt.Errorf("unknown message type: %d", msg.Type())
	}
	pmsg, err := c.encode(msg)
	if err != nil {
		return nil, err
	}
	if err := validateEnums(pmsg.ProtoReflect(), false); err != nil {
		return nil, err
	}
	return pb.Marshal(pmsg)
}

// Decode decodes a message from bytes using protobuf.
func Decode(msgType MessageType, data []byte) (Message, error) {
	c, ok := codecs[msgType]
	if !ok {
		return nil, fmt.Errorf("unknown message type: %d", msgType)
	}
	pmsg := c.newProto()
	normalized, err := normalizeEnumWire(data, pmsg.ProtoReflect().Descriptor())
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}
	if err := pb.Unmarshal(normalized, pmsg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}
	if err := validateEnums(pmsg.ProtoReflect(), true); err != nil {
		return nil, fmt.Errorf("failed to validate: %w", err)
	}
	return c.decode(pmsg)
}

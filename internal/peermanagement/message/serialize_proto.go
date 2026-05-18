package message

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/proto"
	pb "google.golang.org/protobuf/proto"
)

// msgCodec bundles the three operations every message type needs into
// a single record. Previously these lived in three parallel
// type-switches (newProtoMessage / toProto / fromProto); adding a new
// type meant touching all three sites and forgetting one was an easy
// way to ship a half-wired message. With the registry, one missing
// table entry surfaces as a single "unknown message type" at both
// encode and decode, and the three pieces of logic for one type sit
// next to each other.
type msgCodec struct {
	newProto func() pb.Message
	encode   func(Message) (pb.Message, error)
	decode   func(pb.Message) (Message, error)
}

// codecs is the per-MessageType registry. Order in this map carries
// no semantics — keep it sorted by MessageType constant order for
// reviewer sanity.
var codecs = map[MessageType]msgCodec{
	TypeManifests: {
		newProto: func() pb.Message { return &proto.TMManifests{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*Manifests)
			list := make([]*proto.TMManifest, len(m.List))
			for i, manifest := range m.List {
				list[i] = &proto.TMManifest{Stobject: manifest.STObject}
			}
			return &proto.TMManifests{List: list, History: m.History}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMManifests)
			list := make([]Manifest, len(p.List))
			for i, m := range p.List {
				list[i] = Manifest{STObject: m.Stobject}
			}
			return &Manifests{List: list, History: p.History}, nil
		},
	},
	TypePing: {
		newProto: func() pb.Message { return &proto.TMPing{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*Ping)
			pingType := proto.TMPing_PingType(m.PType)
			return &proto.TMPing{
				Type:     &pingType,
				Seq:      &m.Seq,
				PingTime: &m.PingTime,
				NetTime:  &m.NetTime,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMPing)
			return &Ping{
				PType:    PingType(p.GetType()),
				Seq:      p.GetSeq(),
				PingTime: p.GetPingTime(),
				NetTime:  p.GetNetTime(),
			}, nil
		},
	},
	TypeCluster: {
		newProto: func() pb.Message { return &proto.TMCluster{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*Cluster)
			nodes := make([]*proto.TMClusterNode, len(m.ClusterNodes))
			for i, n := range m.ClusterNodes {
				nodes[i] = &proto.TMClusterNode{
					PublicKey:  pb.String(n.PublicKey),
					ReportTime: pb.Uint32(n.ReportTime),
					NodeLoad:   pb.Uint32(n.NodeLoad),
					NodeName:   n.NodeName,
					Address:    n.Address,
				}
			}
			sources := make([]*proto.TMLoadSource, len(m.LoadSources))
			for i, s := range m.LoadSources {
				sources[i] = &proto.TMLoadSource{
					Name:  pb.String(s.Name),
					Cost:  pb.Uint32(s.Cost),
					Count: s.Count,
				}
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
			m := msg.(*Endpoints)
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
			m := msg.(*Transaction)
			txStatus := proto.TransactionStatus(m.Status)
			return &proto.TMTransaction{
				RawTransaction:   m.RawTransaction,
				Status:           &txStatus,
				ReceiveTimestamp: m.ReceiveTimestamp,
				Deferred:         m.Deferred,
			}, nil
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
			m := msg.(*GetLedger)
			itype := proto.TMLedgerInfoType(m.InfoType)
			ltype := proto.TMLedgerType(m.LType)
			return &proto.TMGetLedger{
				Itype:         &itype,
				Ltype:         &ltype,
				LedgerHash:    m.LedgerHash,
				LedgerSeq:     m.LedgerSeq,
				NodeIds:       m.NodeIDs,
				RequestCookie: m.RequestCookie,
				QueryDepth:    m.QueryDepth,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMGetLedger)
			return &GetLedger{
				InfoType:      LedgerInfoType(p.GetItype()),
				LType:         LedgerType(p.GetLtype()),
				LedgerHash:    p.GetLedgerHash(),
				LedgerSeq:     p.GetLedgerSeq(),
				NodeIDs:       p.GetNodeIds(),
				RequestCookie: p.GetRequestCookie(),
				QueryDepth:    p.GetQueryDepth(),
			}, nil
		},
	},
	TypeLedgerData: {
		newProto: func() pb.Message { return &proto.TMLedgerData{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*LedgerData)
			nodes := make([]*proto.TMLedgerNode, len(m.Nodes))
			for i, n := range m.Nodes {
				nodes[i] = &proto.TMLedgerNode{
					Nodedata: n.NodeData,
					Nodeid:   n.NodeID,
				}
			}
			ledgerInfoType := proto.TMLedgerInfoType(m.InfoType)
			return &proto.TMLedgerData{
				LedgerHash:    m.LedgerHash,
				LedgerSeq:     pb.Uint32(m.LedgerSeq),
				Type:          &ledgerInfoType,
				Nodes:         nodes,
				RequestCookie: m.RequestCookie,
				Error:         proto.TMReplyError(m.Error),
			}, nil
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
			return &LedgerData{
				LedgerHash:    p.GetLedgerHash(),
				LedgerSeq:     p.GetLedgerSeq(),
				InfoType:      LedgerInfoType(p.GetType()),
				Nodes:         nodes,
				RequestCookie: p.GetRequestCookie(),
				Error:         ReplyError(p.GetError()),
			}, nil
		},
	},
	TypeProposeLedger: {
		newProto: func() pb.Message { return &proto.TMProposeSet{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*ProposeSet)
			return &proto.TMProposeSet{
				ProposeSeq:          pb.Uint32(m.ProposeSeq),
				CurrentTxHash:       m.CurrentTxHash,
				NodePubKey:          m.NodePubKey,
				CloseTime:           pb.Uint32(m.CloseTime),
				Signature:           m.Signature,
				PreviousLedger:      m.PreviousLedger,
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
				PreviousLedger:      p.GetPreviousLedger(),
				AddedTransactions:   p.GetAddedTransactions(),
				RemovedTransactions: p.GetRemovedTransactions(),
			}, nil
		},
	},
	TypeStatusChange: {
		newProto: func() pb.Message { return &proto.TMStatusChange{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*StatusChange)
			return &proto.TMStatusChange{
				NewStatus:          proto.NodeStatus(m.NewStatus),
				NewEvent:           proto.NodeEvent(m.NewEvent),
				LedgerSeq:          m.LedgerSeq,
				LedgerHash:         m.LedgerHash,
				LedgerHashPrevious: m.LedgerHashPrevious,
				NetworkTime:        m.NetworkTime,
				FirstSeq:           m.FirstSeq,
				LastSeq:            m.LastSeq,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMStatusChange)
			return &StatusChange{
				NewStatus:          NodeStatus(p.GetNewStatus()),
				NewEvent:           NodeEvent(p.GetNewEvent()),
				LedgerSeq:          p.GetLedgerSeq(),
				LedgerHash:         p.GetLedgerHash(),
				LedgerHashPrevious: p.GetLedgerHashPrevious(),
				NetworkTime:        p.GetNetworkTime(),
				FirstSeq:           p.FirstSeq,
				LastSeq:            p.LastSeq,
			}, nil
		},
	},
	TypeHaveSet: {
		newProto: func() pb.Message { return &proto.TMHaveTransactionSet{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*HaveTransactionSet)
			txSetStatus := proto.TxSetStatus(m.Status)
			return &proto.TMHaveTransactionSet{
				Status: &txSetStatus,
				Hash:   m.Hash,
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
			m := msg.(*Validation)
			return &proto.TMValidation{Validation: m.Validation}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMValidation)
			return &Validation{Validation: p.GetValidation()}, nil
		},
	},
	TypeGetObjects: {
		newProto: func() pb.Message { return &proto.TMGetObjectByHash{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*GetObjectByHash)
			objects := make([]*proto.TMIndexedObject, len(m.Objects))
			for i, o := range m.Objects {
				objects[i] = &proto.TMIndexedObject{
					Hash:      o.Hash,
					NodeId:    o.NodeID,
					Index:     o.Index,
					Data:      o.Data,
					LedgerSeq: o.LedgerSeq,
				}
			}
			objType := proto.ObjectType(m.ObjType)
			return &proto.TMGetObjectByHash{
				Type:       &objType,
				Query:      pb.Bool(m.Query),
				Seq:        m.Seq,
				LedgerHash: m.LedgerHash,
				Fat:        m.Fat,
				Objects:    objects,
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMGetObjectByHash)
			objects := make([]IndexedObject, len(p.Objects))
			for i, o := range p.Objects {
				objects[i] = IndexedObject{
					Hash:      o.GetHash(),
					NodeID:    o.GetNodeId(),
					Index:     o.GetIndex(),
					Data:      o.GetData(),
					LedgerSeq: o.GetLedgerSeq(),
				}
			}
			return &GetObjectByHash{
				ObjType:    ObjectType(p.GetType()),
				Query:      p.GetQuery(),
				Seq:        p.GetSeq(),
				LedgerHash: p.GetLedgerHash(),
				Fat:        p.GetFat(),
				Objects:    objects,
			}, nil
		},
	},
	TypeValidatorList: {
		newProto: func() pb.Message { return &proto.TMValidatorList{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*ValidatorList)
			return &proto.TMValidatorList{
				Manifest:  m.Manifest,
				Blob:      m.Blob,
				Signature: m.Signature,
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
			m := msg.(*Squelch)
			return &proto.TMSquelch{
				Squelch:         pb.Bool(m.Squelch),
				ValidatorPubKey: m.ValidatorPubKey,
				SquelchDuration: m.SquelchDuration,
			}, nil
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
			m := msg.(*ValidatorListCollection)
			blobs := make([]*proto.ValidatorBlobInfo, len(m.Blobs))
			for i, b := range m.Blobs {
				blobs[i] = &proto.ValidatorBlobInfo{
					Manifest:  b.Manifest,
					Blob:      b.Blob,
					Signature: b.Signature,
				}
			}
			return &proto.TMValidatorListCollection{
				Version:  pb.Uint32(m.Version),
				Manifest: m.Manifest,
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
			m := msg.(*ProofPathRequest)
			mapType := proto.TMLedgerMapType(m.MapType)
			return &proto.TMProofPathRequest{
				Key:        m.Key,
				LedgerHash: m.LedgerHash,
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
			m := msg.(*ProofPathResponse)
			mapType := proto.TMLedgerMapType(m.MapType)
			return &proto.TMProofPathResponse{
				Key:          m.Key,
				LedgerHash:   m.LedgerHash,
				Type:         &mapType,
				LedgerHeader: m.LedgerHeader,
				Path:         m.Path,
				Error:        proto.TMReplyError(m.Error),
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMProofPathResponse)
			return &ProofPathResponse{
				Key:          p.GetKey(),
				LedgerHash:   p.GetLedgerHash(),
				MapType:      LedgerMapType(p.GetType()),
				LedgerHeader: p.GetLedgerHeader(),
				Path:         p.GetPath(),
				Error:        ReplyError(p.GetError()),
			}, nil
		},
	},
	TypeReplayDeltaReq: {
		newProto: func() pb.Message { return &proto.TMReplayDeltaRequest{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*ReplayDeltaRequest)
			return &proto.TMReplayDeltaRequest{LedgerHash: m.LedgerHash}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMReplayDeltaRequest)
			return &ReplayDeltaRequest{LedgerHash: p.GetLedgerHash()}, nil
		},
	},
	TypeReplayDeltaResponse: {
		newProto: func() pb.Message { return &proto.TMReplayDeltaResponse{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*ReplayDeltaResponse)
			return &proto.TMReplayDeltaResponse{
				LedgerHash:   m.LedgerHash,
				LedgerHeader: m.LedgerHeader,
				Transaction:  m.Transactions,
				Error:        proto.TMReplyError(m.Error),
			}, nil
		},
		decode: func(pmsg pb.Message) (Message, error) {
			p := pmsg.(*proto.TMReplayDeltaResponse)
			return &ReplayDeltaResponse{
				LedgerHash:   p.GetLedgerHash(),
				LedgerHeader: p.GetLedgerHeader(),
				Transactions: p.GetTransaction(),
				Error:        ReplyError(p.GetError()),
			}, nil
		},
	},
	TypeHaveTransactions: {
		newProto: func() pb.Message { return &proto.TMHaveTransactions{} },
		encode: func(msg Message) (pb.Message, error) {
			m := msg.(*HaveTransactions)
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
			m := msg.(*Transactions)
			txs := make([]*proto.TMTransaction, len(m.Transactions))
			for i, tx := range m.Transactions {
				txStatus := proto.TransactionStatus(tx.Status)
				txs[i] = &proto.TMTransaction{
					RawTransaction:   tx.RawTransaction,
					Status:           &txStatus,
					ReceiveTimestamp: tx.ReceiveTimestamp,
					Deferred:         tx.Deferred,
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
	return pb.Marshal(pmsg)
}

// Decode decodes a message from bytes using protobuf.
func Decode(msgType MessageType, data []byte) (Message, error) {
	c, ok := codecs[msgType]
	if !ok {
		return nil, fmt.Errorf("unknown message type: %d", msgType)
	}
	pmsg := c.newProto()
	if err := pb.Unmarshal(data, pmsg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}
	return c.decode(pmsg)
}

package message

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/proto"
	pb "google.golang.org/protobuf/proto"
)

// Encode encodes a message to bytes using protobuf.
func Encode(msg Message) ([]byte, error) {
	protoMsg, err := toProto(msg)
	if err != nil {
		return nil, err
	}
	return pb.Marshal(protoMsg)
}

// Decode decodes a message from bytes using protobuf.
func Decode(msgType MessageType, data []byte) (Message, error) {
	protoMsg, err := newProtoMessage(msgType)
	if err != nil {
		return nil, err
	}

	if err := pb.Unmarshal(data, protoMsg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal: %w", err)
	}

	return fromProto(msgType, protoMsg)
}

// newProtoMessage creates a new proto message for the given type.
func newProtoMessage(msgType MessageType) (pb.Message, error) {
	switch msgType {
	case TypePing:
		return &proto.TMPing{}, nil
	case TypeManifests:
		return &proto.TMManifests{}, nil
	case TypeCluster:
		return &proto.TMCluster{}, nil
	case TypeEndpoints:
		return &proto.TMEndpoints{}, nil
	case TypeTransaction:
		return &proto.TMTransaction{}, nil
	case TypeTransactions:
		return &proto.TMTransactions{}, nil
	case TypeGetLedger:
		return &proto.TMGetLedger{}, nil
	case TypeLedgerData:
		return &proto.TMLedgerData{}, nil
	case TypeProposeLedger:
		return &proto.TMProposeSet{}, nil
	case TypeStatusChange:
		return &proto.TMStatusChange{}, nil
	case TypeHaveSet:
		return &proto.TMHaveTransactionSet{}, nil
	case TypeValidation:
		return &proto.TMValidation{}, nil
	case TypeGetObjects:
		return &proto.TMGetObjectByHash{}, nil
	case TypeValidatorList:
		return &proto.TMValidatorList{}, nil
	case TypeSquelch:
		return &proto.TMSquelch{}, nil
	case TypeValidatorListCollection:
		return &proto.TMValidatorListCollection{}, nil
	case TypeProofPathReq:
		return &proto.TMProofPathRequest{}, nil
	case TypeProofPathResponse:
		return &proto.TMProofPathResponse{}, nil
	case TypeReplayDeltaReq:
		return &proto.TMReplayDeltaRequest{}, nil
	case TypeReplayDeltaResponse:
		return &proto.TMReplayDeltaResponse{}, nil
	case TypeHaveTransactions:
		return &proto.TMHaveTransactions{}, nil
	default:
		return nil, fmt.Errorf("unknown message type: %d", msgType)
	}
}

// toProto converts a message to its protobuf representation.
func toProto(msg Message) (pb.Message, error) {
	switch m := msg.(type) {
	case *Ping:
		pingType := proto.TMPing_PingType(m.PType)
		return &proto.TMPing{
			Type:     &pingType,
			Seq:      &m.Seq,
			PingTime: &m.PingTime,
			NetTime:  &m.NetTime,
		}, nil

	case *Manifests:
		list := make([]*proto.TMManifest, len(m.List))
		for i, manifest := range m.List {
			list[i] = &proto.TMManifest{Stobject: manifest.STObject}
		}
		return &proto.TMManifests{List: list, History: m.History}, nil

	case *Cluster:
		nodes := make([]*proto.TMClusterNode, len(m.ClusterNodes))
		for i, n := range m.ClusterNodes {
			nodes[i] = &proto.TMClusterNode{
				PublicKey:  n.PublicKey,
				ReportTime: n.ReportTime,
				NodeLoad:   n.NodeLoad,
				NodeName:   n.NodeName,
				Address:    n.Address,
			}
		}
		sources := make([]*proto.TMLoadSource, len(m.LoadSources))
		for i, s := range m.LoadSources {
			sources[i] = &proto.TMLoadSource{
				Name:  s.Name,
				Cost:  s.Cost,
				Count: s.Count,
			}
		}
		return &proto.TMCluster{ClusterNodes: nodes, LoadSources: sources}, nil

	case *Endpoints:
		eps := make([]*proto.TMEndpoints_TMEndpointv2, len(m.EndpointsV2))
		for i, ep := range m.EndpointsV2 {
			eps[i] = &proto.TMEndpoints_TMEndpointv2{
				Endpoint: ep.Endpoint,
				Hops:     ep.Hops,
			}
		}
		return &proto.TMEndpoints{Version: m.Version, EndpointsV2: eps}, nil

	case *Transaction:
		return &proto.TMTransaction{
			RawTransaction:   m.RawTransaction,
			Status:           proto.TransactionStatus(m.Status),
			ReceiveTimestamp: m.ReceiveTimestamp,
			Deferred:         m.Deferred,
		}, nil

	case *Transactions:
		txs := make([]*proto.TMTransaction, len(m.Transactions))
		for i, tx := range m.Transactions {
			txs[i] = &proto.TMTransaction{
				RawTransaction:   tx.RawTransaction,
				Status:           proto.TransactionStatus(tx.Status),
				ReceiveTimestamp: tx.ReceiveTimestamp,
				Deferred:         tx.Deferred,
			}
		}
		return &proto.TMTransactions{Transactions: txs}, nil

	case *StatusChange:
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

	case *ProposeSet:
		return &proto.TMProposeSet{
			ProposeSeq:          m.ProposeSeq,
			CurrentTxHash:       m.CurrentTxHash,
			NodePubKey:          m.NodePubKey,
			CloseTime:           m.CloseTime,
			Signature:           m.Signature,
			PreviousLedger:      m.PreviousLedger,
			AddedTransactions:   m.AddedTransactions,
			RemovedTransactions: m.RemovedTransactions,
		}, nil

	case *HaveTransactionSet:
		return &proto.TMHaveTransactionSet{
			Status: proto.TxSetStatus(m.Status),
			Hash:   m.Hash,
		}, nil

	case *Validation:
		return &proto.TMValidation{Validation: m.Validation}, nil

	case *ValidatorList:
		return &proto.TMValidatorList{
			Manifest:  m.Manifest,
			Blob:      m.Blob,
			Signature: m.Signature,
			Version:   m.Version,
		}, nil

	case *ValidatorListCollection:
		blobs := make([]*proto.ValidatorBlobInfo, len(m.Blobs))
		for i, b := range m.Blobs {
			blobs[i] = &proto.ValidatorBlobInfo{
				Manifest:  b.Manifest,
				Blob:      b.Blob,
				Signature: b.Signature,
			}
		}
		return &proto.TMValidatorListCollection{
			Version:  m.Version,
			Manifest: m.Manifest,
			Blobs:    blobs,
		}, nil

	case *GetLedger:
		return &proto.TMGetLedger{
			Itype:         proto.TMLedgerInfoType(m.InfoType),
			Ltype:         proto.TMLedgerType(m.LType),
			LedgerHash:    m.LedgerHash,
			LedgerSeq:     m.LedgerSeq,
			NodeIds:       m.NodeIDs,
			RequestCookie: m.RequestCookie,
			QueryDepth:    m.QueryDepth,
		}, nil

	case *LedgerData:
		nodes := make([]*proto.TMLedgerNode, len(m.Nodes))
		for i, n := range m.Nodes {
			nodes[i] = &proto.TMLedgerNode{
				Nodedata: n.NodeData,
				Nodeid:   n.NodeID,
			}
		}
		return &proto.TMLedgerData{
			LedgerHash:    m.LedgerHash,
			LedgerSeq:     m.LedgerSeq,
			Type:          proto.TMLedgerInfoType(m.InfoType),
			Nodes:         nodes,
			RequestCookie: m.RequestCookie,
			Error:         proto.TMReplyError(m.Error),
		}, nil

	case *GetObjectByHash:
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
		return &proto.TMGetObjectByHash{
			Type:       proto.ObjectType(m.ObjType),
			Query:      m.Query,
			Seq:        m.Seq,
			LedgerHash: m.LedgerHash,
			Fat:        m.Fat,
			Objects:    objects,
		}, nil

	case *Squelch:
		return &proto.TMSquelch{
			Squelch:         m.Squelch,
			ValidatorPubKey: m.ValidatorPubKey,
			SquelchDuration: m.SquelchDuration,
		}, nil

	case *ProofPathRequest:
		return &proto.TMProofPathRequest{
			Key:        m.Key,
			LedgerHash: m.LedgerHash,
			Type:       proto.TMLedgerMapType(m.MapType),
		}, nil

	case *ProofPathResponse:
		return &proto.TMProofPathResponse{
			Key:          m.Key,
			LedgerHash:   m.LedgerHash,
			Type:         proto.TMLedgerMapType(m.MapType),
			LedgerHeader: m.LedgerHeader,
			Path:         m.Path,
			Error:        proto.TMReplyError(m.Error),
		}, nil

	case *ReplayDeltaRequest:
		return &proto.TMReplayDeltaRequest{LedgerHash: m.LedgerHash}, nil

	case *ReplayDeltaResponse:
		return &proto.TMReplayDeltaResponse{
			LedgerHash:   m.LedgerHash,
			LedgerHeader: m.LedgerHeader,
			Transaction:  m.Transactions,
			Error:        proto.TMReplyError(m.Error),
		}, nil

	case *HaveTransactions:
		return &proto.TMHaveTransactions{Hashes: m.Hashes}, nil

	default:
		return nil, fmt.Errorf("unknown message type: %T", msg)
	}
}

// fromProto converts a protobuf message to our message type.
func fromProto(msgType MessageType, protoMsg pb.Message) (Message, error) {
	switch msgType {
	case TypePing:
		p := protoMsg.(*proto.TMPing)
		return &Ping{
			PType:    PingType(p.GetType()),
			Seq:      p.GetSeq(),
			PingTime: p.GetPingTime(),
			NetTime:  p.GetNetTime(),
		}, nil

	case TypeManifests:
		p := protoMsg.(*proto.TMManifests)
		list := make([]Manifest, len(p.List))
		for i, m := range p.List {
			list[i] = Manifest{STObject: m.Stobject}
		}
		return &Manifests{List: list, History: p.History}, nil

	case TypeCluster:
		p := protoMsg.(*proto.TMCluster)
		nodes := make([]ClusterNode, len(p.ClusterNodes))
		for i, n := range p.ClusterNodes {
			nodes[i] = ClusterNode{
				PublicKey:  n.PublicKey,
				ReportTime: n.ReportTime,
				NodeLoad:   n.NodeLoad,
				NodeName:   n.NodeName,
				Address:    n.Address,
			}
		}
		sources := make([]LoadSource, len(p.LoadSources))
		for i, s := range p.LoadSources {
			sources[i] = LoadSource{
				Name:  s.Name,
				Cost:  s.Cost,
				Count: s.Count,
			}
		}
		return &Cluster{ClusterNodes: nodes, LoadSources: sources}, nil

	case TypeEndpoints:
		p := protoMsg.(*proto.TMEndpoints)
		eps := make([]Endpointv2, len(p.EndpointsV2))
		for i, ep := range p.EndpointsV2 {
			eps[i] = Endpointv2{
				Endpoint: ep.Endpoint,
				Hops:     ep.Hops,
			}
		}
		return &Endpoints{Version: p.Version, EndpointsV2: eps}, nil

	case TypeTransaction:
		p := protoMsg.(*proto.TMTransaction)
		return &Transaction{
			RawTransaction:   p.RawTransaction,
			Status:           TransactionStatus(p.Status),
			ReceiveTimestamp: p.ReceiveTimestamp,
			Deferred:         p.Deferred,
		}, nil

	case TypeTransactions:
		p := protoMsg.(*proto.TMTransactions)
		txs := make([]Transaction, len(p.Transactions))
		for i, tx := range p.Transactions {
			txs[i] = Transaction{
				RawTransaction:   tx.RawTransaction,
				Status:           TransactionStatus(tx.Status),
				ReceiveTimestamp: tx.ReceiveTimestamp,
				Deferred:         tx.Deferred,
			}
		}
		return &Transactions{Transactions: txs}, nil

	case TypeGetLedger:
		p := protoMsg.(*proto.TMGetLedger)
		return &GetLedger{
			InfoType:      LedgerInfoType(p.Itype),
			LType:         LedgerType(p.Ltype),
			LedgerHash:    p.LedgerHash,
			LedgerSeq:     p.LedgerSeq,
			NodeIDs:       p.NodeIds,
			RequestCookie: p.RequestCookie,
			QueryDepth:    p.QueryDepth,
		}, nil

	case TypeLedgerData:
		p := protoMsg.(*proto.TMLedgerData)
		nodes := make([]LedgerNode, len(p.Nodes))
		for i, n := range p.Nodes {
			nodes[i] = LedgerNode{
				NodeData: n.Nodedata,
				NodeID:   n.Nodeid,
			}
		}
		return &LedgerData{
			LedgerHash:    p.LedgerHash,
			LedgerSeq:     p.LedgerSeq,
			InfoType:      LedgerInfoType(p.Type),
			Nodes:         nodes,
			RequestCookie: p.RequestCookie,
			Error:         ReplyError(p.Error),
		}, nil

	case TypeProposeLedger:
		p := protoMsg.(*proto.TMProposeSet)
		return &ProposeSet{
			ProposeSeq:          p.ProposeSeq,
			CurrentTxHash:       p.CurrentTxHash,
			NodePubKey:          p.NodePubKey,
			CloseTime:           p.CloseTime,
			Signature:           p.Signature,
			PreviousLedger:      p.PreviousLedger,
			AddedTransactions:   p.AddedTransactions,
			RemovedTransactions: p.RemovedTransactions,
		}, nil

	case TypeStatusChange:
		p := protoMsg.(*proto.TMStatusChange)
		return &StatusChange{
			NewStatus:          NodeStatus(p.NewStatus),
			NewEvent:           NodeEvent(p.NewEvent),
			LedgerSeq:          p.LedgerSeq,
			LedgerHash:         p.LedgerHash,
			LedgerHashPrevious: p.LedgerHashPrevious,
			NetworkTime:        p.NetworkTime,
			FirstSeq:           p.FirstSeq,
			LastSeq:            p.LastSeq,
		}, nil

	case TypeHaveSet:
		p := protoMsg.(*proto.TMHaveTransactionSet)
		return &HaveTransactionSet{
			Status: TxSetStatus(p.Status),
			Hash:   p.Hash,
		}, nil

	case TypeValidation:
		p := protoMsg.(*proto.TMValidation)
		return &Validation{Validation: p.Validation}, nil

	case TypeGetObjects:
		p := protoMsg.(*proto.TMGetObjectByHash)
		objects := make([]IndexedObject, len(p.Objects))
		for i, o := range p.Objects {
			objects[i] = IndexedObject{
				Hash:      o.Hash,
				NodeID:    o.NodeId,
				Index:     o.Index,
				Data:      o.Data,
				LedgerSeq: o.LedgerSeq,
			}
		}
		return &GetObjectByHash{
			ObjType:    ObjectType(p.Type),
			Query:      p.Query,
			Seq:        p.Seq,
			LedgerHash: p.LedgerHash,
			Fat:        p.Fat,
			Objects:    objects,
		}, nil

	case TypeValidatorList:
		p := protoMsg.(*proto.TMValidatorList)
		return &ValidatorList{
			Manifest:  p.Manifest,
			Blob:      p.Blob,
			Signature: p.Signature,
			Version:   p.Version,
		}, nil

	case TypeSquelch:
		p := protoMsg.(*proto.TMSquelch)
		return &Squelch{
			Squelch:         p.Squelch,
			ValidatorPubKey: p.ValidatorPubKey,
			SquelchDuration: p.SquelchDuration,
		}, nil

	case TypeValidatorListCollection:
		p := protoMsg.(*proto.TMValidatorListCollection)
		blobs := make([]ValidatorBlobInfo, len(p.Blobs))
		for i, b := range p.Blobs {
			blobs[i] = ValidatorBlobInfo{
				Manifest:  b.Manifest,
				Blob:      b.Blob,
				Signature: b.Signature,
			}
		}
		return &ValidatorListCollection{
			Version:  p.Version,
			Manifest: p.Manifest,
			Blobs:    blobs,
		}, nil

	case TypeProofPathReq:
		p := protoMsg.(*proto.TMProofPathRequest)
		return &ProofPathRequest{
			Key:        p.Key,
			LedgerHash: p.LedgerHash,
			MapType:    LedgerMapType(p.Type),
		}, nil

	case TypeProofPathResponse:
		p := protoMsg.(*proto.TMProofPathResponse)
		return &ProofPathResponse{
			Key:          p.Key,
			LedgerHash:   p.LedgerHash,
			MapType:      LedgerMapType(p.Type),
			LedgerHeader: p.LedgerHeader,
			Path:         p.Path,
			Error:        ReplyError(p.Error),
		}, nil

	case TypeReplayDeltaReq:
		p := protoMsg.(*proto.TMReplayDeltaRequest)
		return &ReplayDeltaRequest{LedgerHash: p.LedgerHash}, nil

	case TypeReplayDeltaResponse:
		p := protoMsg.(*proto.TMReplayDeltaResponse)
		return &ReplayDeltaResponse{
			LedgerHash:   p.LedgerHash,
			LedgerHeader: p.LedgerHeader,
			Transactions: p.Transaction,
			Error:        ReplyError(p.Error),
		}, nil

	case TypeHaveTransactions:
		p := protoMsg.(*proto.TMHaveTransactions)
		return &HaveTransactions{Hashes: p.Hashes}, nil

	default:
		return nil, fmt.Errorf("unknown message type: %d", msgType)
	}
}

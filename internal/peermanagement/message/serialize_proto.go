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

	case *Endpoints:
		eps := make([]*proto.TMEndpoints_TMEndpointv2, len(m.EndpointsV2))
		for i, ep := range m.EndpointsV2 {
			eps[i] = &proto.TMEndpoints_TMEndpointv2{
				Endpoint: pb.String(ep.Endpoint),
				Hops:     pb.Uint32(ep.Hops),
			}
		}
		return &proto.TMEndpoints{Version: pb.Uint32(m.Version), EndpointsV2: eps}, nil

	case *Transaction:
		txStatus := proto.TransactionStatus(m.Status)
		return &proto.TMTransaction{
			RawTransaction:   m.RawTransaction,
			Status:           &txStatus,
			ReceiveTimestamp: m.ReceiveTimestamp,
			Deferred:         m.Deferred,
		}, nil

	case *Transactions:
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
			ProposeSeq:          pb.Uint32(m.ProposeSeq),
			CurrentTxHash:       m.CurrentTxHash,
			NodePubKey:          m.NodePubKey,
			CloseTime:           pb.Uint32(m.CloseTime),
			Signature:           m.Signature,
			PreviousLedger:      m.PreviousLedger,
			AddedTransactions:   m.AddedTransactions,
			RemovedTransactions: m.RemovedTransactions,
		}, nil

	case *HaveTransactionSet:
		txSetStatus := proto.TxSetStatus(m.Status)
		return &proto.TMHaveTransactionSet{
			Status: &txSetStatus,
			Hash:   m.Hash,
		}, nil

	case *Validation:
		return &proto.TMValidation{Validation: m.Validation}, nil

	case *ValidatorList:
		return &proto.TMValidatorList{
			Manifest:  m.Manifest,
			Blob:      m.Blob,
			Signature: m.Signature,
			Version:   pb.Uint32(m.Version),
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
			Version:  pb.Uint32(m.Version),
			Manifest: m.Manifest,
			Blobs:    blobs,
		}, nil

	case *GetLedger:
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

	case *LedgerData:
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
		objType := proto.ObjectType(m.ObjType)
		return &proto.TMGetObjectByHash{
			Type:       &objType,
			Query:      pb.Bool(m.Query),
			Seq:        m.Seq,
			LedgerHash: m.LedgerHash,
			Fat:        m.Fat,
			Objects:    objects,
		}, nil

	case *Squelch:
		return &proto.TMSquelch{
			Squelch:         pb.Bool(m.Squelch),
			ValidatorPubKey: m.ValidatorPubKey,
			SquelchDuration: m.SquelchDuration,
		}, nil

	case *ProofPathRequest:
		mapType := proto.TMLedgerMapType(m.MapType)
		return &proto.TMProofPathRequest{
			Key:        m.Key,
			LedgerHash: m.LedgerHash,
			Type:       &mapType,
		}, nil

	case *ProofPathResponse:
		mapType := proto.TMLedgerMapType(m.MapType)
		return &proto.TMProofPathResponse{
			Key:          m.Key,
			LedgerHash:   m.LedgerHash,
			Type:         &mapType,
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

	case TypeEndpoints:
		p := protoMsg.(*proto.TMEndpoints)
		eps := make([]Endpointv2, len(p.EndpointsV2))
		for i, ep := range p.EndpointsV2 {
			eps[i] = Endpointv2{
				Endpoint: ep.GetEndpoint(),
				Hops:     ep.GetHops(),
			}
		}
		return &Endpoints{Version: p.GetVersion(), EndpointsV2: eps}, nil

	case TypeTransaction:
		p := protoMsg.(*proto.TMTransaction)
		return &Transaction{
			RawTransaction:   p.GetRawTransaction(),
			Status:           TransactionStatus(p.GetStatus()),
			ReceiveTimestamp: p.GetReceiveTimestamp(),
			Deferred:         p.GetDeferred(),
		}, nil

	case TypeTransactions:
		p := protoMsg.(*proto.TMTransactions)
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

	case TypeGetLedger:
		p := protoMsg.(*proto.TMGetLedger)
		return &GetLedger{
			InfoType:      LedgerInfoType(p.GetItype()),
			LType:         LedgerType(p.GetLtype()),
			LedgerHash:    p.GetLedgerHash(),
			LedgerSeq:     p.GetLedgerSeq(),
			NodeIDs:       p.GetNodeIds(),
			RequestCookie: p.GetRequestCookie(),
			QueryDepth:    p.GetQueryDepth(),
		}, nil

	case TypeLedgerData:
		p := protoMsg.(*proto.TMLedgerData)
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

	case TypeProposeLedger:
		p := protoMsg.(*proto.TMProposeSet)
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

	case TypeStatusChange:
		p := protoMsg.(*proto.TMStatusChange)
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

	case TypeHaveSet:
		p := protoMsg.(*proto.TMHaveTransactionSet)
		return &HaveTransactionSet{
			Status: TxSetStatus(p.GetStatus()),
			Hash:   p.GetHash(),
		}, nil

	case TypeValidation:
		p := protoMsg.(*proto.TMValidation)
		return &Validation{Validation: p.GetValidation()}, nil

	case TypeGetObjects:
		p := protoMsg.(*proto.TMGetObjectByHash)
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

	case TypeValidatorList:
		p := protoMsg.(*proto.TMValidatorList)
		return &ValidatorList{
			Manifest:  p.GetManifest(),
			Blob:      p.GetBlob(),
			Signature: p.GetSignature(),
			Version:   p.GetVersion(),
		}, nil

	case TypeSquelch:
		p := protoMsg.(*proto.TMSquelch)
		return &Squelch{
			Squelch:         p.GetSquelch(),
			ValidatorPubKey: p.GetValidatorPubKey(),
			SquelchDuration: p.GetSquelchDuration(),
		}, nil

	case TypeValidatorListCollection:
		p := protoMsg.(*proto.TMValidatorListCollection)
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

	case TypeProofPathReq:
		p := protoMsg.(*proto.TMProofPathRequest)
		return &ProofPathRequest{
			Key:        p.GetKey(),
			LedgerHash: p.GetLedgerHash(),
			MapType:    LedgerMapType(p.GetType()),
		}, nil

	case TypeProofPathResponse:
		p := protoMsg.(*proto.TMProofPathResponse)
		return &ProofPathResponse{
			Key:          p.GetKey(),
			LedgerHash:   p.GetLedgerHash(),
			MapType:      LedgerMapType(p.GetType()),
			LedgerHeader: p.GetLedgerHeader(),
			Path:         p.GetPath(),
			Error:        ReplyError(p.GetError()),
		}, nil

	case TypeReplayDeltaReq:
		p := protoMsg.(*proto.TMReplayDeltaRequest)
		return &ReplayDeltaRequest{LedgerHash: p.GetLedgerHash()}, nil

	case TypeReplayDeltaResponse:
		p := protoMsg.(*proto.TMReplayDeltaResponse)
		return &ReplayDeltaResponse{
			LedgerHash:   p.GetLedgerHash(),
			LedgerHeader: p.GetLedgerHeader(),
			Transactions: p.GetTransaction(),
			Error:        ReplyError(p.GetError()),
		}, nil

	case TypeHaveTransactions:
		p := protoMsg.(*proto.TMHaveTransactions)
		return &HaveTransactions{Hashes: p.GetHashes()}, nil

	default:
		return nil, fmt.Errorf("unknown message type: %d", msgType)
	}
}

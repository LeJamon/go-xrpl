package message

import (
	"bytes"
	"reflect"
	"testing"
)

func TestPingRoundtrip(t *testing.T) {
	tests := []*Ping{
		{PType: PingTypePing, Seq: 1, PingTime: 1000},
		{PType: PingTypePong, Seq: 2, PingTime: 2000, NetTime: 3000},
		{PType: PingTypePing, Seq: 0, PingTime: 0},
	}

	for i, original := range tests {
		encoded, err := Encode(original)
		if err != nil {
			t.Errorf("Test %d: Encode error: %v", i, err)
			continue
		}
		msg, err := Decode(TypePing, encoded)
		if err != nil {
			t.Errorf("Test %d: Decode error: %v", i, err)
			continue
		}
		decoded := msg.(*Ping)

		if decoded.PType != original.PType {
			t.Errorf("Test %d: PType = %d, want %d", i, decoded.PType, original.PType)
		}
		if decoded.Seq != original.Seq {
			t.Errorf("Test %d: Seq = %d, want %d", i, decoded.Seq, original.Seq)
		}
		if decoded.PingTime != original.PingTime {
			t.Errorf("Test %d: PingTime = %d, want %d", i, decoded.PingTime, original.PingTime)
		}
		if decoded.NetTime != original.NetTime {
			t.Errorf("Test %d: NetTime = %d, want %d", i, decoded.NetTime, original.NetTime)
		}
	}
}

func TestManifestsRoundtrip(t *testing.T) {
	original := &Manifests{
		List: []Manifest{
			{STObject: []byte{1, 2, 3, 4}},
			{STObject: []byte{5, 6, 7, 8}},
		},
		History: true,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeManifests, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Manifests)

	if len(decoded.List) != len(original.List) {
		t.Fatalf("List length = %d, want %d", len(decoded.List), len(original.List))
	}

	for i := range original.List {
		if !bytes.Equal(decoded.List[i].STObject, original.List[i].STObject) {
			t.Errorf("Manifest %d STObject mismatch", i)
		}
	}

	if decoded.History != original.History {
		t.Errorf("History = %v, want %v", decoded.History, original.History)
	}
}

func TestEndpointsRoundtrip(t *testing.T) {
	original := &Endpoints{
		Version: 2,
		EndpointsV2: []Endpointv2{
			{Endpoint: "192.168.1.1:51235", Hops: 0},
			{Endpoint: "10.0.0.1:51235", Hops: 1},
			{Endpoint: "172.16.0.1:51235", Hops: 2},
		},
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeEndpoints, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Endpoints)

	if decoded.Version != original.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, original.Version)
	}

	if len(decoded.EndpointsV2) != len(original.EndpointsV2) {
		t.Fatalf("EndpointsV2 length = %d, want %d", len(decoded.EndpointsV2), len(original.EndpointsV2))
	}

	for i := range original.EndpointsV2 {
		if decoded.EndpointsV2[i].Endpoint != original.EndpointsV2[i].Endpoint {
			t.Errorf("Endpoint %d = %q, want %q", i, decoded.EndpointsV2[i].Endpoint, original.EndpointsV2[i].Endpoint)
		}
		if decoded.EndpointsV2[i].Hops != original.EndpointsV2[i].Hops {
			t.Errorf("Hops %d = %d, want %d", i, decoded.EndpointsV2[i].Hops, original.EndpointsV2[i].Hops)
		}
	}
}

func TestStatusChangeRoundtrip(t *testing.T) {
	first := uint32(100000)
	last := uint32(1000000)
	original := &StatusChange{
		NewStatus:          NodeStatusValidating,
		NewEvent:           NodeEventAcceptedLedger,
		LedgerSeq:          1000000,
		LedgerHash:         bytes.Repeat([]byte{0xAB}, 32),
		LedgerHashPrevious: bytes.Repeat([]byte{0xCD}, 32),
		NetworkTime:        1234567890,
		FirstSeq:           &first,
		LastSeq:            &last,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeStatusChange, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*StatusChange)

	if decoded.NewStatus != original.NewStatus {
		t.Errorf("NewStatus = %d, want %d", decoded.NewStatus, original.NewStatus)
	}
	if decoded.NewEvent != original.NewEvent {
		t.Errorf("NewEvent = %d, want %d", decoded.NewEvent, original.NewEvent)
	}
	if decoded.LedgerSeq != original.LedgerSeq {
		t.Errorf("LedgerSeq = %d, want %d", decoded.LedgerSeq, original.LedgerSeq)
	}
	if !bytes.Equal(decoded.LedgerHash, original.LedgerHash) {
		t.Error("LedgerHash mismatch")
	}
	if !bytes.Equal(decoded.LedgerHashPrevious, original.LedgerHashPrevious) {
		t.Error("LedgerHashPrevious mismatch")
	}
}

func TestTransactionRoundtrip(t *testing.T) {
	original := &Transaction{
		RawTransaction:   bytes.Repeat([]byte{0x12}, 200),
		Status:           TxStatusNew,
		ReceiveTimestamp: 1234567890,
		Deferred:         true,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeTransaction, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Transaction)

	if !bytes.Equal(decoded.RawTransaction, original.RawTransaction) {
		t.Error("RawTransaction mismatch")
	}
	if decoded.Status != original.Status {
		t.Errorf("Status = %d, want %d", decoded.Status, original.Status)
	}
	if decoded.ReceiveTimestamp != original.ReceiveTimestamp {
		t.Errorf("ReceiveTimestamp = %d, want %d", decoded.ReceiveTimestamp, original.ReceiveTimestamp)
	}
	if decoded.Deferred != original.Deferred {
		t.Errorf("Deferred = %v, want %v", decoded.Deferred, original.Deferred)
	}
}

func TestValidationRoundtrip(t *testing.T) {
	original := &Validation{
		Validation: bytes.Repeat([]byte{0x34}, 150),
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeValidation, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Validation)

	if !bytes.Equal(decoded.Validation, original.Validation) {
		t.Error("Validation mismatch")
	}
}

func TestSquelchRoundtrip(t *testing.T) {
	original := &Squelch{
		Squelch:         true,
		ValidatorPubKey: bytes.Repeat([]byte{0x56}, 33),
		SquelchDuration: 300,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeSquelch, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Squelch)

	if decoded.Squelch != original.Squelch {
		t.Errorf("Squelch = %v, want %v", decoded.Squelch, original.Squelch)
	}
	if !bytes.Equal(decoded.ValidatorPubKey, original.ValidatorPubKey) {
		t.Error("ValidatorPubKey mismatch")
	}
	if decoded.SquelchDuration != original.SquelchDuration {
		t.Errorf("SquelchDuration = %d, want %d", decoded.SquelchDuration, original.SquelchDuration)
	}
}

func TestHaveTransactionsRoundtrip(t *testing.T) {
	original := &HaveTransactions{
		Hashes: [][]byte{
			bytes.Repeat([]byte{0x11}, 32),
			bytes.Repeat([]byte{0x22}, 32),
			bytes.Repeat([]byte{0x33}, 32),
		},
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeHaveTransactions, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*HaveTransactions)

	if len(decoded.Hashes) != len(original.Hashes) {
		t.Fatalf("Hashes length = %d, want %d", len(decoded.Hashes), len(original.Hashes))
	}

	for i := range original.Hashes {
		if !bytes.Equal(decoded.Hashes[i], original.Hashes[i]) {
			t.Errorf("Hash %d mismatch", i)
		}
	}
}

func TestGetLedgerRoundtrip(t *testing.T) {
	original := &GetLedger{
		InfoType:      LedgerInfoBase,
		LType:         LedgerTypeAccepted,
		LedgerHash:    bytes.Repeat([]byte{0x78}, 32),
		LedgerSeq:     500000,
		NodeIDs:       [][]byte{bytes.Repeat([]byte{0x99}, 32)},
		RequestCookie: 12345,
		QueryDepth:    3,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeGetLedger, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*GetLedger)

	if decoded.InfoType != original.InfoType {
		t.Errorf("InfoType = %d, want %d", decoded.InfoType, original.InfoType)
	}
	if decoded.LType != original.LType {
		t.Errorf("LType = %d, want %d", decoded.LType, original.LType)
	}
	if !bytes.Equal(decoded.LedgerHash, original.LedgerHash) {
		t.Error("LedgerHash mismatch")
	}
	if decoded.LedgerSeq != original.LedgerSeq {
		t.Errorf("LedgerSeq = %d, want %d", decoded.LedgerSeq, original.LedgerSeq)
	}
	if decoded.RequestCookie != original.RequestCookie {
		t.Errorf("RequestCookie = %d, want %d", decoded.RequestCookie, original.RequestCookie)
	}
	if decoded.QueryDepth != original.QueryDepth {
		t.Errorf("QueryDepth = %d, want %d", decoded.QueryDepth, original.QueryDepth)
	}
}

func TestLedgerDataRoundtrip(t *testing.T) {
	original := &LedgerData{
		LedgerHash: bytes.Repeat([]byte{0xAA}, 32),
		LedgerSeq:  600000,
		InfoType:   LedgerInfoAsNode,
		Nodes: []LedgerNode{
			{NodeData: []byte{1, 2, 3}, NodeID: bytes.Repeat([]byte{0xBB}, 32)},
			{NodeData: []byte{4, 5, 6}, NodeID: bytes.Repeat([]byte{0xCC}, 32)},
		},
		RequestCookie: 54321,
		Error:         ReplyErrorNone,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeLedgerData, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*LedgerData)

	if !bytes.Equal(decoded.LedgerHash, original.LedgerHash) {
		t.Error("LedgerHash mismatch")
	}
	if decoded.LedgerSeq != original.LedgerSeq {
		t.Errorf("LedgerSeq = %d, want %d", decoded.LedgerSeq, original.LedgerSeq)
	}
	if len(decoded.Nodes) != len(original.Nodes) {
		t.Fatalf("Nodes length = %d, want %d", len(decoded.Nodes), len(original.Nodes))
	}
}

func TestClusterRoundtrip(t *testing.T) {
	original := &Cluster{
		ClusterNodes: []ClusterNode{
			{PublicKey: "nXXX...", ReportTime: 1000, NodeLoad: 50, NodeName: "node1", Address: "192.168.1.1:51235"},
			{PublicKey: "nYYY...", ReportTime: 2000, NodeLoad: 60, NodeName: "node2", Address: "192.168.1.2:51235"},
		},
		LoadSources: []LoadSource{
			{Name: "peer", Cost: 10, Count: 5},
			{Name: "rpc", Cost: 20, Count: 10},
		},
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeCluster, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*Cluster)

	if len(decoded.ClusterNodes) != len(original.ClusterNodes) {
		t.Fatalf("ClusterNodes length = %d, want %d", len(decoded.ClusterNodes), len(original.ClusterNodes))
	}

	for i := range original.ClusterNodes {
		if decoded.ClusterNodes[i].PublicKey != original.ClusterNodes[i].PublicKey {
			t.Errorf("Node %d PublicKey mismatch", i)
		}
		if decoded.ClusterNodes[i].NodeName != original.ClusterNodes[i].NodeName {
			t.Errorf("Node %d NodeName mismatch", i)
		}
	}

	if len(decoded.LoadSources) != len(original.LoadSources) {
		t.Fatalf("LoadSources length = %d, want %d", len(decoded.LoadSources), len(original.LoadSources))
	}
}

func TestProposeSetRoundtrip(t *testing.T) {
	original := &ProposeSet{
		ProposeSeq:          5,
		CurrentTxHash:       bytes.Repeat([]byte{0x11}, 32),
		NodePubKey:          bytes.Repeat([]byte{0x22}, 33),
		CloseTime:           1234567890,
		Signature:           bytes.Repeat([]byte{0x33}, 64),
		PreviousLedger:      bytes.Repeat([]byte{0x44}, 32),
		AddedTransactions:   [][]byte{{1, 2, 3}, {4, 5, 6}},
		RemovedTransactions: [][]byte{{7, 8, 9}},
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeProposeLedger, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*ProposeSet)

	if decoded.ProposeSeq != original.ProposeSeq {
		t.Errorf("ProposeSeq = %d, want %d", decoded.ProposeSeq, original.ProposeSeq)
	}
	if !bytes.Equal(decoded.CurrentTxHash, original.CurrentTxHash) {
		t.Error("CurrentTxHash mismatch")
	}
	if len(decoded.AddedTransactions) != len(original.AddedTransactions) {
		t.Errorf("AddedTransactions length = %d, want %d", len(decoded.AddedTransactions), len(original.AddedTransactions))
	}
	if len(decoded.RemovedTransactions) != len(original.RemovedTransactions) {
		t.Errorf("RemovedTransactions length = %d, want %d", len(decoded.RemovedTransactions), len(original.RemovedTransactions))
	}
}

func TestValidatorListRoundtrip(t *testing.T) {
	original := &ValidatorList{
		Manifest:  bytes.Repeat([]byte{0xAA}, 100),
		Blob:      bytes.Repeat([]byte{0xBB}, 500),
		Signature: bytes.Repeat([]byte{0xCC}, 64),
		Version:   1,
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	msg, err := Decode(TypeValidatorList, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decoded := msg.(*ValidatorList)

	if !bytes.Equal(decoded.Manifest, original.Manifest) {
		t.Error("Manifest mismatch")
	}
	if !bytes.Equal(decoded.Blob, original.Blob) {
		t.Error("Blob mismatch")
	}
	if !bytes.Equal(decoded.Signature, original.Signature) {
		t.Error("Signature mismatch")
	}
	if decoded.Version != original.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, original.Version)
	}
}

func TestGenericEncodeDecode(t *testing.T) {
	messages := []Message{
		&Ping{PType: PingTypePing, Seq: 1},
		&Manifests{History: true},
		&Endpoints{Version: 2},
		&StatusChange{NewStatus: NodeStatusConnected},
		&Transaction{RawTransaction: []byte{1, 2, 3}},
		&Validation{Validation: []byte{4, 5, 6}},
		&Squelch{Squelch: true, ValidatorPubKey: []byte{7, 8, 9}},
	}

	for _, msg := range messages {
		t.Run(reflect.TypeOf(msg).String(), func(t *testing.T) {
			encoded, err := Encode(msg)
			if err != nil {
				t.Fatalf("Encode error: %v", err)
			}

			decoded, err := Decode(msg.Type(), encoded)
			if err != nil {
				t.Fatalf("Decode error: %v", err)
			}

			if decoded.Type() != msg.Type() {
				t.Errorf("Type = %d, want %d", decoded.Type(), msg.Type())
			}
		})
	}
}

func TestDecodeUnknownType(t *testing.T) {
	_, err := Decode(MessageType(9999), []byte{})
	if err == nil {
		t.Error("Expected error for unknown message type")
	}
}

// unknownMsg is a test type that implements Message but is not handled by Encode
type unknownMsg struct{}

func (u *unknownMsg) Type() MessageType { return MessageType(9999) }

func TestEncodeUnknownType(t *testing.T) {
	_, err := Encode(&unknownMsg{})
	if err == nil {
		t.Error("Expected error for unknown message type")
	}
}

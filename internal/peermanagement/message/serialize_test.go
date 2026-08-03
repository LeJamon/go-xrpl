package message

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
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
	qt := QueryTypeIndirect
	original := &GetLedger{
		InfoType:      LedgerInfoBase,
		LType:         LedgerTypeAccepted,
		LedgerHash:    bytes.Repeat([]byte{0x78}, 32),
		LedgerSeq:     500000,
		NodeIDs:       [][]byte{bytes.Repeat([]byte{0x99}, 32)},
		RequestCookie: 12345,
		QueryType:     &qt,
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
	if decoded.QueryType == nil || *decoded.QueryType != *original.QueryType {
		t.Errorf("QueryType = %v, want %d", decoded.QueryType, *original.QueryType)
	}
	if decoded.QueryDepth != original.QueryDepth {
		t.Errorf("QueryDepth = %d, want %d", decoded.QueryDepth, original.QueryDepth)
	}
}

func TestGetLedgerQueryDepthPresence(t *testing.T) {
	tests := []struct {
		name    string
		request *GetLedger
		present bool
	}{
		{name: "absent", request: &GetLedger{InfoType: LedgerInfoBase}},
		{name: "explicit zero", request: &GetLedger{InfoType: LedgerInfoAsNode, QueryDepthSet: true}, present: true},
		{name: "nonzero", request: &GetLedger{InfoType: LedgerInfoAsNode, QueryDepth: 1}, present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := Encode(test.request)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			decodedMessage, err := Decode(TypeGetLedger, encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			decoded := decodedMessage.(*GetLedger)
			if decoded.HasQueryDepth() != test.present {
				t.Fatalf("HasQueryDepth=%t, want %t", decoded.HasQueryDepth(), test.present)
			}
			if decoded.QueryDepth != test.request.QueryDepth {
				t.Fatalf("QueryDepth=%d, want %d", decoded.QueryDepth, test.request.QueryDepth)
			}
		})
	}
}

func TestGetLedgerQueryTypePresence(t *testing.T) {
	t.Run("absent stays nil", func(t *testing.T) {
		encoded, err := Encode(&GetLedger{InfoType: LedgerInfoBase})
		if err != nil {
			t.Fatalf("Encode error: %v", err)
		}
		decoded, err := Decode(TypeGetLedger, encoded)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		if qt := decoded.(*GetLedger).QueryType; qt != nil {
			t.Errorf("QueryType = %d, want nil (absent)", *qt)
		}
	})

	t.Run("explicit indirect stays present", func(t *testing.T) {
		value := QueryTypeIndirect
		encoded, err := Encode(&GetLedger{InfoType: LedgerInfoBase, QueryType: &value})
		if err != nil {
			t.Fatalf("Encode error: %v", err)
		}
		decoded, err := Decode(TypeGetLedger, encoded)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		if qt := decoded.(*GetLedger).QueryType; qt == nil || *qt != value {
			t.Fatalf("QueryType = %v, want present qtINDIRECT", qt)
		}
	})

	t.Run("unknown wire value becomes absent", func(t *testing.T) {
		wire := []byte{0x08, 0x00, 0x38, 0x07}
		decoded, err := Decode(TypeGetLedger, wire)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		if qt := decoded.(*GetLedger).QueryType; qt != nil {
			t.Fatalf("QueryType = %d, want nil", *qt)
		}
	})

	t.Run("unknown outbound value is rejected", func(t *testing.T) {
		invalid := LedgerQueryType(7)
		if _, err := Encode(&GetLedger{InfoType: LedgerInfoBase, QueryType: &invalid}); err == nil {
			t.Fatal("Encode accepted an unknown optional enum")
		}
	})

	for _, test := range []struct {
		name string
		wire []byte
	}{
		{name: "valid then unknown", wire: []byte{0x08, 0x00, 0x38, 0x00, 0x38, 0x07}},
		{name: "unknown then valid", wire: []byte{0x08, 0x00, 0x38, 0x07, 0x38, 0x00}},
	} {
		t.Run(test.name+" preserves optional valid enum", func(t *testing.T) {
			decoded, err := Decode(TypeGetLedger, test.wire)
			if err != nil {
				t.Fatal(err)
			}
			queryType := decoded.(*GetLedger).QueryType
			if queryType == nil || *queryType != QueryTypeIndirect {
				t.Fatalf("QueryType = %v, want present qtINDIRECT", queryType)
			}
		})
	}
}

func TestDuplicateRequiredEnumKeepsLastRecognizedValue(t *testing.T) {
	for _, test := range []struct {
		name string
		wire []byte
	}{
		{name: "valid then unknown", wire: []byte{0x08, 0x00, 0x08, 0x07}},
		{name: "unknown then valid", wire: []byte{0x08, 0x07, 0x08, 0x00}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := Decode(TypePing, test.wire)
			if err != nil {
				t.Fatal(err)
			}
			if got := decoded.(*Ping).PType; got != PingTypePing {
				t.Fatalf("PType = %d, want ptPING", got)
			}
		})
	}

	t.Run("nested valid then unknown", func(t *testing.T) {
		transaction := protowire.AppendTag(nil, 1, protowire.BytesType)
		transaction = protowire.AppendBytes(transaction, nil)
		transaction = protowire.AppendTag(transaction, 2, protowire.VarintType)
		transaction = protowire.AppendVarint(transaction, uint64(TxStatusNew))
		transaction = protowire.AppendTag(transaction, 2, protowire.VarintType)
		transaction = protowire.AppendVarint(transaction, 99)
		wire := protowire.AppendTag(nil, 1, protowire.BytesType)
		wire = protowire.AppendBytes(wire, transaction)

		decoded, err := Decode(TypeTransactions, wire)
		if err != nil {
			t.Fatal(err)
		}
		transactions := decoded.(*Transactions).Transactions
		if len(transactions) != 1 || transactions[0].Status != TxStatusNew {
			t.Fatalf("Transactions = %+v, want one tsNEW", transactions)
		}
	})
}

func TestGetLedgerZeroValuePresence(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		encoded, err := Encode(&GetLedger{InfoType: LedgerInfoBase})
		if err != nil {
			t.Fatalf("Encode error: %v", err)
		}
		decoded, err := Decode(TypeGetLedger, encoded)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		got := decoded.(*GetLedger)
		if got.HasLedgerSeq() || got.HasRequestCookie() {
			t.Fatalf("zero-value fields unexpectedly present: seq=%v cookie=%v", got.HasLedgerSeq(), got.HasRequestCookie())
		}
	})

	t.Run("explicit zero", func(t *testing.T) {
		encoded, err := Encode(&GetLedger{
			InfoType:         LedgerInfoBase,
			LedgerSeqSet:     true,
			RequestCookieSet: true,
		})
		if err != nil {
			t.Fatalf("Encode error: %v", err)
		}
		decoded, err := Decode(TypeGetLedger, encoded)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		got := decoded.(*GetLedger)
		if !got.HasLedgerSeq() || !got.HasRequestCookie() {
			t.Fatalf("explicit zero presence lost: seq=%v cookie=%v", got.HasLedgerSeq(), got.HasRequestCookie())
		}
		if got.LedgerSeq != 0 || got.RequestCookie != 0 {
			t.Fatalf("explicit zero values changed: seq=%d cookie=%d", got.LedgerSeq, got.RequestCookie)
		}
	})

	t.Run("unknown ltype wire value becomes absent", func(t *testing.T) {
		decoded, err := Decode(TypeGetLedger, []byte{0x08, 0x00, 0x10, 0x07})
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		if got := decoded.(*GetLedger); got.HasLType() {
			t.Fatalf("LType = %d, want absent", got.LType)
		}
	})
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

func TestLedgerDataZeroCookiePresence(t *testing.T) {
	encoded, err := Encode(&LedgerData{
		InfoType:         LedgerInfoBase,
		RequestCookieSet: true,
	})
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	decoded, err := Decode(TypeLedgerData, encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	got := decoded.(*LedgerData)
	if !got.HasRequestCookie() || got.RequestCookie != 0 {
		t.Fatalf("explicit zero cookie presence lost: present=%v value=%d", got.HasRequestCookie(), got.RequestCookie)
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
		&Transaction{RawTransaction: []byte{1, 2, 3}, Status: TxStatusNew},
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

func TestDecodeRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		msgType MessageType
		wire    []byte
	}{
		{name: "top level", msgType: TypePing},
		{name: "nested", msgType: TypeManifests, wire: []byte{0x0a, 0x00}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.msgType, test.wire); err == nil {
				t.Fatal("expected missing required field error")
			}
		})
	}
}

func TestDecodeRejectsUnknownRequiredEnums(t *testing.T) {
	transaction := []byte{0x0a, 0x01, 0x01, 0x10, 0x63}
	tests := []struct {
		name    string
		msgType MessageType
		wire    []byte
	}{
		{name: "top level", msgType: TypeTransaction, wire: transaction},
		{name: "nested", msgType: TypeTransactions, wire: append([]byte{0x0a, byte(len(transaction))}, transaction...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.msgType, test.wire); err == nil {
				t.Fatal("expected unknown required enum error")
			} else if !strings.Contains(err.Error(), "required field") {
				t.Fatalf("error = %q, want required-field validation", err)
			}
		})
	}
}

func TestEncodeRejectsUnknownRequiredEnums(t *testing.T) {
	tests := []Message{
		&Transaction{Status: TransactionStatus(99)},
		&Transactions{Transactions: []Transaction{{Status: TransactionStatus(99)}}},
	}
	for _, message := range tests {
		if _, err := Encode(message); err == nil {
			t.Fatalf("Encode(%T) accepted an unknown required enum", message)
		} else if !strings.Contains(err.Error(), "invalid required enum") {
			t.Fatalf("Encode(%T) error = %q, want required enum validation", message, err)
		}
	}
}

func TestRequiredZeroValuesEncode(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		wire    []byte
	}{
		{name: "enum and bool", message: &GetObjectByHash{}, wire: []byte{0x08, 0x00, 0x10, 0x00}},
		{name: "empty bytes", message: &Validation{}, wire: []byte{0x0a, 0x00}},
		{name: "nested empty bytes", message: &Manifests{List: []Manifest{{}}}, wire: []byte{0x0a, 0x02, 0x0a, 0x00}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := Encode(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire, test.wire) {
				t.Fatalf("wire = %x, want %x", wire, test.wire)
			}
		})
	}
}

func TestGetLedgerLTypePresence(t *testing.T) {
	tests := []struct {
		name    string
		request *GetLedger
		present bool
	}{
		{name: "absent", request: &GetLedger{InfoType: LedgerInfoBase}},
		{name: "explicit zero", request: &GetLedger{InfoType: LedgerInfoBase, LTypeSet: true}, present: true},
		{name: "nonzero", request: &GetLedger{InfoType: LedgerInfoBase, LType: LedgerTypeClosed}, present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := Encode(test.request)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(TypeGetLedger, wire)
			if err != nil {
				t.Fatal(err)
			}
			request := decoded.(*GetLedger)
			if request.HasLType() != test.present || request.LTypeSet != test.present {
				t.Fatalf("ltype presence = %v/%v, want %v", request.HasLType(), request.LTypeSet, test.present)
			}
			if request.LType != test.request.LType {
				t.Fatalf("ltype = %d, want %d", request.LType, test.request.LType)
			}
		})
	}
}

func TestPingOptionalScalarPresence(t *testing.T) {
	tests := []struct {
		name string
		ping *Ping
		set  bool
	}{
		{name: "absent", ping: &Ping{PType: PingTypePing}},
		{name: "explicit zero", ping: &Ping{PType: PingTypePing, SeqSet: true, PingTimeSet: true, NetTimeSet: true}, set: true},
		{name: "nonzero", ping: &Ping{PType: PingTypePong, Seq: 1, PingTime: 2, NetTime: 3}, set: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := Encode(test.ping)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(TypePing, wire)
			if err != nil {
				t.Fatal(err)
			}
			ping := decoded.(*Ping)
			if ping.SeqSet != test.set || ping.PingTimeSet != test.set || ping.NetTimeSet != test.set {
				t.Fatalf("scalar presence = %v/%v/%v, want %v", ping.SeqSet, ping.PingTimeSet, ping.NetTimeSet, test.set)
			}
		})
	}
}

func TestStatusChangeOptionalScalarPresence(t *testing.T) {
	tests := []struct {
		name   string
		status *StatusChange
		set    bool
	}{
		{name: "absent", status: &StatusChange{}},
		{name: "explicit zero", status: &StatusChange{LedgerSeqSet: true, NetworkTimeSet: true}, set: true},
		{name: "nonzero", status: &StatusChange{NewStatus: NodeStatusConnected, NewEvent: NodeEventAcceptedLedger, LedgerSeq: 1, NetworkTime: 2}, set: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := Encode(test.status)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(TypeStatusChange, wire)
			if err != nil {
				t.Fatal(err)
			}
			status := decoded.(*StatusChange)
			wantEnums := test.status.NewStatus != 0
			if status.NewStatusSet != wantEnums || status.NewEventSet != wantEnums || status.LedgerSeqSet != test.set || status.NetworkTimeSet != test.set {
				t.Fatalf("presence = %v/%v/%v/%v, want enums=%v scalars=%v", status.NewStatusSet, status.NewEventSet, status.LedgerSeqSet, status.NetworkTimeSet, wantEnums, test.set)
			}
			if status.NewStatus != test.status.NewStatus || status.NewEvent != test.status.NewEvent {
				t.Fatalf("status/event = %d/%d, want %d/%d", status.NewStatus, status.NewEvent, test.status.NewStatus, test.status.NewEvent)
			}
		})
	}

	t.Run("unknown enum wire values become absent", func(t *testing.T) {
		decoded, err := Decode(TypeStatusChange, []byte{0x08, 0x00, 0x10, 0x00})
		if err != nil {
			t.Fatal(err)
		}
		status := decoded.(*StatusChange)
		if status.NewStatusSet || status.NewEventSet {
			t.Fatalf("unknown enum presence retained: status=%v event=%v", status.NewStatusSet, status.NewEventSet)
		}
	})
}

func TestReplyErrorPresence(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		msgType MessageType
	}{
		{name: "ledger data absent", message: &LedgerData{}, msgType: TypeLedgerData},
		{name: "ledger data present", message: &LedgerData{Error: ReplyErrorNoLedger}, msgType: TypeLedgerData},
		{name: "proof path absent", message: &ProofPathResponse{MapType: LedgerMapTransaction}, msgType: TypeProofPathResponse},
		{name: "proof path present", message: &ProofPathResponse{MapType: LedgerMapTransaction, Error: ReplyErrorNoNode}, msgType: TypeProofPathResponse},
		{name: "replay delta absent", message: &ReplayDeltaResponse{}, msgType: TypeReplayDeltaResponse},
		{name: "replay delta present", message: &ReplayDeltaResponse{Error: ReplyErrorBadRequest}, msgType: TypeReplayDeltaResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := Encode(test.message)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(test.msgType, wire)
			if err != nil {
				t.Fatal(err)
			}
			var set bool
			switch message := decoded.(type) {
			case *LedgerData:
				set = message.ErrorSet
			case *ProofPathResponse:
				set = message.ErrorSet
			case *ReplayDeltaResponse:
				set = message.ErrorSet
			}
			wantSet := strings.Contains(test.name, "present")
			if set != wantSet {
				t.Fatalf("error presence = %v, want %v", set, wantSet)
			}
		})
	}

	for _, message := range []Message{
		&LedgerData{ErrorSet: true},
		&ProofPathResponse{MapType: LedgerMapTransaction, ErrorSet: true},
		&ReplayDeltaResponse{ErrorSet: true},
	} {
		if _, err := Encode(message); err == nil {
			t.Fatalf("Encode(%T) accepted an unknown optional enum", message)
		}
	}

	for _, test := range []struct {
		name    string
		message Message
		msgType MessageType
		field   protowire.Number
	}{
		{name: "ledger data", message: &LedgerData{}, msgType: TypeLedgerData, field: 6},
		{name: "proof path", message: &ProofPathResponse{MapType: LedgerMapTransaction}, msgType: TypeProofPathResponse, field: 6},
		{name: "replay delta", message: &ReplayDeltaResponse{}, msgType: TypeReplayDeltaResponse, field: 4},
	} {
		t.Run(test.name+" unknown wire value becomes absent", func(t *testing.T) {
			wire, err := Encode(test.message)
			if err != nil {
				t.Fatal(err)
			}
			wire = protowire.AppendTag(wire, test.field, protowire.VarintType)
			wire = protowire.AppendVarint(wire, 0)
			decoded, err := Decode(test.msgType, wire)
			if err != nil {
				t.Fatal(err)
			}
			var set bool
			switch message := decoded.(type) {
			case *LedgerData:
				set = message.HasError()
			case *ProofPathResponse:
				set = message.HasError()
			case *ReplayDeltaResponse:
				set = message.HasError()
			}
			if set {
				t.Fatal("unknown optional enum remained present")
			}
		})
	}
}

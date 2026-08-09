package proto

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	pb "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSchemaProvenance(t *testing.T) {
	schema, err := os.ReadFile("xrpl.proto")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "d52b004814cbe9d243fd132166c18da7923ddf5608827277bf5928457ee4bbe5"
	if actual := fmt.Sprintf("%x", sha256.Sum256(schema)); actual != expected {
		t.Fatalf("xrpl.proto SHA256 = %s, want %s", actual, expected)
	}

	generated, err := os.ReadFile("xrpl.pb.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range [][]byte{
		[]byte("protoc-gen-go v1.36.11"),
		[]byte("protoc        v7.34.1"),
	} {
		if !bytes.Contains(generated, marker) {
			t.Fatalf("xrpl.pb.go does not contain %q", marker)
		}
	}
}

func TestSchemaDescriptorShape(t *testing.T) {
	file := File_internal_peermanagement_proto_xrpl_proto
	if file.Syntax() != protoreflect.Proto2 {
		t.Fatalf("syntax = %v, want proto2", file.Syntax())
	}

	messages, required, optional, repeated := descriptorCounts(file.Messages())
	if messages != 30 || required != 48 || optional != 43 || repeated != 14 {
		t.Fatalf("descriptor counts = messages:%d required:%d optional:%d repeated:%d, want 30/48/43/14", messages, required, optional, repeated)
	}

	endpoints := file.Messages().ByName("TMEndpoints")
	if endpoints == nil || !endpoints.ReservedRanges().Has(2) {
		t.Fatal("TMEndpoints must reserve field 2")
	}
	objects := file.Messages().ByName("TMGetObjectByHash")
	if objects == nil || !objects.ReservedRanges().Has(3) {
		t.Fatal("TMGetObjectByHash must reserve field 3")
	}
	if file.Messages().ByName("TMLink") == nil {
		t.Fatal("TMLink descriptor is missing")
	}
}

func descriptorCounts(messages protoreflect.MessageDescriptors) (messageCount, required, optional, repeated int) {
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		messageCount++
		fields := message.Fields()
		for j := 0; j < fields.Len(); j++ {
			switch fields.Get(j).Cardinality() {
			case protoreflect.Required:
				required++
			case protoreflect.Optional:
				optional++
			case protoreflect.Repeated:
				repeated++
			}
		}
		nestedMessages, nestedRequired, nestedOptional, nestedRepeated := descriptorCounts(message.Messages())
		messageCount += nestedMessages
		required += nestedRequired
		optional += nestedOptional
		repeated += nestedRepeated
	}
	return messageCount, required, optional, repeated
}

func TestSchemaEnums(t *testing.T) {
	file := File_internal_peermanagement_proto_xrpl_proto
	tests := []struct {
		descriptor protoreflect.EnumDescriptor
		fullName   protoreflect.FullName
		values     map[protoreflect.Name]protoreflect.EnumNumber
	}{
		{file.Enums().ByName("MessageType"), "protocol.MessageType", map[protoreflect.Name]protoreflect.EnumNumber{"mtMANIFESTS": 2, "mtPING": 3, "mtCLUSTER": 5, "mtENDPOINTS": 15, "mtTRANSACTION": 30, "mtGET_LEDGER": 31, "mtLEDGER_DATA": 32, "mtPROPOSE_LEDGER": 33, "mtSTATUS_CHANGE": 34, "mtHAVE_SET": 35, "mtVALIDATION": 41, "mtGET_OBJECTS": 42, "mtVALIDATOR_LIST": 54, "mtSQUELCH": 55, "mtVALIDATOR_LIST_COLLECTION": 56, "mtPROOF_PATH_REQ": 57, "mtPROOF_PATH_RESPONSE": 58, "mtREPLAY_DELTA_REQ": 59, "mtREPLAY_DELTA_RESPONSE": 60, "mtHAVE_TRANSACTIONS": 63, "mtTRANSACTIONS": 64}},
		{file.Enums().ByName("TransactionStatus"), "protocol.TransactionStatus", map[protoreflect.Name]protoreflect.EnumNumber{"tsNEW": 1, "tsCURRENT": 2, "tsCOMMITTED": 3, "tsREJECT_CONFLICT": 4, "tsREJECT_INVALID": 5, "tsREJECT_FUNDS": 6, "tsHELD_SEQ": 7, "tsHELD_LEDGER": 8}},
		{file.Enums().ByName("NodeStatus"), "protocol.NodeStatus", map[protoreflect.Name]protoreflect.EnumNumber{"nsCONNECTING": 1, "nsCONNECTED": 2, "nsMONITORING": 3, "nsVALIDATING": 4, "nsSHUTTING": 5}},
		{file.Enums().ByName("NodeEvent"), "protocol.NodeEvent", map[protoreflect.Name]protoreflect.EnumNumber{"neCLOSING_LEDGER": 1, "neACCEPTED_LEDGER": 2, "neSWITCHED_LEDGER": 3, "neLOST_SYNC": 4}},
		{file.Enums().ByName("TxSetStatus"), "protocol.TxSetStatus", map[protoreflect.Name]protoreflect.EnumNumber{"tsHAVE": 1, "tsCAN_GET": 2, "tsNEED": 3}},
		{file.Enums().ByName("TMLedgerInfoType"), "protocol.TMLedgerInfoType", map[protoreflect.Name]protoreflect.EnumNumber{"liBASE": 0, "liTX_NODE": 1, "liAS_NODE": 2, "liTS_CANDIDATE": 3}},
		{file.Enums().ByName("TMLedgerType"), "protocol.TMLedgerType", map[protoreflect.Name]protoreflect.EnumNumber{"ltACCEPTED": 0, "ltCURRENT": 1, "ltCLOSED": 2}},
		{file.Enums().ByName("TMQueryType"), "protocol.TMQueryType", map[protoreflect.Name]protoreflect.EnumNumber{"qtINDIRECT": 0}},
		{file.Enums().ByName("TMReplyError"), "protocol.TMReplyError", map[protoreflect.Name]protoreflect.EnumNumber{"reNO_LEDGER": 1, "reNO_NODE": 2, "reBAD_REQUEST": 3}},
		{file.Enums().ByName("TMLedgerMapType"), "protocol.TMLedgerMapType", map[protoreflect.Name]protoreflect.EnumNumber{"lmTRANSACTION": 1, "lmACCOUNT_STATE": 2}},
		{file.Messages().ByName("TMGetObjectByHash").Enums().ByName("ObjectType"), "protocol.TMGetObjectByHash.ObjectType", map[protoreflect.Name]protoreflect.EnumNumber{"otUNKNOWN": 0, "otLEDGER": 1, "otTRANSACTION": 2, "otTRANSACTION_NODE": 3, "otSTATE_NODE": 4, "otCAS_OBJECT": 5, "otFETCH_PACK": 6, "otTRANSACTIONS": 7}},
		{file.Messages().ByName("TMPing").Enums().ByName("pingType"), "protocol.TMPing.pingType", map[protoreflect.Name]protoreflect.EnumNumber{"ptPING": 0, "ptPONG": 1}},
	}

	for _, test := range tests {
		if test.descriptor == nil {
			t.Fatalf("enum %s is missing", test.fullName)
		}
		if test.descriptor.FullName() != test.fullName {
			t.Errorf("enum full name = %s, want %s", test.descriptor.FullName(), test.fullName)
		}
		if !test.descriptor.IsClosed() {
			t.Errorf("enum %s is not closed", test.fullName)
		}
		if test.descriptor.Values().Len() != len(test.values) {
			t.Errorf("enum %s has %d values, want %d", test.fullName, test.descriptor.Values().Len(), len(test.values))
		}
		for name, number := range test.values {
			value := test.descriptor.Values().ByName(name)
			if value == nil || value.Number() != number {
				t.Errorf("enum %s value %s = %v, want %d", test.fullName, name, value, number)
			}
		}
	}
}

func TestRequiredFieldsUseDefaultMarshalValidation(t *testing.T) {
	tests := []struct {
		name    string
		marshal pb.Message
		wire    []byte
		decode  pb.Message
	}{
		{name: "marshal", marshal: &TMManifest{}},
		{name: "marshal nested", marshal: &TMManifests{List: []*TMManifest{{}}}},
		{name: "unmarshal", wire: nil, decode: &TMManifest{}},
		{name: "unmarshal nested", wire: []byte{0x0a, 0x00}, decode: &TMManifests{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.marshal != nil {
				_, err = pb.Marshal(test.marshal)
			} else {
				err = pb.Unmarshal(test.wire, test.decode)
			}
			if err == nil {
				t.Fatal("expected missing required field error")
			}
		})
	}
}

func TestRequiredZeroValuesMarshal(t *testing.T) {
	objectType := TMGetObjectByHash_otUNKNOWN
	message := &TMGetObjectByHash{Type: &objectType, Query: pb.Bool(false)}
	wire, err := pb.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, []byte{0x08, 0x00, 0x10, 0x00}) {
		t.Fatalf("wire = %x, want 08001000", wire)
	}
}

func TestGeneratedAPIGolden(t *testing.T) {
	_ = &TMLink{NodePubKey: []byte{}}
	_ = &TMProposeSet{Previousledger: []byte{}}
	_ = &TMIndexedObject{NodeID: []byte{}}
	_ = &TMGetLedger{NodeIDs: [][]byte{}}
	var _ TMGetObjectByHash_ObjectType = TMGetObjectByHash_otUNKNOWN
	var _ TMPingPingType = TMPing_ptPING
}

package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestTrafficCounterBasic(t *testing.T) {
	tc := NewTrafficCounter()

	// Add some inbound traffic
	tc.AddCount(CategoryTransaction, true, 100)
	tc.AddCount(CategoryTransaction, true, 200)

	// Add some outbound traffic
	tc.AddCount(CategoryTransaction, false, 150)

	stats := tc.Stats(CategoryTransaction)
	if stats == nil {
		t.Fatal("Stats should not be nil")
	}

	if stats.BytesIn != 300 {
		t.Errorf("Expected BytesIn 300, got %d", stats.BytesIn)
	}

	if stats.BytesOut != 150 {
		t.Errorf("Expected BytesOut 150, got %d", stats.BytesOut)
	}

	if stats.MessagesIn != 2 {
		t.Errorf("Expected MessagesIn 2, got %d", stats.MessagesIn)
	}

	if stats.MessagesOut != 1 {
		t.Errorf("Expected MessagesOut 1, got %d", stats.MessagesOut)
	}
}

func TestTrafficCounterCategorize(t *testing.T) {
	tests := []struct {
		msgType  message.MessageType
		expected TrafficCategory
	}{
		{message.TypePing, CategoryBase},
		{message.TypeCluster, CategoryUnknown},
		{message.TypeEndpoints, CategoryOverlay},
		{message.TypeManifests, CategoryManifests},
		{message.TypeTransaction, CategoryTransaction},
		{message.TypeValidation, CategoryValidation},
		{message.TypeSquelch, CategorySquelch},
		{message.MessageType(999), CategoryUnknown},
	}

	for _, tc := range tests {
		result := CategorizeMessage(tc.msgType)
		if result != tc.expected {
			t.Errorf("CategorizeMessage(%d) = %v, expected %v", tc.msgType, result, tc.expected)
		}
	}
}

func TestTrafficCategories(t *testing.T) {
	check := func(msg message.Message, wantIn, wantOut string) {
		t.Helper()
		payload, err := message.Encode(msg)
		require.NoError(t, err)
		require.Equal(t, wantIn, categorizeMessagePayload(msg.Type(), payload, true).String(), "%s inbound", msg.Type())
		require.Equal(t, wantOut, categorizeMessagePayload(msg.Type(), payload, false).String(), "%s outbound", msg.Type())
	}
	check(&message.HaveTransactionSet{Status: message.TxSetStatusHave}, "set_get", "set_share")

	for _, tc := range []struct {
		subtype    message.ObjectType
		get, share string
	}{
		{message.ObjectTypeUnknown, "getobject_get", "getobject_share"},
		{message.ObjectTypeLedger, "getobject_Ledger_get", "getobject_Ledger_share"},
		{message.ObjectTypeTransaction, "getobject_Transaction_get", "getobject_Transaction_share"},
		{message.ObjectTypeTransactionNode, "getobject_Transaction_node_get", "getobject_Transaction_node_share"},
		{message.ObjectTypeStateNode, "getobject_Account_State_node_get", "getobject_Account_State_node_share"},
		{message.ObjectTypeCasObject, "getobject_CAS_get", "getobject_CAS_share"},
		{message.ObjectTypeFetchPack, "getobject_Fetch Pack_get", "getobject_Fetch_Pack_share"},
		{message.ObjectTypeTransactions, "getobject_Transactions_get", "getobject_Transactions_get"},
	} {
		check(&message.GetObjectByHash{ObjType: tc.subtype, Query: true}, tc.share, tc.get)
		check(&message.GetObjectByHash{ObjType: tc.subtype, Query: false}, tc.get, tc.share)
	}
	for _, tc := range []struct {
		subtype       message.LedgerInfoType
		request, data string
	}{
		{message.LedgerInfoBase, "ledger", "ledger_data"},
		{message.LedgerInfoTxNode, "ledger_Transaction_Node", "ledger_data_Transaction_Node"},
		{message.LedgerInfoAsNode, "ledger_Account_State_Node", "ledger_data_Account_State_Node"},
		{message.LedgerInfoTsCandidate, "ledger_Transaction_Set_candidate", "ledger_data_Transaction_Set_candidate"},
	} {
		req := &message.GetLedger{InfoType: tc.subtype}
		data := &message.LedgerData{InfoType: tc.subtype}
		check(req, tc.request+"_share", tc.request+"_get")
		check(data, tc.data+"_get", tc.data+"_share")
		req.RequestCookieSet = true
		data.RequestCookieSet = true
		check(req, tc.request+"_share", tc.request+"_share")
		check(data, tc.data+"_share", tc.data+"_share")
	}

	// An unknown enum occurrence following a recognized one is ignored by proto2.
	require.Equal(t, CategoryShareHashLedger, categorizeMessagePayload(message.TypeGetObjects, []byte{0x08, 1, 0x08, 99, 0x10, 1}, true))
	require.Equal(t, CategoryGetLedgerTxNodeGet, categorizeMessagePayload(message.TypeGetLedger, []byte{0x08, 1, 0x08, 99}, false))
	require.Equal(t, CategoryUnknown, categorizeMessagePayload(message.TypeGetLedger, []byte{0x08, 99}, true))
}

func TestOutboundTrafficCategoryCompressedLedgerData(t *testing.T) {
	frame, err := message.EncodeFrame(&message.LedgerData{
		InfoType: message.LedgerInfoAsNode,
		Nodes:    []message.LedgerNode{{NodeData: make([]byte, 8192)}},
	})
	require.NoError(t, err)
	compressed, ok := message.CompressFrameIfWorthwhile(frame)
	require.True(t, ok)
	for _, wire := range [][]byte{frame, compressed} {
		h, err := message.DecodeHeader(wire)
		require.NoError(t, err)
		require.Equal(t, CategoryLedgerDataStateNodeShare, outboundTrafficCategory(h, wire))
	}
}

func TestTrafficCounterReset(t *testing.T) {
	tc := NewTrafficCounter()

	tc.AddCount(CategoryTransaction, true, 100)
	tc.Reset()

	stats := tc.Stats(CategoryTransaction)
	if stats.BytesIn != 0 {
		t.Errorf("Expected BytesIn 0 after reset, got %d", stats.BytesIn)
	}
}

func TestTrafficCategoryString(t *testing.T) {
	tests := []struct {
		cat      TrafficCategory
		expected string
	}{
		{CategoryBase, "overhead"},
		{CategoryTransaction, "transactions"},
		{CategoryValidation, "validations"},
		{CategoryProofPathRequest, "proof_path_request"},
		{CategoryProofPathResponse, "proof_path_response"},
		{CategoryReplayDeltaRequest, "replay_delta_request"},
		{CategoryReplayDeltaResponse, "replay_delta_response"},
		{CategoryHaveTransactions, "have_transactions"},
		{CategoryRequestedTransactions, "requested_transactions"},
		{CategoryTotal, "total"},
		{CategoryUnknown, "unknown"},
	}

	for _, tc := range tests {
		if tc.cat.String() != tc.expected {
			t.Errorf("Category(%d).String() = %s, expected %s", tc.cat, tc.cat.String(), tc.expected)
		}
	}
}

func TestTrafficCounterTotalStats(t *testing.T) {
	tc := NewTrafficCounter()

	// Add traffic to different categories
	tc.AddCount(CategoryTransaction, true, 100)
	tc.AddCount(CategoryValidation, true, 50)
	tc.AddCount(CategoryTransaction, false, 75)

	// Total should aggregate all categories
	total := tc.TotalStats()
	if total == nil {
		t.Fatal("Total stats should not be nil")
	}

	if total.BytesIn != 150 {
		t.Errorf("Expected total BytesIn 150, got %d", total.BytesIn)
	}

	if total.BytesOut != 75 {
		t.Errorf("Expected total BytesOut 75, got %d", total.BytesOut)
	}

	if total.MessagesIn != 2 {
		t.Errorf("Expected total MessagesIn 2, got %d", total.MessagesIn)
	}
}

package rpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// The WS path_find session must enforce the same post-parse amount guards as
// rippled's PathRequest::parseJson (and as the ripple_path_find RPC handler):
// a non-convert-all destination_amount must be > 0, send_max requires
// convert-all (destination_amount == -1), and a send_max must be > 0 unless it
// is itself -1.
func TestParseAndCreateSession_AmountGuards(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	params := func(dstAmt, sendMax string) json.RawMessage {
		s := `{"source_account":"` + acct + `","destination_account":"` + acct +
			`","destination_amount":` + dstAmt
		if sendMax != "" {
			s += `,"send_max":` + sendMax
		}
		return json.RawMessage(s + `}`)
	}

	reject := []struct {
		name    string
		dstAmt  string
		sendMax string
		token   string
	}{
		{"negative destination_amount", `"-5"`, "", "dstAmtMalformed"},
		{"zero destination_amount", `"0"`, "", "dstAmtMalformed"},
		{"send_max without convert-all", `"1000000"`, `"5"`, "dstAmtMalformed"},
		{"non-positive send_max with convert-all", `"-1"`, `"0"`, "sendMaxMalformed"},
	}
	for _, tt := range reject {
		t.Run(tt.name, func(t *testing.T) {
			_, rpcErr := ParseAndCreateSession(params(tt.dstAmt, tt.sendMax), nil)
			if rpcErr == nil || rpcErr.ErrorString != tt.token {
				t.Fatalf("got %v, want %s", rpcErr, tt.token)
			}
		})
	}

	accept := []struct {
		name    string
		dstAmt  string
		sendMax string
	}{
		{"positive destination_amount", `"1000000"`, ""},
		{"convert-all with positive send_max", `"-1"`, `"10"`},
	}
	for _, tt := range accept {
		t.Run(tt.name, func(t *testing.T) {
			session, rpcErr := ParseAndCreateSession(params(tt.dstAmt, tt.sendMax), nil)
			if rpcErr != nil {
				t.Fatalf("unexpected error: %v", rpcErr)
			}
			if session == nil {
				t.Fatal("want session, got nil")
			}
		})
	}
}

func TestParseAndCreateSession_MPTAssets(t *testing.T) {
	const (
		acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		id   = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	)
	params := json.RawMessage(`{
		"source_account":"` + acct + `",
		"destination_account":"` + acct + `",
		"destination_amount":{"mpt_issuance_id":"` + id + `","value":"10"},
		"source_currencies":[{"mpt_issuance_id":"` + id + `"}]
	}`)

	session, rpcErr := ParseAndCreateSession(params, nil)
	require.Nil(t, rpcErr)
	require.True(t, session.dstAmount.IsMPT())
	require.Len(t, session.srcCurrencies, 1)
	decoded, err := mptutil.DecodeID(id)
	require.NoError(t, err)
	require.True(t, session.srcCurrencies[0].Equal(payment.NewMPTIssue(decoded)))
}

func TestParseAndCreateSession_MPTAssetMemberValidation(t *testing.T) {
	const (
		acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		id   = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	)
	for _, sourceCurrencies := range []string{
		`[]`,
		`[{"mpt_issuance_id":"` + id + `","currency":null}]`,
		`[{"mpt_issuance_id":"` + id + `","issuer":null}]`,
	} {
		params := json.RawMessage(`{
			"source_account":"` + acct + `",
			"destination_account":"` + acct + `",
			"destination_amount":{"mpt_issuance_id":"` + id + `","value":"10"},
			"source_currencies":` + sourceCurrencies + `
		}`)
		_, rpcErr := ParseAndCreateSession(params, nil)
		require.NotNil(t, rpcErr)
		require.Equal(t, "srcCurMalformed", rpcErr.ErrorString)
	}
}

func TestParseAndCreateSession_RejectsMPTWithZeroIssuer(t *testing.T) {
	const (
		acct  = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		badID = "000000010000000000000000000000000000000000000000"
	)
	badAmount := `{"mpt_issuance_id":"` + badID + `","value":"10"}`

	tests := []struct {
		name   string
		params json.RawMessage
		token  string
	}{
		{
			name: "destination_amount",
			params: json.RawMessage(`{"source_account":"` + acct +
				`","destination_account":"` + acct +
				`","destination_amount":` + badAmount + `}`),
			token: "dstAmtMalformed",
		},
		{
			name: "send_max",
			params: json.RawMessage(`{"source_account":"` + acct +
				`","destination_account":"` + acct +
				`","destination_amount":"-1","send_max":` + badAmount + `}`),
			token: "sendMaxMalformed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := ParseAndCreateSession(test.params, nil)
			require.NotNil(t, rpcErr)
			require.Equal(t, test.token, rpcErr.ErrorString)
		})
	}
}

func TestParseAndCreateSession_MPTSendMaxIssuerReconciliation(t *testing.T) {
	var issuerID, holderID [20]byte
	issuerID[19] = 1
	holderID[19] = 2
	issuer := state.EncodeAccountIDSafe(issuerID)
	holder := state.EncodeAccountIDSafe(holderID)
	id := mptutil.EncodeID(keylet.MakeMPTID(1, issuerID))

	params := func(source string) json.RawMessage {
		return json.RawMessage(`{"source_account":"` + source +
			`","destination_account":"` + issuer +
			`","destination_amount":"-1","send_max":{"mpt_issuance_id":"` + id +
			`","value":"10"},"source_currencies":[{"mpt_issuance_id":"` + id + `"}]}`)
	}

	_, rpcErr := ParseAndCreateSession(params(holder), nil)
	require.NotNil(t, rpcErr)
	require.Equal(t, "srcIsrMalformed", rpcErr.ErrorString)

	session, rpcErr := ParseAndCreateSession(params(issuer), nil)
	require.Nil(t, rpcErr)
	require.Len(t, session.srcCurrencies, 1)
	require.True(t, session.srcCurrencies[0].IsMPT)
}

func TestParseAndCreateSession_AcceptsZeroMPTSourceLiteral(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	params := json.RawMessage(`{"source_account":"` + acct +
		`","destination_account":"` + acct +
		`","destination_amount":"1","source_currencies":[{"mpt_issuance_id":"0"}]}`)

	session, rpcErr := ParseAndCreateSession(params, nil)
	require.Nil(t, rpcErr)
	require.Len(t, session.srcCurrencies, 1)
	require.True(t, session.srcCurrencies[0].IsMPT)
	require.Equal(t, [24]byte{}, session.srcCurrencies[0].MPTID)
}

func TestPathFindPersistentConvertAllResponseShape(t *testing.T) {
	const id = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	session := &PathFindSession{
		convertAll: true,
		dstAmount:  state.NewMPTAmountWithIssuanceID(-1, "", id),
	}
	result := &pathfinder.PathRequestResult{
		DestinationCurrencies: []string{"USD", "XRP"},
		Alternatives: []pathfinder.PathAlternative{
			{
				SourceAmount:      state.NewXRPAmountFromInt(10),
				DestinationAmount: state.NewMPTAmountWithIssuanceID(25, "", id),
			},
		},
	}

	encoded, err := json.Marshal(session.buildEvent(result, true))
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(encoded, &response))
	require.NotContains(t, response, "destination_currencies")
	topLevelDestination, ok := response["destination_amount"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "-1", topLevelDestination["value"])

	alternatives, ok := response["alternatives"].([]any)
	require.True(t, ok)
	require.Len(t, alternatives, 1)
	alternative, ok := alternatives[0].(map[string]any)
	require.True(t, ok)
	destinationAmount, ok := alternative["destination_amount"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, id, destinationAmount["mpt_issuance_id"])
	require.Equal(t, "25", destinationAmount["value"])
}

func TestPathFindPersistentResponseStateTransitions(t *testing.T) {
	const (
		account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		id      = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	)
	session := &PathFindSession{
		convertAll:    true,
		dstAmount:     state.NewMPTAmountWithIssuanceID(-1, "", strings.ToLower(id)),
		srcAccountStr: account,
		dstAccountStr: account,
		id:            "request-1",
	}
	result := &pathfinder.PathRequestResult{
		Alternatives: []pathfinder.PathAlternative{{
			SourceAmount:      state.NewXRPAmountFromInt(10),
			DestinationAmount: state.NewMPTAmountWithIssuanceID(25, "", id),
		}},
	}

	create := session.storeResultLocked(result, false)
	require.False(t, create.FullReply)
	require.Empty(t, create.Type)
	require.Empty(t, create.Status)
	require.False(t, create.Closed)
	var createJSON map[string]any
	require.NoError(t, json.Unmarshal(mustMarshalPathFindEvent(t, create), &createJSON))
	require.NotContains(t, createJSON, "type")
	destination := createJSON["destination_amount"].(map[string]any)
	require.Equal(t, id, destination["mpt_issuance_id"])
	require.Equal(t, "-1", destination["value"])
	fastStatus := session.Status()
	require.False(t, fastStatus.FullReply)
	require.Equal(t, "success", fastStatus.Status)

	update := session.storeResultLocked(result, true)
	require.True(t, update.FullReply)
	require.Equal(t, "path_find", update.Type)
	require.Empty(t, session.lastStatus.Type)

	status := session.Status()
	require.True(t, status.FullReply)
	require.Empty(t, status.Type)
	require.Equal(t, "success", status.Status)
	require.False(t, status.Closed)

	closed := session.Close()
	require.True(t, closed.FullReply)
	require.Empty(t, closed.Type)
	require.Equal(t, "success", closed.Status)
	require.True(t, closed.Closed)
}

func TestPathFindCloseDoesNotWaitForInFlightExecution(t *testing.T) {
	session := &PathFindSession{}
	executeStarted := make(chan struct{})
	releaseExecute := make(chan struct{})
	executeDone := make(chan struct{})

	go func() {
		defer close(executeDone)
		session.executeAndStore(func() *pathfinder.PathRequestResult {
			close(executeStarted)
			<-releaseExecute
			return &pathfinder.PathRequestResult{}
		}, true)
	}()
	<-executeStarted

	closeResult := make(chan *PathFindEvent, 1)
	go func() {
		closeResult <- session.Close()
	}()

	select {
	case event := <-closeResult:
		require.True(t, event.Closed)
	case <-time.After(time.Second):
		t.Fatal("Close waited for the in-flight pathfinding calculation")
	}

	close(releaseExecute)
	select {
	case <-executeDone:
	case <-time.After(time.Second):
		t.Fatal("pathfinding calculation did not finish after release")
	}
}

func mustMarshalPathFindEvent(t *testing.T, event *PathFindEvent) []byte {
	t.Helper()
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	return encoded
}

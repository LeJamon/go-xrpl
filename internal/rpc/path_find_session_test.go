package rpc

import (
	"encoding/json"
	"testing"
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

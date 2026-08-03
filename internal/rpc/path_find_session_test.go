package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
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

func TestParseAndCreateSession_DecodeErrorIsFixed(t *testing.T) {
	_, rpcErr := ParseAndCreateSession(json.RawMessage(`{"private":"decoder-detail"`), nil)
	if rpcErr == nil {
		t.Fatal("expected invalid parameters error")
	}
	if rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Invalid parameters." {
		t.Fatalf("error = %#v, want fixed invalidParams message", rpcErr)
	}
}

func TestParseAndCreateSession_Domain(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const domain = "AB"
	base := `{"source_account":"` + acct + `","destination_account":"` + acct + `","destination_amount":"1000000"`

	params := func(value string) json.RawMessage {
		return json.RawMessage(base + `,"domain":` + value + `}`)
	}

	t.Run("absent is unrestricted", func(t *testing.T) {
		session, rpcErr := ParseAndCreateSession(json.RawMessage(base+`}`), nil)
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if session.domainID != nil {
			t.Fatalf("domainID = %v, want nil", session.domainID)
		}
	})

	for _, test := range []struct {
		name  string
		value string
		want  *[32]byte
	}{
		{name: `"0" uses zero-value uint256 exception`, value: `"0"`, want: &[32]byte{}},
		{name: "full hex domain", value: `"` + strings.Repeat(domain, 32) + `"`, want: func() *[32]byte {
			var id [32]byte
			for i := range id {
				id[i] = 0xab
			}
			return &id
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, rpcErr := ParseAndCreateSession(params(test.value), nil)
			if rpcErr != nil {
				t.Fatalf("unexpected error: %v", rpcErr)
			}
			if session.domainID == nil {
				t.Fatal("domainID = nil")
			}
			if *session.domainID != *test.want {
				t.Fatalf("domainID = %x, want %x", session.domainID, test.want)
			}
		})
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "short hex", value: `"` + strings.Repeat("AB", 31) + `"`},
		{name: "long hex", value: `"` + strings.Repeat("AB", 33) + `"`},
		{name: "invalid hex", value: `"not-hex"`},
		{name: "number", value: `12345`},
		{name: "boolean", value: `true`},
		{name: "null", value: `null`},
		{name: "object", value: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, rpcErr := ParseAndCreateSession(params(test.value), nil)
			if session != nil {
				t.Fatal("got session for malformed domain")
			}
			if rpcErr == nil {
				t.Fatal("expected domainMalformed error")
			}
			if rpcErr.Code != rpctypes.RpcDOMAIN_MALFORMED || rpcErr.ErrorString != "domainMalformed" || rpcErr.Message != "Domain is malformed." {
				t.Fatalf("error = %#v, want rpcDOMAIN_MALFORMED/domainMalformed/Domain is malformed.", rpcErr)
			}
		})
	}
}

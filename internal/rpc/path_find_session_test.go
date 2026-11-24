package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
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
			if rpcErr.Code != rpcerrors.RpcDOMAIN_MALFORMED || rpcErr.ErrorString != "domainMalformed" || rpcErr.Message != "Domain is malformed." {
				t.Fatalf("error = %#v, want rpcDOMAIN_MALFORMED/domainMalformed/Domain is malformed.", rpcErr)
			}
		})
	}
}

// Missing or malformed accounts and a missing destination_amount carry
// rippled's PathRequest::parseJson error codes, as ripple_path_find does.
func TestParseAndCreateSession_RequiredFieldErrors(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	tests := []struct {
		name    string
		params  string
		code    int
		token   string
		message string
	}{
		{"missing source", `{"destination_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcSRC_ACT_MISSING, "srcActMissing", "Source account not provided."},
		{"missing destination", `{"source_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcDST_ACT_MISSING, "dstActMissing", "Destination account not provided."},
		{"missing amount", `{"source_account":"` + acct + `","destination_account":"` + acct + `"}`, rpcerrors.RpcDST_AMT_MISSING, "dstAmtMissing", "Destination amount/currency/issuer is missing."},
		{"empty source", `{"source_account":"","destination_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcSRC_ACT_MALFORMED, "srcActMalformed", "Source account is malformed."},
		{"bad source", `{"source_account":"bad","destination_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcSRC_ACT_MALFORMED, "srcActMalformed", "Source account is malformed."},
		{"numeric source", `{"source_account":5,"destination_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcSRC_ACT_MALFORMED, "srcActMalformed", "Source account is malformed."},
		{"bad destination", `{"source_account":"` + acct + `","destination_account":"bad","destination_amount":"1000"}`, rpcerrors.RpcDST_ACT_MALFORMED, "dstActMalformed", "Destination account is malformed."},
		{"container source is internal", `{"source_account":{},"destination_account":"` + acct + `","destination_amount":"1000"}`, rpcerrors.RpcINTERNAL, "internal", "Internal error."},
		{"container destination is internal", `{"source_account":"` + acct + `","destination_account":[],"destination_amount":"1000"}`, rpcerrors.RpcINTERNAL, "internal", "Internal error."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := ParseAndCreateSession(json.RawMessage(test.params), nil)
			if rpcErr == nil {
				t.Fatal("expected error")
			}
			if rpcErr.Code != test.code || rpcErr.ErrorString != test.token || rpcErr.Message != test.message {
				t.Fatalf("error = %d %q %q, want %d %q %q", rpcErr.Code, rpcErr.ErrorString, rpcErr.Message, test.code, test.token, test.message)
			}
		})
	}
}

func TestParseAndCreateSession_RequiredFieldNamesAreCaseSensitive(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, test := range []struct {
		name   string
		params string
		code   int
		token  string
	}{
		{name: "source account", params: `{"Source_Account":"` + acct + `","destination_account":"` + acct + `","destination_amount":"1000"}`, code: rpcerrors.RpcSRC_ACT_MISSING, token: "srcActMissing"},
		{name: "destination account", params: `{"source_account":"` + acct + `","Destination_Account":"` + acct + `","destination_amount":"1000"}`, code: rpcerrors.RpcDST_ACT_MISSING, token: "dstActMissing"},
		{name: "destination amount", params: `{"source_account":"` + acct + `","destination_account":"` + acct + `","Destination_Amount":"1000"}`, code: rpcerrors.RpcDST_AMT_MISSING, token: "dstAmtMissing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := ParseAndCreateSession(json.RawMessage(test.params), nil)
			if rpcErr == nil || rpcErr.Code != test.code || rpcErr.ErrorString != test.token {
				t.Fatalf("error = %#v, want %d/%s", rpcErr, test.code, test.token)
			}
		})
	}
}

func TestParseAndCreateSession_IgnoresMisCasedDuplicateRequiredKey(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	params := json.RawMessage(`{"source_account":"` + acct + `","Source_Account":"bad","destination_account":"` + acct + `","destination_amount":"1000"}`)
	session, rpcErr := ParseAndCreateSession(params, nil)
	if rpcErr != nil || session == nil {
		t.Fatalf("error = %v, session = %v, want exact source_account value to win", rpcErr, session)
	}
}

func TestParseAndCreateSession_TruncatesAccountAtEmbeddedNUL(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	params := json.RawMessage(`{"source_account":"` + acct + `\u0000suffix","destination_account":"` + acct + `","destination_amount":"1000"}`)
	session, rpcErr := ParseAndCreateSession(params, nil)
	if rpcErr != nil || session == nil {
		t.Fatalf("error = %v, session = %v, want account prefix to decode", rpcErr, session)
	}
	if session.srcAccountStr != acct {
		t.Fatalf("source account = %q, want %q", session.srcAccountStr, acct)
	}
}

func TestParseAndCreateSession_HexZeroSourceCurrencyIsXRP(t *testing.T) {
	const acct = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const zero = "0000000000000000000000000000000000000000"
	base := `{"source_account":"` + acct + `","destination_account":"` + acct + `","destination_amount":"1000",`

	session, rpcErr := ParseAndCreateSession(json.RawMessage(base+`"source_currencies":[{"currency":"`+zero+`"}]}`), nil)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
	if len(session.srcCurrencies) != 1 || session.srcCurrencies[0].Currency != "XRP" || session.srcCurrencies[0].Issuer != ([20]byte{}) {
		t.Fatalf("source currencies = %+v, want XRP", session.srcCurrencies)
	}

	_, rpcErr = ParseAndCreateSession(json.RawMessage(base+`"source_currencies":[{"currency":"`+zero+`","issuer":"`+acct+`"}]}`), nil)
	if rpcErr == nil || rpcErr.ErrorString != "srcCurMalformed" || rpcErr.Code != rpcerrors.RpcSRC_CUR_MALFORMED {
		t.Fatalf("error = %v, want srcCurMalformed", rpcErr)
	}
}

func TestParseAndCreateSession_MPTIssuerErrors(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const mptID = "000000010000000000000000000000000000000000000000"
	base := `{"source_account":"` + account + `","destination_account":"` + account + `","destination_amount":"1000","source_currencies":[`
	for _, test := range []struct {
		name    string
		asset   string
		code    int
		token   string
		message string
	}{
		{"valid issuer", `{"mpt_issuance_id":"` + mptID + `","issuer":"` + account + `"}`, rpcerrors.RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed."},
		{"null issuer", `{"mpt_issuance_id":"` + mptID + `","issuer":null}`, rpcerrors.RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed."},
		{"malformed issuer", `{"mpt_issuance_id":"0","issuer":5}`, rpcerrors.RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed."},
		{"invalid ID precedes issuer", `{"mpt_issuance_id":"bad","issuer":"` + account + `"}`, rpcerrors.RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed."},
		{"nonstring ID precedes issuer", `{"mpt_issuance_id":5,"issuer":null}`, rpcerrors.RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed."},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, rpcErr := ParseAndCreateSession(json.RawMessage(base+test.asset+`]}`), nil)
			if session != nil || rpcErr == nil {
				t.Fatalf("session = %v, error = %v, want validation error", session, rpcErr)
			}
			if rpcErr.Code != test.code || rpcErr.ErrorString != test.token || rpcErr.Message != test.message {
				t.Fatalf("error = %#v, want %d %q %q", rpcErr, test.code, test.token, test.message)
			}
		})
	}
}

func TestParseAndCreateSession_SourceCurrenciesMatchRippledAssetRules(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	base := `{"source_account":"` + account + `","destination_account":"` + account + `","destination_amount":"1000","source_currencies":[`

	tests := []struct {
		name      string
		asset     string
		wantError string
		wantMPT   bool
	}{
		{name: "null currency", asset: `{"currency":null}`, wantError: "srcCurMalformed"},
		{name: "null issuer", asset: `{"currency":"USD","issuer":null}`, wantError: "srcIsrMalformed"},
		{name: "empty currency is XRP", asset: `{"currency":""}`},
		{name: "currency NUL is truncated", asset: `{"currency":"USD\u0000suffix"}`},
		{name: "MPT NUL is truncated", asset: `{"mpt_issuance_id":"0\u0000suffix"}`, wantMPT: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, rpcErr := ParseAndCreateSession(json.RawMessage(base+test.asset+`]}`), nil)
			if test.wantError != "" {
				if session != nil || rpcErr == nil || rpcErr.ErrorString != test.wantError {
					t.Fatalf("session = %v, error = %v, want %s", session, rpcErr, test.wantError)
				}
				return
			}
			if rpcErr != nil || session == nil {
				t.Fatalf("session = %v, error = %v, want success", session, rpcErr)
			}
			if len(session.srcCurrencies) != 1 {
				t.Fatalf("source currencies = %+v, want one issue", session.srcCurrencies)
			}
			if test.wantMPT {
				if !session.srcCurrencies[0].IsMPT {
					t.Fatalf("source currency = %+v, want MPT", session.srcCurrencies[0])
				}
				return
			}
			if test.name == "empty currency is XRP" || test.name == "currency NUL is truncated" {
				if session.srcCurrencies[0].Currency != "XRP" && test.name == "empty currency is XRP" {
					t.Fatalf("source currency = %q, want XRP", session.srcCurrencies[0].Currency)
				}
			}
		})
	}
}

func TestParseAndCreateSessionSendMaxReconciliation(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const mptID = "000000010000000000000000000000000000000000000001"
	const issuerHex = "0000000000000000000000000000000000000001"
	const sourceCurrencyHex = "0000000000000000000000005553440000000000"

	t.Run("matching MPT ignores issuance issuer mismatch", func(t *testing.T) {
		params := `{"source_account":"` + account + `","destination_account":"` + account + `","destination_amount":"-1","send_max":{"mpt_issuance_id":"` + mptID + `","value":"10"},"source_currencies":[{"mpt_issuance_id":"` + mptID + `"}]}`
		session, rpcErr := ParseAndCreateSession(json.RawMessage(params), nil)
		if rpcErr != nil || session == nil || len(session.srcCurrencies) != 1 || !session.srcCurrencies[0].IsMPT {
			t.Fatalf("session = %v, error = %v, source currencies = %v, want one matching MPT", session, rpcErr, sessionSourceCurrencies(session))
		}
	})

	t.Run("matching IOU compares currency bytes and emits one resolved issue", func(t *testing.T) {
		params := `{"source_account":"` + account + `","destination_account":"` + account + `","destination_amount":"-1","send_max":{"currency":"USD","issuer":"` + issuerHex + `","value":"10"},"source_currencies":[{"currency":"` + sourceCurrencyHex + `"}]}`
		session, rpcErr := ParseAndCreateSession(json.RawMessage(params), nil)
		if rpcErr != nil || session == nil || len(session.srcCurrencies) != 1 {
			t.Fatalf("session = %v, error = %v, source currencies = %v, want one resolved IOU", session, rpcErr, sessionSourceCurrencies(session))
		}
		wantIssuer := [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
		if session.srcCurrencies[0].Issuer != wantIssuer {
			t.Fatalf("source issuer = %x, want %x", session.srcCurrencies[0].Issuer, wantIssuer)
		}
		if session.srcCurrencies[0].Currency != "USD" {
			t.Fatalf("source currency = %q, want USD", session.srcCurrencies[0].Currency)
		}
	})
}

func sessionSourceCurrencies(session *PathFindSession) []payment.Issue {
	if session == nil {
		return nil
	}
	return session.srcCurrencies
}

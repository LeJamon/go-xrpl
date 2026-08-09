package types

import (
	"encoding/json"
	"testing"
)

func TestConditionNumericContract(t *testing.T) {
	tests := []struct {
		name string
		got  Condition
		want Condition
	}{
		{name: "none", got: NoCondition, want: 0},
		{name: "network", got: NeedsNetworkConnection, want: 1},
		{name: "current ledger", got: NeedsCurrentLedger, want: 2},
		{name: "closed ledger", got: NeedsClosedLedger, want: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("%s = %d, want %d", test.name, test.got, test.want)
			}
		})
	}
}

func TestWarningCodeContract(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{name: "unsupported amendments majority", got: WarningUnsupportedAmendmentsMajority, want: 1001},
		{name: "amendment blocked", got: WarningAmendmentBlocked, want: 1002},
		{name: "expired validator list", got: WarningExpiredValidatorList, want: 1003},
		{name: "fields deprecated", got: WarningFieldsDeprecated, want: 2004},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("%s = %d, want %d", test.name, test.got, test.want)
			}
		})
	}
}

func TestSubscriptionRequestUnmarshalResetsReusedValue(t *testing.T) {
	var request SubscriptionRequest
	request.ApiVersion = 2
	first := []byte(`{
		"streams":["ledger"],
		"accounts":["rAccount"],
		"accounts_proposed":["rProposed"],
		"rt_accounts":["rRealtime"],
		"account_history_tx_stream":{"account":"rHistory"},
		"books":[{"taker_pays":{"currency":"USD"},"taker_gets":{"currency":"XRP"}}],
		"url":"https://example.test/stream",
		"url_username":"url-user",
		"url_password":"url-password",
		"username":"legacy-user",
		"password":"legacy-password"
	}`)
	if err := json.Unmarshal(first, &request); err != nil {
		t.Fatalf("first decode: %v", err)
	}

	if err := json.Unmarshal([]byte(`{"streams":[]}`), &request); err != nil {
		t.Fatalf("second decode: %v", err)
	}
	if request.ApiVersion != 2 {
		t.Fatalf("ApiVersion = %d, want transport-owned value 2", request.ApiVersion)
	}
	if len(request.Streams) != 0 || request.Streams == nil {
		t.Fatalf("Streams = %#v, want an empty decoded array", request.Streams)
	}
	if request.Accounts != nil || request.AccountsProposed != nil || request.RTAccounts != nil {
		t.Fatalf("omitted account arrays retained: accounts=%#v proposed=%#v realtime=%#v", request.Accounts, request.AccountsProposed, request.RTAccounts)
	}
	if request.AccountHistory != nil || request.Books != nil {
		t.Fatalf("omitted nested fields retained: history=%#v books=%#v", request.AccountHistory, request.Books)
	}
	if request.URL != "" || request.URLUsername != "" || request.URLPassword != "" || request.Username != "" || request.Password != "" {
		t.Fatalf("omitted URL fields retained: url=%q urlUsername=%q urlPassword=%q username=%q password=%q", request.URL, request.URLUsername, request.URLPassword, request.Username, request.Password)
	}
	if username, password, usernameSet, passwordSet := request.URLCredentials(); username != "" || password != "" || usernameSet || passwordSet {
		t.Fatalf("URLCredentials = (%q, %q, %t, %t), want zero credentials and presence", username, password, usernameSet, passwordSet)
	}
	wire := request.WireArrays()
	if !wire.Present || string(wire.Streams) != "[]" || wire.Accounts != nil || wire.Books != nil {
		t.Fatalf("WireArrays after reuse = %#v, want only streams=[] present", wire)
	}
}

func TestLedgerIndexUnmarshalReusesValue(t *testing.T) {
	var index LedgerIndex
	for _, test := range []struct {
		name string
		data string
		want LedgerIndex
	}{
		{name: "named", data: `"validated"`, want: "validated"},
		{name: "number", data: `12345`, want: "12345"},
		{name: "null", data: `null`, want: ""},
	} {
		if err := json.Unmarshal([]byte(test.data), &index); err != nil {
			t.Fatalf("%s decode: %v", test.name, err)
		}
		if index != test.want {
			t.Fatalf("%s decode = %q, want %q", test.name, index, test.want)
		}
	}
}

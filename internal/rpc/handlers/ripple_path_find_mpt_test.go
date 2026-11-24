package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestRipplePathFindRejectsMPTWithZeroIssuer(t *testing.T) {
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
			_, rpcErr := (&ripplePathFindMethod{}).Handle(&types.RpcContext{
				Services: types.NewTestServiceGraph(&types.ServiceContainer{
					Ledger:       &pathFindTestLedger{info: freshPathFindInfo(), view: &pathFindTestView{}},
					Capabilities: types.RPCCapabilities{PathSearchMax: 3},
				}),
			}, test.params)
			require.NotNil(t, rpcErr)
			require.Equal(t, test.token, rpcErr.ErrorString)
		})
	}
}

func TestParseSourceCurrenciesMPTSendMaxMatchesIssuance(t *testing.T) {
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	idString := mptutil.EncodeID(id)
	sendMax := state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuer), idString)
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"mpt_issuance_id":"` + idString + `"}]`),
	}

	issues, rpcErr := parseSourceCurrencies(probe, holder, &sendMax)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 1)
	require.Equal(t, id, issues[0].MPTID)
}

func TestParseSourceCurrenciesIOUSendMaxResolvesOneIssue(t *testing.T) {
	var source, issuer [20]byte
	source[19] = 2
	issuer[19] = 3
	const sourceCurrencyHex = "0000000000000000000000005553440000000000"
	sendMax := state.NewIssuedAmountFromFloat64(
		10, "USD", state.EncodeAccountIDSafe(issuer),
	)
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"currency":"` + sourceCurrencyHex + `"}]`),
	}

	issues, rpcErr := parseSourceCurrencies(probe, source, &sendMax)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 1)
	require.Equal(t, "USD", issues[0].Currency)
	require.Equal(t, issuer, issues[0].Issuer)
}

func TestParseSourceCurrenciesAcceptsZeroMPTLiteral(t *testing.T) {
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"mpt_issuance_id":"0"}]`),
	}

	issues, rpcErr := parseSourceCurrencies(probe, [20]byte{1}, nil)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 1)
	require.True(t, issues[0].IsMPT)
	require.Equal(t, [24]byte{}, issues[0].MPTID)
}

func TestParseSourceCurrenciesEmptyStringIsXRP(t *testing.T) {
	issues, rpcErr := parseSourceCurrencies(map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"currency":""}]`),
	}, [20]byte{1}, nil)
	require.Nil(t, rpcErr)
	require.Equal(t, []payment.Issue{{Currency: "XRP"}}, issues)
}

func TestParseSourceCurrenciesMPTIssuerIsCurrencyMalformed(t *testing.T) {
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"mpt_issuance_id":"0","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}]`),
	}
	_, rpcErr := parseSourceCurrencies(probe, [20]byte{1}, nil)
	require.NotNil(t, rpcErr)
	require.Equal(t, "srcCurMalformed", rpcErr.ErrorString)
}

func TestParseSourceCurrenciesOrdersMixedAssetsLikeRippled(t *testing.T) {
	var mptIssuer [20]byte
	mptIssuer[19] = 1
	mptID := mptutil.EncodeID(keylet.MakeMPTID(1, mptIssuer))
	issues, rpcErr := parseSourceCurrencies(map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"currency":"0000000000000000000000005553440000000000"},{"mpt_issuance_id":"` + mptID + `"},{"currency":"USD"}]`),
	}, [20]byte{2}, nil)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 2)
	require.True(t, issues[0].IsMPT)
	require.Equal(t, "USD", issues[1].Currency)
}

func TestParseSourceCurrenciesStringGuards(t *testing.T) {
	source := [20]byte{2}
	issuerHex := "0000000000000000000000000000000000000001"
	tests := []struct {
		name    string
		asset   string
		errCode int
		issuer  [20]byte
		mpt     bool
	}{
		{name: "null currency", asset: `{"currency":null}`, errCode: rpcerrors.RpcSRC_CUR_MALFORMED},
		{name: "null issuer", asset: `{"currency":"USD","issuer":null}`, errCode: rpcerrors.RpcSRC_ISR_MALFORMED},
		{name: "mpt null", asset: `{"mpt_issuance_id":null}`, errCode: rpcerrors.RpcSRC_CUR_MALFORMED},
		{name: "mpt issuer", asset: `{"mpt_issuance_id":"0","issuer":5}`, errCode: rpcerrors.RpcSRC_CUR_MALFORMED},
		{name: "zero issuer", asset: `{"currency":"USD","issuer":"0"}`, issuer: source},
		{name: "hex issuer", asset: `{"currency":"USD","issuer":"` + issuerHex + `"}`, issuer: [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
		{name: "currency NUL", asset: `{"currency":"USD\u0000suffix"}`, issuer: source},
		{name: "MPT NUL", asset: `{"mpt_issuance_id":"0\u0000suffix"}`, mpt: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues, rpcErr := parseSourceCurrencies(map[string]json.RawMessage{
				"source_currencies": json.RawMessage("[" + test.asset + "]"),
			}, source, nil)
			if test.errCode != 0 {
				require.NotNil(t, rpcErr)
				require.Equal(t, test.errCode, rpcErr.Code)
				return
			}
			require.Nil(t, rpcErr)
			require.Len(t, issues, 1)
			if test.mpt {
				require.True(t, issues[0].IsMPT)
				return
			}
			require.Equal(t, test.issuer, issues[0].Issuer)
		})
	}
}

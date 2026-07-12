package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
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
			_, rpcErr := (&RipplePathFindMethod{}).Handle(&types.RPCContext{}, test.params)
			require.NotNil(t, rpcErr)
			require.Equal(t, test.token, rpcErr.ErrorString)
		})
	}
}

func TestParseSourceCurrenciesMPTSendMaxIssuerReconciliation(t *testing.T) {
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	idString := mptutil.EncodeID(id)
	sendMax := state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuer), idString)
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"mpt_issuance_id":"` + idString + `"}]`),
	}

	_, rpcErr := parseSourceCurrencies(probe, holder, &sendMax)
	require.NotNil(t, rpcErr)
	require.Equal(t, "srcIsrMalformed", rpcErr.ErrorString)

	issues, rpcErr := parseSourceCurrencies(probe, issuer, &sendMax)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 1)
	require.Equal(t, id, issues[0].MPTID)
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

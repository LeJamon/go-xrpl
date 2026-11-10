package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// rippled PathRequest::parseJson reads source currencies with toCurrency,
// which parses the 40-hex all-zero code as XRP: it may not carry an issuer
// and needs no default issuer.
func TestRipplePathFindHexZeroSourceCurrencyIsXRP(t *testing.T) {
	const zero = "0000000000000000000000000000000000000000"
	src := [20]byte{1}

	issues, rpcErr := parseSourceCurrencies(map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"currency":"` + zero + `"}]`),
	}, src, nil)
	require.Nil(t, rpcErr)
	require.Equal(t, []payment.Issue{{Currency: "XRP"}}, issues)

	_, rpcErr = parseSourceCurrencies(map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"currency":"` + zero + `","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}]`),
	}, src, nil)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpcerrors.RpcSRC_CUR_MALFORMED, rpcErr.Code)
	require.Equal(t, "srcCurMalformed", rpcErr.ErrorString)
}

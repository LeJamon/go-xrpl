package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
)

// sentinelAddr returns the classic address of a reserved AccountID sentinel.
func sentinelAddr(t *testing.T, id [20]byte) string {
	t.Helper()
	addr, err := addresscodec.EncodeAccountIDToClassicAddress(id[:])
	require.NoError(t, err)
	return addr
}

// TestIssueFromJSON_RejectsSentinelIssuers verifies that issue parsing rejects
// xrpAccount() (ACCOUNT_ZERO) and noAccount() (ACCOUNT_ONE) as an IOU issuer,
// matching rippled issueFromJson. Reference: rippled commit af7e5ef995.
func TestIssueFromJSON_RejectsSentinelIssuers(t *testing.T) {
	xrpAddr := sentinelAddr(t, xrpAccountID)
	noAddr := sentinelAddr(t, noAccountID)
	validIssuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("amm_info parseIssue rejects xrpAccount", func(t *testing.T) {
		_, _, err := parseIssue(json.RawMessage(`{"currency":"USD","issuer":"` + xrpAddr + `"}`))
		require.Error(t, err)
	})
	t.Run("amm_info parseIssue rejects noAccount", func(t *testing.T) {
		_, _, err := parseIssue(json.RawMessage(`{"currency":"USD","issuer":"` + noAddr + `"}`))
		require.Error(t, err)
	})
	t.Run("amm_info parseIssue accepts a valid issuer", func(t *testing.T) {
		_, _, err := parseIssue(json.RawMessage(`{"currency":"USD","issuer":"` + validIssuer + `"}`))
		require.NoError(t, err)
	})

	t.Run("ledger_entry parseCurrencyIssuer rejects xrpAccount", func(t *testing.T) {
		_, _, err := parseCurrencyIssuer(json.RawMessage(`{"currency":"USD","issuer":"` + xrpAddr + `"}`))
		require.Error(t, err)
	})
	t.Run("ledger_entry parseCurrencyIssuer rejects noAccount", func(t *testing.T) {
		_, _, err := parseCurrencyIssuer(json.RawMessage(`{"currency":"USD","issuer":"` + noAddr + `"}`))
		require.Error(t, err)
	})
	t.Run("ledger_entry parseCurrencyIssuer accepts a valid issuer", func(t *testing.T) {
		_, _, err := parseCurrencyIssuer(json.RawMessage(`{"currency":"USD","issuer":"` + validIssuer + `"}`))
		require.NoError(t, err)
	})
	t.Run("ledger_entry parseCurrencyIssuer accepts XRP without issuer", func(t *testing.T) {
		_, _, err := parseCurrencyIssuer(json.RawMessage(`{"currency":"XRP"}`))
		require.NoError(t, err)
	})
}

// TestArraySizeRpcError verifies the JSON-array-size overflow from the codec is
// mapped to invalidParams (item 2, rippled STParsedJSON cap), and other encode
// errors fall through (nil). Reference: rippled commit 377b155ddc.
func TestArraySizeRpcError(t *testing.T) {
	hashes := make([]any, 513)
	for i := range hashes {
		hashes[i] = strings.Repeat("A", 64)
	}
	_, encErr := binarycodec.Encode(map[string]any{"Amendments": hashes})
	require.Error(t, encErr)

	rpcErr := arraySizeRpcError(encErr)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	assert.Contains(t, rpcErr.Message, "exceeds allowed JSON array size of 512 elements per field.")

	assert.Nil(t, arraySizeRpcError(errors.New("some other encode error")))
	assert.Nil(t, arraySizeRpcError(nil))
}

// TestReadLimitField exercises the readLimitField port directly: absent/null ->
// default, explicit 0 -> invalidParams for every role, malformed ->
// expected_field, and non-admin clamping. Reference: rippled RPCHelpers.cpp.
func TestReadLimitField(t *testing.T) {
	r := limitRange{Min: 10, Default: 200, Max: 400}

	check := func(t *testing.T, params string, unlimited bool) (uint32, *rpcerrors.RpcError) {
		t.Helper()
		return readLimitField(json.RawMessage(params), r, unlimited)
	}

	t.Run("absent -> default", func(t *testing.T) {
		v, err := check(t, `{}`, false)
		require.Nil(t, err)
		assert.Equal(t, uint32(200), v)
	})
	t.Run("null -> default", func(t *testing.T) {
		v, err := check(t, `{"limit":null}`, false)
		require.Nil(t, err)
		assert.Equal(t, uint32(200), v)
	})
	t.Run("explicit 0 -> invalidParams (guest)", func(t *testing.T) {
		_, err := check(t, `{"limit":0}`, false)
		require.NotNil(t, err)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, err.Code)
		assert.Equal(t, "Invalid field 'limit'.", err.Message)
	})
	t.Run("explicit 0 -> invalidParams (admin/unlimited)", func(t *testing.T) {
		_, err := check(t, `{"limit":0}`, true)
		require.NotNil(t, err)
		assert.Equal(t, "Invalid field 'limit'.", err.Message)
	})
	t.Run("string -> expected_field", func(t *testing.T) {
		_, err := check(t, `{"limit":"abc"}`, false)
		require.NotNil(t, err)
		assert.Equal(t, "Invalid field 'limit', not unsigned integer.", err.Message)
	})
	t.Run("negative -> expected_field", func(t *testing.T) {
		_, err := check(t, `{"limit":-5}`, false)
		require.NotNil(t, err)
		assert.Equal(t, "Invalid field 'limit', not unsigned integer.", err.Message)
	})
	t.Run("below min clamped (guest)", func(t *testing.T) {
		v, err := check(t, `{"limit":5}`, false)
		require.Nil(t, err)
		assert.Equal(t, uint32(10), v)
	})
	t.Run("above max clamped (guest)", func(t *testing.T) {
		v, err := check(t, `{"limit":9999}`, false)
		require.Nil(t, err)
		assert.Equal(t, uint32(400), v)
	})
	t.Run("above max not clamped for unlimited", func(t *testing.T) {
		v, err := check(t, `{"limit":9999}`, true)
		require.Nil(t, err)
		assert.Equal(t, uint32(9999), v)
	})
}

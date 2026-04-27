package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeValidationArchive is a deterministic in-memory stub for
// types.ValidationArchiveLookup. We deliberately do NOT use a real
// SQLite/postgres backend here — the handler under test should not
// care which backend produced the rows, only that the projection
// shape is preserved. The fake also lets us assert exactly what
// (nodeKey, limit) the handler called us with.
type fakeValidationArchive struct {
	mu              sync.Mutex
	byValidator     []types.ArchivedValidation
	byValidatorErr  error
	count           int64
	lastNodeKeyHex  string
	lastLimit       int
}

func (f *fakeValidationArchive) GetValidationsForLedger(ledgerSeq uint32) ([]types.ArchivedValidation, error) {
	return nil, nil
}

func (f *fakeValidationArchive) GetValidationsByValidator(nodeKey []byte, limit int) ([]types.ArchivedValidation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastNodeKeyHex = bytesHex(nodeKey)
	f.lastLimit = limit
	if f.byValidatorErr != nil {
		return nil, f.byValidatorErr
	}
	// Mirror the storage contract: limit > 0 truncates the result;
	// limit <= 0 means "no bound". The SQL backends do this with
	// LIMIT $N — the handler trusts them, so the fake must too.
	rows := f.byValidator
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]types.ArchivedValidation, len(rows))
	copy(out, rows)
	return out, nil
}

func (f *fakeValidationArchive) GetValidationCount() (int64, error) {
	return f.count, nil
}

func bytesHex(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexchars[c>>4]
		out[i*2+1] = hexchars[c&0x0f]
	}
	return string(out)
}

// makeValidatorPubKey returns a deterministic 33-byte "validator key".
// EncodeNodePublicKey treats it as opaque bytes — it base58-encodes
// with the NodePublic type byte prefix without validating it as a
// curve point — so any stable 33-byte pattern works for handler tests
// that don't exercise crypto.
func makeValidatorPubKey() []byte {
	pk := make([]byte, 33)
	pk[0] = 0x02 // compressed-secp256k1 prefix, just for realism
	for i := 1; i < 33; i++ {
		pk[i] = byte(i)
	}
	return pk
}

func mkLedgerHash(seed byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = seed ^ byte(i)
	}
	return h
}

// installValidatorServices swaps Services for a test fixture. The
// returned cleanup restores the previous container so parallel-style
// tests don't bleed state.
func installValidatorServices(validatorPK []byte, archive types.ValidationArchiveLookup) func() {
	old := types.Services
	types.Services = &types.ServiceContainer{
		ValidatorPublicKey: validatorPK,
		ValidationArchive:  archive,
	}
	return func() { types.Services = old }
}

// adminCtx is a small ergonomic helper — every test uses the same
// admin context with a background context.Context.
func adminCtx() *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}
}

// TestValidatorInfo_RecentValidations_ArchiveBacked is acceptance
// test #1 from issue #285: a wired archive returning N rows for the
// local validator key surfaces them in DESC order with the requested
// limit honoured.
func TestValidatorInfo_RecentValidations_ArchiveBacked(t *testing.T) {
	pk := makeValidatorPubKey()
	expectedMaster, err := addresscodec.EncodeNodePublicKey(pk)
	require.NoError(t, err)

	// Seed the fake with three rows in DESC order of ledger_seq —
	// this mirrors what the SQL layer guarantees so the handler does
	// not need to re-sort.
	archive := &fakeValidationArchive{
		byValidator: []types.ArchivedValidation{
			{LedgerSeq: 100, LedgerHash: mkLedgerHash(0xAA), SignTimeS: 1700000300, SeenTimeS: 1700000301, Flags: 1},
			{LedgerSeq: 99, LedgerHash: mkLedgerHash(0xBB), SignTimeS: 1700000200, SeenTimeS: 1700000201, Flags: 0},
			{LedgerSeq: 98, LedgerHash: mkLedgerHash(0xCC), SignTimeS: 1700000100, SeenTimeS: 1700000101, Flags: 2},
		},
	}
	cleanup := installValidatorServices(pk, archive)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}

	params, err := json.Marshal(map[string]interface{}{"limit": 2})
	require.NoError(t, err)

	result, rpcErr := method.Handle(adminCtx(), params)
	require.Nil(t, rpcErr, "handler returned error: %#v", rpcErr)
	require.NotNil(t, result)

	// Marshal back through JSON so the test exercises the same wire
	// shape the RPC framework will emit.
	resp := decodeResponse(t, result)

	assert.Equal(t, expectedMaster, resp["master_key"], "master_key should be the validator key when no manifest is wired")
	assert.NotContains(t, resp, "ephemeral_key", "no manifest cache → no ephemeral_key")
	assert.NotContains(t, resp, "manifest")
	assert.NotContains(t, resp, "seq")
	assert.NotContains(t, resp, "domain")

	rv, ok := resp["recent_validations"].([]interface{})
	require.True(t, ok, "recent_validations missing or wrong type: %T", resp["recent_validations"])
	require.Len(t, rv, 2, "limit=2 should be honoured by the handler")

	first := rv[0].(map[string]interface{})
	second := rv[1].(map[string]interface{})

	// The fake returns in DESC order; the handler must preserve it.
	assert.EqualValues(t, 100, first["ledger_seq"], "first entry should be highest ledger")
	assert.EqualValues(t, 99, second["ledger_seq"])

	// Pre-merge note in the issue: ledger hashes are hex, uppercase
	// — match rippled's text representation so external tooling
	// doesn't have to renormalise.
	hash, _ := first["ledger_hash"].(string)
	assert.Len(t, hash, 64, "ledger_hash should be 64 hex chars")
	assert.Equal(t, strings.ToUpper(hash), hash, "ledger_hash must be uppercase hex")

	// Verify the fake was called with the validator key bytes and
	// the limit we asked for — guards against the handler silently
	// passing the wrong identity downstream.
	assert.Equal(t, bytesHex(pk), archive.lastNodeKeyHex)
	assert.Equal(t, 2, archive.lastLimit)
}

// TestValidatorInfo_NotConfigured_StillReturnsNotValidator is
// acceptance test #2: when no validator key is wired the handler
// short-circuits to notValidator regardless of whether an archive is
// available. This is the exact rippled gate (validationPK is empty),
// not a goXRPL-flavoured "no archive" gate.
func TestValidatorInfo_NotConfigured_StillReturnsNotValidator(t *testing.T) {
	// Provide an archive but NO validator key — the handler must
	// still return notValidator. If the gate moved from validator
	// presence to archive presence by accident, this would start
	// returning {recent_validations: []} silently.
	archive := &fakeValidationArchive{}
	cleanup := installValidatorServices(nil, archive)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_VALIDATOR, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "validator")
	assert.Empty(t, archive.lastNodeKeyHex, "archive must NOT be queried when notValidator")
}

// TestServices_ValidationArchive_Nil_HandlerStillWorks is acceptance
// test #3: archive disabled, server is a validator → response has
// the rippled core fields and silently omits recent_validations
// without panicking.
func TestServices_ValidationArchive_Nil_HandlerStillWorks(t *testing.T) {
	pk := makeValidatorPubKey()
	cleanup := installValidatorServices(pk, nil)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)
	require.Nil(t, rpcErr, "handler must not error when archive is nil: %#v", rpcErr)
	require.NotNil(t, result)

	resp := decodeResponse(t, result)
	assert.NotEmpty(t, resp["master_key"], "core rippled field must still be returned")
	assert.NotContains(t, resp, "recent_validations", "no archive → field must be absent, not empty array")
}

// TestValidatorInfo_ArchiveError_Surfaces ensures the handler does
// not swallow archive errors — if the SQL layer returns a transient
// failure we want the operator to see it via an RPC internal error,
// not a silently-empty response.
func TestValidatorInfo_ArchiveError_Surfaces(t *testing.T) {
	pk := makeValidatorPubKey()
	archive := &fakeValidationArchive{byValidatorErr: errors.New("db is down")}
	cleanup := installValidatorServices(pk, archive)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	_, rpcErr := method.Handle(adminCtx(), nil)
	require.NotNil(t, rpcErr)
	assert.Contains(t, rpcErr.Message, "db is down")
}

// TestValidatorInfo_LimitClamping covers the negative-rejection and
// max-cap branches of parseRecentValidationsLimit so a misuse of the
// param doesn't leak into the SQL backend.
func TestValidatorInfo_LimitClamping(t *testing.T) {
	pk := makeValidatorPubKey()
	archive := &fakeValidationArchive{}
	cleanup := installValidatorServices(pk, archive)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}

	t.Run("negative limit rejected", func(t *testing.T) {
		params := json.RawMessage(`{"limit": -1}`)
		_, rpcErr := method.Handle(adminCtx(), params)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("limit above cap is clamped", func(t *testing.T) {
		params := json.RawMessage(`{"limit": 5000}`)
		_, rpcErr := method.Handle(adminCtx(), params)
		require.Nil(t, rpcErr)
		assert.Equal(t, 1000, archive.lastLimit, "should clamp to recentValidationsMaxLimit")
	})

	t.Run("missing limit uses default", func(t *testing.T) {
		_, rpcErr := method.Handle(adminCtx(), nil)
		require.Nil(t, rpcErr)
		assert.Equal(t, 256, archive.lastLimit, "should use recentValidationsDefaultLimit")
	})
}

// decodeResponse round-trips the handler's `interface{}` result
// through JSON so test assertions check the on-the-wire shape rather
// than internal field names.
func decodeResponse(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

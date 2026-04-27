package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeManifestLookup is a minimal ManifestLookup. We don't reach for
// the real manifest.Cache here because that requires a fully-signed
// ed25519 manifest blob; the handler under test only consumes the
// lookup interface, so a stub is enough.
type fakeManifestLookup struct {
	masterFor   map[[33]byte][33]byte
	manifestFor map[[33]byte][]byte
	seqFor      map[[33]byte]uint32
	domainFor   map[[33]byte]string
}

func (f *fakeManifestLookup) GetMasterKey(signing [33]byte) [33]byte {
	if v, ok := f.masterFor[signing]; ok {
		return v
	}
	return signing
}
func (f *fakeManifestLookup) GetSigningKey(master [33]byte) ([33]byte, bool) {
	for s, m := range f.masterFor {
		if m == master {
			return s, true
		}
	}
	return [33]byte{}, false
}
func (f *fakeManifestLookup) GetManifest(master [33]byte) ([]byte, bool) {
	v, ok := f.manifestFor[master]
	return v, ok
}
func (f *fakeManifestLookup) GetSequence(master [33]byte) (uint32, bool) {
	v, ok := f.seqFor[master]
	return v, ok
}
func (f *fakeManifestLookup) GetDomain(master [33]byte) (string, bool) {
	v, ok := f.domainFor[master]
	return v, ok
}

func makeValidatorPubKey(prefix byte) []byte {
	pk := make([]byte, 33)
	pk[0] = prefix
	for i := 1; i < 33; i++ {
		pk[i] = byte(i)
	}
	return pk
}

func installServices(pk []byte, manifests types.ManifestLookup) func() {
	prev := types.Services
	types.Services = &types.ServiceContainer{
		ValidatorPublicKey: pk,
		Manifests:          manifests,
	}
	return func() { types.Services = prev }
}

func adminCtx() *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
	}
}

func decodeResponse(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

// TestValidatorInfo_NotConfigured pins rippled's testErrors:
// `error_message == "not a validator"` with rpcINVALID_PARAMS code.
func TestValidatorInfo_NotConfigured(t *testing.T) {
	cleanup := installServices(nil, nil)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	assert.Equal(t, "not a validator", rpcErr.Message)
}

// TestValidatorInfo_MasterOnly covers rippled's `if (mk == validationPK)
// return ret;` early-return — when no manifest cache resolves the key,
// only master_key is emitted and the ephemeral/manifest/seq/domain
// block stays absent.
func TestValidatorInfo_MasterOnly(t *testing.T) {
	pk := makeValidatorPubKey(0x02)
	expectedMaster, err := addresscodec.EncodeNodePublicKey(pk)
	require.NoError(t, err)

	cleanup := installServices(pk, nil)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := decodeResponse(t, result)
	assert.Equal(t, expectedMaster, resp["master_key"])
	assert.NotContains(t, resp, "ephemeral_key")
	assert.NotContains(t, resp, "manifest")
	assert.NotContains(t, resp, "seq")
	assert.NotContains(t, resp, "domain")
}

// TestValidatorInfo_WithManifest covers the full rippled response when
// the manifest cache resolves the configured signing key to a different
// master — ephemeral_key, manifest, seq, domain are all emitted.
func TestValidatorInfo_WithManifest(t *testing.T) {
	signingKey := makeValidatorPubKey(0x02)
	var signingArr [33]byte
	copy(signingArr[:], signingKey)

	var masterArr [33]byte
	masterArr[0] = 0x03
	for i := 1; i < 33; i++ {
		masterArr[i] = byte(i)
	}

	manifestBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	domain := "example.org"
	seq := uint32(7)

	manifests := &fakeManifestLookup{
		masterFor:   map[[33]byte][33]byte{signingArr: masterArr},
		manifestFor: map[[33]byte][]byte{masterArr: manifestBytes},
		seqFor:      map[[33]byte]uint32{masterArr: seq},
		domainFor:   map[[33]byte]string{masterArr: domain},
	}
	cleanup := installServices(signingKey, manifests)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)
	require.Nil(t, rpcErr)

	resp := decodeResponse(t, result)
	expectedMaster, _ := addresscodec.EncodeNodePublicKey(masterArr[:])
	expectedEphemeral, _ := addresscodec.EncodeNodePublicKey(signingKey)

	assert.Equal(t, expectedMaster, resp["master_key"])
	assert.Equal(t, expectedEphemeral, resp["ephemeral_key"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(manifestBytes), resp["manifest"])
	assert.EqualValues(t, seq, resp["seq"])
	assert.Equal(t, domain, resp["domain"])
}

// TestValidatorInfo_SeqZeroSerialises pins rippled parity for `seq`:
// rippled emits ret[jss::seq] = *seq even when the value is 0, so we
// use *uint32 to avoid omitempty dropping a legitimate zero-sequence
// manifest.
func TestValidatorInfo_SeqZeroSerialises(t *testing.T) {
	signingKey := makeValidatorPubKey(0x02)
	var signingArr [33]byte
	copy(signingArr[:], signingKey)

	var masterArr [33]byte
	masterArr[0] = 0x03
	for i := 1; i < 33; i++ {
		masterArr[i] = byte(i)
	}

	manifests := &fakeManifestLookup{
		masterFor: map[[33]byte][33]byte{signingArr: masterArr},
		seqFor:    map[[33]byte]uint32{masterArr: 0},
	}
	cleanup := installServices(signingKey, manifests)
	defer cleanup()

	method := &handlers.ValidatorInfoMethod{}
	result, rpcErr := method.Handle(adminCtx(), nil)
	require.Nil(t, rpcErr)

	resp := decodeResponse(t, result)
	assert.Contains(t, resp, "seq", "seq=0 must round-trip; pointer + omitempty preserves the zero value")
	assert.EqualValues(t, 0, resp["seq"])
}

// TestValidatorInfo_RequiredRoleAdmin pins rippled's testPrivileges
// expectation: the dispatcher must reject non-admin callers (returns a
// null result there). Here we only assert the handler advertises the
// admin requirement; the dispatcher-level enforcement is covered by
// admin_role_test.go.
func TestValidatorInfo_RequiredRoleAdmin(t *testing.T) {
	method := &handlers.ValidatorInfoMethod{}
	assert.Equal(t, types.RoleAdmin, method.RequiredRole())
}

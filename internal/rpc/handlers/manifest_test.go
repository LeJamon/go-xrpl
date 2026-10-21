package handlers_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func applyRPCManifest(t *testing.T, cache *manifest.Cache, sequence uint32, signingSeed byte, domain string) ([33]byte, [33]byte, []byte) {
	t.Helper()
	masterBytes, masterPrivate := deterministicEd25519KeypairRPC(0x11)
	ephemeralBytes, ephemeralPrivate := deterministicEd25519KeypairRPC(signingSeed)
	var master, ephemeral [33]byte
	copy(master[:], masterBytes)
	copy(ephemeral[:], ephemeralBytes)
	fields := map[string]any{
		"PublicKey": hex.EncodeToString(masterBytes),
		"Sequence":  sequence,
	}
	if sequence != manifest.RevokedSequence {
		fields["SigningPubKey"] = hex.EncodeToString(ephemeralBytes)
	}
	if domain != "" {
		fields["Domain"] = hex.EncodeToString([]byte(domain))
	}
	preimage := manifestPreimage(t, fields)
	if sequence != manifest.RevokedSequence {
		fields["Signature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(ephemeralPrivate), preimage))
	}
	fields["MasterSignature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(masterPrivate), preimage))
	encoded, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	serialized, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	parsed, err := manifest.Deserialize(serialized)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(parsed))
	return master, ephemeral, serialized
}

func manifestPreimage(t *testing.T, src map[string]any) []byte {
	t.Helper()
	filtered := make(map[string]any, len(src))
	for k, v := range src {
		if k == "Signature" || k == "MasterSignature" {
			continue
		}
		filtered[k] = v
	}
	encoded, err := binarycodec.Encode(filtered)
	if err != nil {
		t.Fatalf("encode preimage: %v", err)
	}
	body, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode preimage: %v", err)
	}
	prefix := protocol.HashPrefixManifest()
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out
}

func deterministicEd25519KeypairRPC(seed byte) ([]byte, []byte) {
	s := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(s)
	pub := priv.Public().(ed25519.PublicKey)
	return append([]byte{0xED}, pub...), priv
}

func servicesForCache(cache types.ManifestLookup) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{Manifests: cache})
}

func callManifestRPC(t *testing.T, services *types.ServiceGraph, publicKey string) (map[string]any, *rpcerrors.RpcError) {
	t.Helper()
	params, err := json.Marshal(map[string]string{"public_key": publicKey})
	require.NoError(t, err)
	method := &handlers.ManifestMethod{}
	result, rpcErr := method.Handle(&types.RpcContext{Services: services}, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	j, _ := json.Marshal(result)
	var out map[string]any
	if err := json.Unmarshal(j, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out, nil
}

func TestRPC_Manifest_UnknownKey_ReturnsKeyDetails(t *testing.T) {
	cache := manifest.NewCache()
	services := servicesForCache(cache)

	unknownBytes, _ := deterministicEd25519KeypairRPC(0x99)
	unknownB58, _ := addresscodec.EncodeNodePublicKey(unknownBytes)

	out, rpcErr := callManifestRPC(t, services, unknownB58)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	if out["requested"] != unknownB58 {
		t.Fatalf("requested: %v", out["requested"])
	}
	if _, ok := out["manifest"]; ok {
		t.Fatal("manifest field should be absent for unknown key")
	}
	require.Equal(t, map[string]any{
		"master_key":    unknownB58,
		"ephemeral_key": unknownB58,
	}, out["details"])
}

func TestRPCManifestInput(t *testing.T) {
	const validKey = "n9LXMXFTeVL6o9fxdFHfeVZWf6YzWCBzt7YyeK1HV7wZ4ZFRNgUV"
	tests := []struct {
		name      string
		params    string
		requested string
		code      int
		message   string
		echo      bool
	}{
		{name: "missing", params: `{}`, code: 31, message: "Missing field 'public_key'."},
		{name: "case sensitive field", params: `{"Public_Key":"n9LXMXFTeVL6o9fxdFHfeVZWf6YzWCBzt7YyeK1HV7wZ4ZFRNgUV"}`, code: 31, message: "Missing field 'public_key'."},
		{name: "nil parameters", code: 31, message: "Missing field 'public_key'."},
		{name: "null", params: `{"public_key":null}`, echo: true, code: 31, message: "Invalid parameters."},
		{name: "empty", params: `{"public_key":""}`, echo: true, code: 31, message: "Invalid parameters."},
		{name: "malformed", params: `{"public_key":"abcdef12345"}`, requested: "abcdef12345", echo: true, code: 31, message: "Invalid parameters."},
		{name: "account ID", params: `{"public_key":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`, requested: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", echo: true, code: 31, message: "Invalid parameters."},
		{name: "integer", params: `{"public_key":42}`, requested: "42", echo: true, code: 31, message: "Invalid parameters."},
		{name: "negative zero", params: `{"public_key":-0}`, requested: "0", echo: true, code: 31, message: "Invalid parameters."},
		{name: "min integer", params: `{"public_key":-2147483648}`, requested: "-2147483648", echo: true, code: 31, message: "Invalid parameters."},
		{name: "max unsigned", params: `{"public_key":4294967295}`, requested: "4294967295", echo: true, code: 31, message: "Invalid parameters."},
		{name: "real", params: `{"public_key":42.0}`, requested: "42.000000", echo: true, code: 31, message: "Invalid parameters."},
		{name: "exponent", params: `{"public_key":4.2e1}`, requested: "42.000000", echo: true, code: 31, message: "Invalid parameters."},
		{name: "negative real zero", params: `{"public_key":-0.0}`, requested: "-0.000000", echo: true, code: 31, message: "Invalid parameters."},
		{name: "true", params: `{"public_key":true}`, requested: "true", echo: true, code: 31, message: "Invalid parameters."},
		{name: "false", params: `{"public_key":false}`, requested: "false", echo: true, code: 31, message: "Invalid parameters."},
		{name: "array", params: `{"public_key":[]}`, code: 73, message: "Internal error."},
		{name: "object", params: `{"public_key":{}}`, code: 73, message: "Internal error."},
		{name: "uncached", params: `{"public_key":"` + validKey + `"}`, requested: validKey},
		{name: "embedded null", params: `{"public_key":"` + validKey + `\u0000ignored"}`, requested: validKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &types.RpcContext{LoadCost: uint32(resource.FeeReferenceRPC().Cost())}
			result, rpcErr := (&handlers.ManifestMethod{}).Handle(ctx, json.RawMessage(tt.params))
			wantCost := resource.FeeReferenceRPC().Cost()
			if tt.code == rpcerrors.RpcINTERNAL {
				wantCost = resource.FeeExceptionRPC().Cost()
			}
			require.Equal(t, uint32(wantCost), ctx.LoadCost)
			if tt.code == 0 {
				require.Nil(t, rpcErr)
				require.Equal(t, map[string]any{
					"requested": tt.requested,
					"details":   map[string]any{"master_key": tt.requested, "ephemeral_key": tt.requested},
				}, result)
				return
			}
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			require.Equal(t, tt.code, rpcErr.Code)
			require.Equal(t, tt.message, rpcErr.Message)
			requested, ok := rpcErr.ResponseFields()["requested"]
			require.Equal(t, tt.echo, ok)
			if tt.echo {
				require.Equal(t, tt.requested, requested)
			}
		})
	}
}

func TestRPCManifestKeyEncoding(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prefix  byte
		payload []byte
		valid   bool
	}{
		{"ed25519", addresscodec.NodePublicKeyPrefix, append([]byte{0xed}, make([]byte, 32)...), true},
		{"secp256k1 even", addresscodec.NodePublicKeyPrefix, append([]byte{2}, make([]byte, 32)...), true},
		{"secp256k1 odd", addresscodec.NodePublicKeyPrefix, append([]byte{3}, make([]byte, 32)...), true},
		{"unrecognized key type", addresscodec.NodePublicKeyPrefix, append([]byte{4}, make([]byte, 32)...), false},
		{"short key", addresscodec.NodePublicKeyPrefix, append([]byte{2}, make([]byte, 31)...), false},
		{"long key", addresscodec.NodePublicKeyPrefix, append([]byte{2}, make([]byte, 33)...), false},
		{"account public prefix", addresscodec.AccountPublicKeyPrefix, append([]byte{2}, make([]byte, 32)...), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key := addresscodec.Base58CheckEncode(tt.payload, tt.prefix)
			params, err := json.Marshal(map[string]any{"public_key": key})
			require.NoError(t, err)
			result, rpcErr := (&handlers.ManifestMethod{}).Handle(&types.RpcContext{}, params)
			if tt.valid {
				require.Nil(t, rpcErr)
				require.Equal(t, map[string]any{"requested": key, "details": map[string]any{"master_key": key, "ephemeral_key": key}}, result)
			} else {
				require.Nil(t, result)
				require.NotNil(t, rpcErr)
				require.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
				require.Equal(t, "Invalid parameters.", rpcErr.Message)
				require.Equal(t, key, rpcErr.Extra["requested"])
			}
		})
	}
}

func TestRPCManifestCacheLifecycle(t *testing.T) {
	cache := manifest.NewCache()
	services := servicesForCache(cache)
	master, oldSigning, serialized := applyRPCManifest(t, cache, 0, 0x22, "")
	masterKey, err := addresscodec.EncodeNodePublicKey(master[:])
	require.NoError(t, err)
	oldKey, err := addresscodec.EncodeNodePublicKey(oldSigning[:])
	require.NoError(t, err)
	for _, key := range []string{masterKey, oldKey} {
		out, rpcErr := callManifestRPC(t, services, key)
		require.Nil(t, rpcErr)
		require.Equal(t, map[string]any{
			"requested": key,
			"manifest":  base64.StdEncoding.EncodeToString(serialized),
			"details":   map[string]any{"master_key": masterKey, "ephemeral_key": oldKey, "seq": float64(0), "domain": ""},
		}, out)
	}
	_, newSigning, serialized := applyRPCManifest(t, cache, 1, 0x33, "example.org")
	newKey, err := addresscodec.EncodeNodePublicKey(newSigning[:])
	require.NoError(t, err)
	for _, key := range []string{masterKey, newKey} {
		out, rpcErr := callManifestRPC(t, services, key)
		require.Nil(t, rpcErr)
		require.Equal(t, map[string]any{
			"requested": key,
			"manifest":  base64.StdEncoding.EncodeToString(serialized),
			"details":   map[string]any{"master_key": masterKey, "ephemeral_key": newKey, "seq": float64(1), "domain": "example.org"},
		}, out)
	}
	out, rpcErr := callManifestRPC(t, services, oldKey)
	require.Nil(t, rpcErr)
	require.Equal(t, map[string]any{"requested": oldKey, "details": map[string]any{"master_key": oldKey, "ephemeral_key": oldKey}}, out)
	applyRPCManifest(t, cache, manifest.RevokedSequence, 0, "")
	out, rpcErr = callManifestRPC(t, services, masterKey)
	require.Nil(t, rpcErr)
	require.Equal(t, map[string]any{"requested": masterKey, "details": map[string]any{"master_key": masterKey, "ephemeral_key": masterKey}}, out)
	out, rpcErr = callManifestRPC(t, services, newKey)
	require.Nil(t, rpcErr)
	require.Equal(t, map[string]any{"requested": newKey, "details": map[string]any{"master_key": newKey, "ephemeral_key": newKey}}, out)
}

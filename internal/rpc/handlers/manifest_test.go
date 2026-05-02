package handlers_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/protocol"
)

// buildAndApplyManifest installs one valid manifest in a fresh cache
// and returns both the cache and the raw serialized manifest so the
// test can compare the base64 body in the RPC response.
func buildAndApplyManifest(t *testing.T) (*manifest.Cache, [33]byte, [33]byte, []byte) {
	t.Helper()

	masterPubBytes, masterPriv := deterministicEd25519KeypairRPC(0x11)
	ephPubBytes, ephPriv := deterministicEd25519KeypairRPC(0x22)

	var master [33]byte
	var ephemeral [33]byte
	copy(master[:], masterPubBytes)
	copy(ephemeral[:], ephPubBytes)

	jsonMap := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPubBytes),
		"SigningPubKey": hex.EncodeToString(ephPubBytes),
		"Sequence":      uint32(7),
		"Domain":        hex.EncodeToString([]byte("example.org")),
	}
	preimage := manifestPreimage(t, jsonMap)
	jsonMap["Signature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(ephPriv), preimage))
	jsonMap["MasterSignature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(masterPriv), preimage))

	encoded, err := binarycodec.Encode(jsonMap)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	serialized, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}

	m, err := manifest.Deserialize(serialized)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	c := manifest.NewCache()
	if d := c.ApplyManifest(m); d != manifest.Accepted {
		t.Fatalf("Apply: %s", d)
	}
	return c, master, ephemeral, serialized
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
	prefix := protocol.HashPrefixManifest
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

// servicesForCache builds a per-test ServiceContainer wired to the given
// manifest cache.
func servicesForCache(cache types.ManifestLookup) *types.ServiceContainer {
	return &types.ServiceContainer{Manifests: cache}
}

func callManifestRPC(t *testing.T, services *types.ServiceContainer, publicKey string) (map[string]any, *types.RpcError) {
	t.Helper()
	var params json.RawMessage
	if publicKey != "" {
		p, _ := json.Marshal(map[string]string{"public_key": publicKey})
		params = p
	} else {
		params = json.RawMessage(`{}`)
	}
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

func TestRPC_Manifest_ReturnsStoredState_ByMasterKey(t *testing.T) {
	cache, master, ephemeral, serialized := buildAndApplyManifest(t)
	services := servicesForCache(cache)

	masterB58, err := addresscodec.EncodeNodePublicKey(master[:])
	if err != nil {
		t.Fatalf("encode master: %v", err)
	}
	ephemeralB58, err := addresscodec.EncodeNodePublicKey(ephemeral[:])
	if err != nil {
		t.Fatalf("encode ephemeral: %v", err)
	}

	out, rpcErr := callManifestRPC(t, services, masterB58)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}

	if out["requested"] != masterB58 {
		t.Fatalf("requested: got %v want %s", out["requested"], masterB58)
	}
	b64, _ := out["manifest"].(string)
	if b64 == "" {
		t.Fatal("manifest field empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !bytes.Equal(decoded, serialized) {
		t.Fatalf("manifest bytes mismatch: err=%v", err)
	}
	details, _ := out["details"].(map[string]any)
	if details["master_key"] != masterB58 {
		t.Fatalf("details.master_key: got %v want %s", details["master_key"], masterB58)
	}
	if details["ephemeral_key"] != ephemeralB58 {
		t.Fatalf("details.ephemeral_key: got %v want %s", details["ephemeral_key"], ephemeralB58)
	}
	if seq, ok := details["seq"].(float64); !ok || uint32(seq) != 7 {
		t.Fatalf("details.seq: got %v want 7", details["seq"])
	}
	if details["domain"] != "example.org" {
		t.Fatalf("details.domain: got %v want example.org", details["domain"])
	}
}

func TestRPC_Manifest_ByEphemeralKey_ResolvesToMaster(t *testing.T) {
	cache, master, ephemeral, _ := buildAndApplyManifest(t)
	services := servicesForCache(cache)

	masterB58, _ := addresscodec.EncodeNodePublicKey(master[:])
	ephemeralB58, _ := addresscodec.EncodeNodePublicKey(ephemeral[:])

	// Query with the EPHEMERAL key — the handler must resolve up to
	// the master via GetMasterKey and return the master in details.
	out, rpcErr := callManifestRPC(t, services, ephemeralB58)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	if out["requested"] != ephemeralB58 {
		t.Fatalf("requested echoed wrong: %v", out["requested"])
	}
	details, _ := out["details"].(map[string]any)
	if details["master_key"] != masterB58 {
		t.Fatalf("details.master_key not resolved to master: got %v", details["master_key"])
	}
}

func TestRPC_Manifest_UnknownKey_ReturnsSparseResponse(t *testing.T) {
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
	if _, ok := out["details"]; ok {
		t.Fatal("details field should be absent for unknown key")
	}
}

func TestRPC_Manifest_MissingPublicKey_InvalidParams(t *testing.T) {
	cache := manifest.NewCache()
	services := servicesForCache(cache)

	_, rpcErr := callManifestRPC(t, services, "")
	if rpcErr == nil {
		t.Fatal("expected error on missing public_key")
	}
}

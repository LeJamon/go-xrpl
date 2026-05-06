package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/stretchr/testify/require"
)

// installManifest builds and applies a valid master/ephemeral binding
// to the cache, returning the master and ephemeral pubkeys.
func installManifest(t *testing.T, cache *manifest.Cache, masterSeed, ephSeed byte) (master [33]byte, ephemeral [33]byte) {
	t.Helper()
	serialized := buildWireManifest(t, 1, masterSeed, ephSeed)
	parsed, err := manifest.Deserialize(serialized)
	require.NoError(t, err)
	if disp := cache.ApplyManifest(parsed); disp != manifest.Accepted {
		t.Fatalf("ApplyManifest disposition: got %v want Accepted", disp)
	}
	return parsed.MasterKey, parsed.SigningKey
}

// TestRouter_ResolveMasterNodeID_Validation_RewritesToMaster is the
// regression guard for issue #265, restated for the new router-seam
// translation. An inbound validation signed by a rotated ephemeral key
// whose master is bound in the manifest cache must reach the engine
// with NodeID == calcNodeID(master) — matching rippled
// RCLValidations.cpp:188-190 calcNodeID(masterKey.value_or(signingKey)).
func TestRouter_ResolveMasterNodeID_Validation_RewritesToMaster(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, nil, inbox)

	cache := manifest.NewCache()
	master, ephemeral := installManifest(t, cache, 0x40, 0x41)
	router.SetManifestCache(cache, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	v := &consensus.Validation{
		Full:      true,
		LedgerSeq: 99,
		SignTime:  time.Unix(protocol.RippleEpochUnix+12345, 0),
	}
	for i := range v.LedgerID {
		v.LedgerID[i] = byte(i + 1)
	}
	v.SigningPubKey = consensus.SigningPubKey(ephemeral)
	v.NodeID = consensus.CalcNodeID(ephemeral)
	v.Signature = make([]byte, 70)

	inbox <- &peermanagement.InboundMessage{
		PeerID:  9,
		Type:    uint16(message.TypeValidation),
		Payload: encodePayload(t, &message.Validation{Validation: SerializeSTValidation(v)}),
	}

	wantNodeID := consensus.CalcNodeID(master)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if vs := engine.getValidations(); len(vs) == 1 {
			if vs[0].NodeID != wantNodeID {
				t.Fatalf("validation NodeID = %x, want calcNodeID(master) = %x (signing-derived = %x)",
					vs[0].NodeID, wantNodeID, consensus.CalcNodeID(ephemeral))
			}
			if vs[0].SigningPubKey != consensus.SigningPubKey(ephemeral) {
				t.Fatalf("validation SigningPubKey diverged: got %x want %x",
					vs[0].SigningPubKey, ephemeral)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("router did not dispatch validation within deadline")
}

// TestRouter_ResolveMasterNodeID_Validation_NoMappingPreservesSigning
// is the baseline: without a manifest mapping, the parser-default
// NodeID = calcNodeID(signingKey) must round-trip untouched. Guards
// against accidentally overwriting NodeID with calcNodeID(master) when
// master is unknown — which would invent identities for non-validator
// peers and break trust comparisons.
func TestRouter_ResolveMasterNodeID_Validation_NoMappingPreservesSigning(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, nil, inbox)

	cache := manifest.NewCache()
	router.SetManifestCache(cache, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	var signing [33]byte
	signing[0] = 0xED
	for i := 1; i < len(signing); i++ {
		signing[i] = byte(i)
	}

	v := &consensus.Validation{
		Full:      true,
		LedgerSeq: 100,
		SignTime:  time.Unix(protocol.RippleEpochUnix+12345, 0),
	}
	for i := range v.LedgerID {
		v.LedgerID[i] = byte(i + 1)
	}
	v.SigningPubKey = consensus.SigningPubKey(signing)
	v.NodeID = consensus.CalcNodeID(signing)
	v.Signature = make([]byte, 70)

	inbox <- &peermanagement.InboundMessage{
		PeerID:  10,
		Type:    uint16(message.TypeValidation),
		Payload: encodePayload(t, &message.Validation{Validation: SerializeSTValidation(v)}),
	}

	wantNodeID := consensus.CalcNodeID(signing)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if vs := engine.getValidations(); len(vs) == 1 {
			if vs[0].NodeID != wantNodeID {
				t.Fatalf("NodeID = %x, want calcNodeID(signingKey) = %x (no manifest mapping should leave parser default intact)",
					vs[0].NodeID, wantNodeID)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("router did not dispatch validation within deadline")
}

// TestRouter_ResolveMasterNodeID_HelperContract pins the function-level
// contract of resolveMasterNodeID independently of the wire path:
// the helper rewrites *nid only when the manifest cache has a real
// signing→master binding distinct from the input. handleProposal and
// handleValidation both delegate to this helper at router.go:411,473,
// so this guards against a regression in the helper itself even when
// the wire seams above are correctly wiring it.
func TestRouter_ResolveMasterNodeID_HelperContract(t *testing.T) {
	router := NewRouter(&mockEngine{}, newTestAdaptor(t), nil, make(chan *peermanagement.InboundMessage, 1))
	cache := manifest.NewCache()
	master, ephemeral := installManifest(t, cache, 0x60, 0x61)
	router.SetManifestCache(cache, nil)

	t.Run("known mapping rewrites to calcNodeID(master)", func(t *testing.T) {
		nid := consensus.CalcNodeID(ephemeral)
		router.resolveMasterNodeID(&nid, consensus.SigningPubKey(ephemeral))
		if nid != consensus.CalcNodeID(master) {
			t.Fatalf("nid = %x, want calcNodeID(master) = %x", nid, consensus.CalcNodeID(master))
		}
	})

	t.Run("unknown signing leaves parser default intact", func(t *testing.T) {
		var unknown [33]byte
		unknown[0] = 0xED
		for i := 1; i < len(unknown); i++ {
			unknown[i] = byte(0xA0 ^ i)
		}
		nid := consensus.CalcNodeID(unknown)
		want := nid
		router.resolveMasterNodeID(&nid, consensus.SigningPubKey(unknown))
		if nid != want {
			t.Fatalf("nid mutated for unknown signing: got %x want %x", nid, want)
		}
	})

	t.Run("nil cache is a no-op", func(t *testing.T) {
		bare := NewRouter(&mockEngine{}, newTestAdaptor(t), nil, make(chan *peermanagement.InboundMessage, 1))
		nid := consensus.CalcNodeID(ephemeral)
		want := nid
		bare.resolveMasterNodeID(&nid, consensus.SigningPubKey(ephemeral))
		if nid != want {
			t.Fatalf("nil-cache router mutated nid: got %x want %x", nid, want)
		}
	})
}

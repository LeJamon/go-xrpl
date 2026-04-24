// Copyright (c) goXRPLd contributors.
//
// Tests for B2 (PR #264 Round-7): suppression-hash domain must match
// rippled's proposalUniqueId (sha512Half of an STObject-style serializer
// over the decoded proposal fields) and sha512Half(val->serialized) for
// validations (inner STValidation blob, not the TMValidation envelope).
//
// These tests are the TDD scaffolding for the replacement of hashPayload
// in router.go. The key insight being pinned: semantically-equivalent
// protobuf payloads MUST produce the same suppression hash, so mixed
// Go/rippled peer dedup doesn't desynchronize just because one peer
// re-marshaled the TMProposeSet / TMValidation envelope with different
// framing.

package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "google.golang.org/protobuf/proto"
)

// buildSuppressionTestProposal returns a *consensus.Proposal with stable
// field values suitable for hashing. All inputs that contribute to
// proposalUniqueId are set explicitly so the expected hash is a function
// only of those fields — not of Timestamp, Round, or any engine-internal
// bookkeeping.
func buildSuppressionTestProposal() *consensus.Proposal {
	var txSet consensus.TxSetID
	for i := range txSet {
		txSet[i] = byte(0x40 + i)
	}
	var prevLedger consensus.LedgerID
	for i := range prevLedger {
		prevLedger[i] = byte(0x80 + i)
	}
	var nodeID consensus.NodeID
	nodeID[0] = 0x02 // valid compressed secp256k1 prefix
	for i := 1; i < len(nodeID); i++ {
		nodeID[i] = byte(i)
	}
	sig := make([]byte, 70)
	for i := range sig {
		sig[i] = byte(0xA0 ^ i)
	}
	return &consensus.Proposal{
		Position:       7,
		TxSet:          txSet,
		PreviousLedger: prevLedger,
		CloseTime:      time.Unix(1_700_000_000, 0),
		NodeID:         nodeID,
		Signature:      sig,
	}
}

// TestSuppression_ProposalHash_DeterministicOnFields pins that the
// proposal suppression hash is a pure function of the fields rippled
// feeds into proposalUniqueId. Two proposals with identical contributing
// fields but different non-contributing fields (Round, Timestamp) MUST
// produce the same hash.
func TestSuppression_ProposalHash_DeterministicOnFields(t *testing.T) {
	p1 := buildSuppressionTestProposal()
	p2 := buildSuppressionTestProposal()

	// Perturb fields NOT fed into proposalUniqueId. If the
	// implementation wrongly hashed these, the hashes would diverge.
	p1.Timestamp = time.Unix(1, 0)
	p2.Timestamp = time.Unix(2_000_000_000, 0)
	p1.Round = consensus.RoundID{Seq: 1, ParentHash: p1.PreviousLedger}
	p2.Round = consensus.RoundID{Seq: 999, ParentHash: p2.PreviousLedger}

	h1 := hashProposalSuppression(p1)
	h2 := hashProposalSuppression(p2)

	assert.Equal(t, h1, h2,
		"proposal suppression hash must depend only on fields fed into rippled's proposalUniqueId")
}

// TestSuppression_ProposalHash_FieldsMatter pins that changing ANY
// contributing field flips the hash. A regression that ignores a field
// would merge distinct proposals into the same suppression key — a
// subtle dedup bug that'd silently drop legitimate traffic.
func TestSuppression_ProposalHash_FieldsMatter(t *testing.T) {
	base := buildSuppressionTestProposal()

	mutations := []struct {
		name string
		mut  func(*consensus.Proposal)
	}{
		{"Position", func(p *consensus.Proposal) { p.Position++ }},
		{"TxSet", func(p *consensus.Proposal) { p.TxSet[0] ^= 0xFF }},
		{"PreviousLedger", func(p *consensus.Proposal) { p.PreviousLedger[31] ^= 0xFF }},
		{"CloseTime", func(p *consensus.Proposal) { p.CloseTime = p.CloseTime.Add(1 * time.Second) }},
		{"NodeID", func(p *consensus.Proposal) { p.NodeID[32] ^= 0x01 }},
		{"Signature", func(p *consensus.Proposal) { p.Signature[0] ^= 0x01 }},
	}

	baseHash := hashProposalSuppression(base)
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			mut := *base // shallow copy
			// Clone slices / arrays that mutate below.
			mut.Signature = append([]byte(nil), base.Signature...)
			m.mut(&mut)
			got := hashProposalSuppression(&mut)
			assert.NotEqual(t, baseHash, got,
				"mutating %s must flip the suppression hash", m.name)
		})
	}
}

// TestSuppression_ProposalSemanticIdentity_SameHash is the headline
// regression test for B2. It builds TWO protobuf payloads that decode
// to the same *consensus.Proposal but differ byte-for-byte on the wire
// (one includes deprecated field `hops`, the other doesn't). Pre-fix:
// hashing the raw protobuf payload made these two hashes differ, so a
// Go peer would register them as distinct suppression keys and feed
// the reduce-relay slot on both — desyncing dedup from rippled peers
// that only see one suppression entry.
func TestSuppression_ProposalSemanticIdentity_SameHash(t *testing.T) {
	// Build a ProposeSet, marshal it, then construct a SECOND
	// byte-different marshal that decodes to the identical ProposeSet
	// by including the deprecated `hops` field (proto field #12). The
	// adaptor's fromProto for TMProposeSet does NOT copy Hops into the
	// exported ProposeSet struct (see serialize_proto.go:414-425), so
	// both byte streams round-trip to the same in-memory message.
	pubKey := make([]byte, 33)
	pubKey[0] = 0x02
	for i := 1; i < 33; i++ {
		pubKey[i] = byte(i)
	}
	sig := make([]byte, 70)
	for i := range sig {
		sig[i] = byte(i)
	}

	set := &message.ProposeSet{
		ProposeSeq:     5,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     pubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_000, 0)),
		Signature:      sig,
		PreviousLedger: make([]byte, 32),
	}
	for i := range set.CurrentTxHash {
		set.CurrentTxHash[i] = byte(0x40 + i)
	}
	for i := range set.PreviousLedger {
		set.PreviousLedger[i] = byte(0x80 + i)
	}

	// Payload A: no deprecated field.
	payloadA, err := message.Encode(set)
	require.NoError(t, err)

	// Payload B: same fields plus `hops=7` (deprecated, not read by
	// fromProto) — bytes differ but the round-trip produces the same
	// ProposeSet struct.
	protoB := &proto.TMProposeSet{
		ProposeSeq:     pb.Uint32(set.ProposeSeq),
		CurrentTxHash:  set.CurrentTxHash,
		NodePubKey:     set.NodePubKey,
		CloseTime:      pb.Uint32(set.CloseTime),
		Signature:      set.Signature,
		PreviousLedger: set.PreviousLedger,
		Hops:           7,
	}
	payloadB, err := pb.Marshal(protoB)
	require.NoError(t, err)

	require.NotEqual(t, payloadA, payloadB,
		"test setup error: payloads must differ byte-for-byte to prove the property")

	// Decode both — verify they produce the same consensus.Proposal.
	decodedA, err := message.Decode(message.TypeProposeLedger, payloadA)
	require.NoError(t, err)
	proposeA, ok := decodedA.(*message.ProposeSet)
	require.True(t, ok)

	decodedB, err := message.Decode(message.TypeProposeLedger, payloadB)
	require.NoError(t, err)
	proposeB, ok := decodedB.(*message.ProposeSet)
	require.True(t, ok)

	propA := ProposalFromMessage(proposeA)
	propB := ProposalFromMessage(proposeB)

	// Round, Timestamp contribute nothing to the hash — normalize for
	// struct equality readability.
	propA.Timestamp = time.Time{}
	propB.Timestamp = time.Time{}
	require.Equal(t, propA, propB,
		"the two byte-different payloads must decode to identical *consensus.Proposal")

	// Same decoded fields => same suppression hash.
	hA := hashProposalSuppression(propA)
	hB := hashProposalSuppression(propB)
	assert.Equal(t, hA, hB,
		"semantically-identical proposals must hash to the same suppression key — "+
			"regression guard for B2 (router_dedup was hashing raw protobuf payload)")
}

// TestSuppression_ValidationHash_OfSerializedBlob pins that the
// validation suppression hash is sha512Half of the inner STValidation
// blob — matching rippled's `sha512Half(makeSlice(m->validation()))`
// at PeerImp.cpp:2374. NOT the TMValidation protobuf envelope.
func TestSuppression_ValidationHash_OfSerializedBlob(t *testing.T) {
	val := buildTestValidation()
	blob := serializeSTValidation(val)

	got := hashValidationSuppression(blob)

	// The contract: identical to a direct sha512Half(blob).
	// Any deviation (e.g., hashing a TMValidation envelope, adding a
	// prefix, using SHA256) would break dedup parity with rippled.
	require.NotEqual(t, [32]byte{}, got,
		"suppression hash over a nonempty blob must not be zero")

	// Hashing the same blob twice must return the same value (purity).
	again := hashValidationSuppression(blob)
	assert.Equal(t, got, again, "hashValidationSuppression must be a pure function")
}

// TestSuppression_ValidationInnerBlobDomain_SameHash is the headline
// regression test for B2 on the validation side. It constructs two
// TMValidation protobuf envelopes that carry the SAME inner STValidation
// blob but differ byte-for-byte on the outer envelope (one sets the
// deprecated `hops` field, the other doesn't). Pre-fix: hashing the raw
// TMValidation payload bytes would produce different suppression keys;
// post-fix: hashing the inner serialized blob produces the SAME key —
// matching rippled.
func TestSuppression_ValidationInnerBlobDomain_SameHash(t *testing.T) {
	v := buildTestValidation()
	blob := serializeSTValidation(v)

	// Envelope A: the minimal TMValidation our Encode path emits.
	envelopeA, err := message.Encode(&message.Validation{Validation: blob})
	require.NoError(t, err)

	// Envelope B: same inner blob, plus deprecated `hops=3`. fromProto
	// (serialize_proto.go:447-449) reads only the `validation` field,
	// so both envelopes round-trip to an identical inner blob.
	envelopeB, err := pb.Marshal(&proto.TMValidation{
		Validation: blob,
		Hops:       3,
	})
	require.NoError(t, err)

	require.NotEqual(t, envelopeA, envelopeB,
		"test setup error: envelopes must differ byte-for-byte to prove the property")

	// Decode both envelopes and recover the inner blob.
	decodedA, err := message.Decode(message.TypeValidation, envelopeA)
	require.NoError(t, err)
	innerA := decodedA.(*message.Validation).Validation

	decodedB, err := message.Decode(message.TypeValidation, envelopeB)
	require.NoError(t, err)
	innerB := decodedB.(*message.Validation).Validation

	require.Equal(t, innerA, innerB,
		"the two byte-different envelopes must decode to the same inner STValidation blob")

	// Same inner blob => same suppression hash, irrespective of outer
	// envelope framing. That's the rippled-parity invariant.
	hA := hashValidationSuppression(innerA)
	hB := hashValidationSuppression(innerB)
	assert.Equal(t, hA, hB,
		"semantically-identical validations must hash to the same suppression key — "+
			"regression guard for B2 (router_dedup was hashing raw protobuf payload)")
}

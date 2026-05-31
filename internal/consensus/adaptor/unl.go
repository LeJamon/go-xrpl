package adaptor

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// UNL (Unique Node List) manages the set of trusted validators.
type UNL struct {
	validators []consensus.NodeID
	set        map[consensus.NodeID]struct{}
	quorum     int
}

// NewUNL creates a UNL from a list of base58-encoded validator public keys.
// Keys are n-prefixed base58 encoded compressed public keys (33 bytes).
func NewUNL(keys []string) (*UNL, error) {
	validators := make([]consensus.NodeID, 0, len(keys))
	set := make(map[consensus.NodeID]struct{}, len(keys))

	for _, key := range keys {
		nodeID, err := DecodeValidatorKey(key)
		if err != nil {
			return nil, fmt.Errorf("invalid validator key %q: %w", key, err)
		}
		if _, exists := set[nodeID]; exists {
			continue // deduplicate
		}
		validators = append(validators, nodeID)
		set[nodeID] = struct{}{}
	}

	n := len(validators)
	// Quorum: ceil(n * 0.8) matching rippled's calcQuorum
	quorum := (n*4 + 4) / 5
	if quorum < 1 && n > 0 {
		quorum = 1
	}

	return &UNL{
		validators: validators,
		set:        set,
		quorum:     quorum,
	}, nil
}

// IsTrusted returns true if the node is in the UNL.
func (u *UNL) IsTrusted(node consensus.NodeID) bool {
	_, ok := u.set[node]
	return ok
}

// Validators returns the full list of trusted validator NodeIDs.
func (u *UNL) Validators() []consensus.NodeID {
	result := make([]consensus.NodeID, len(u.validators))
	copy(result, u.validators)
	return result
}

// Quorum returns the number of validators needed for consensus.
func (u *UNL) Quorum() int {
	return u.quorum
}

// Size returns the number of validators in the UNL.
func (u *UNL) Size() int {
	return len(u.validators)
}

// DecodeValidatorKey decodes a base58-encoded n-prefixed node public
// key into the 20-byte consensus.NodeID identifier rippled uses for
// trust / quorum lookups. The base58 form configured by operators (in
// `[validators]`) carries a 33-byte master public key; this helper
// applies calcNodeID (RIPEMD-160(SHA-256(masterPubKey))) so the trust
// set is keyed identically to the inbound NodeID values populated by
// the consensus router.
func DecodeValidatorKey(key string) (consensus.NodeID, error) {
	nodeID, _, err := DecodeValidatorKeyWithMaster(key)
	return nodeID, err
}

// DecodeValidatorKeyWithMaster decodes a base58-encoded validator
// public key into both its 20-byte NodeID and the underlying 33-byte
// master pubkey. NegativeUNL voting needs the raw master because the
// UNLModify pseudo-tx carries the master pubkey on the wire — see
// NegativeUNLVote.cpp:118-120 (sfUNLModifyValidator is the master).
func DecodeValidatorKeyWithMaster(key string) (nodeID consensus.NodeID, master [33]byte, err error) {
	// Guard against panics in the base58 decoder for malformed input
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid key encoding: %v", r)
		}
	}()

	decoded, decErr := addresscodec.DecodeNodePublicKey(key)
	if decErr != nil {
		return consensus.NodeID{}, [33]byte{}, fmt.Errorf("decode node public key: %w", decErr)
	}
	if len(decoded) != 33 {
		return consensus.NodeID{}, [33]byte{}, fmt.Errorf("unexpected key length: got %d, want 33", len(decoded))
	}
	copy(master[:], decoded)
	return consensus.CalcNodeID(master), master, nil
}

// CalcQuorum computes the quorum for n validators matching rippled.
// Formula: ceil(n * 0.8)
func CalcQuorum(n int) int {
	if n <= 0 {
		return 0
	}
	q := (n*4 + 4) / 5
	if q < 1 {
		q = 1
	}
	return q
}

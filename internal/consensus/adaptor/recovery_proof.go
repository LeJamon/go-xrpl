package adaptor

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	// ErrRecoveryProofUnavailable means the node cannot currently establish a
	// finality quorum (for example during a trust transition).
	ErrRecoveryProofUnavailable = errors.New("validation quorum proof is unavailable")
	// ErrRecoveryProofInvalid means persisted evidence no longer authenticates
	// the exact target under the current trust context.
	ErrRecoveryProofInvalid = errors.New("validation quorum proof is invalid")
)

// CaptureValidationQuorumProof snapshots the exact signed validations that
// currently establish quorum. The raw STValidation bytes are retained so the
// proof can be rechecked after the in-memory validation tracker is rebuilt.
func (a *Adaptor) CaptureValidationQuorumProof(seq uint32, hash [32]byte) (consensus.ValidationQuorumProof, error) {
	if a == nil || seq == 0 || hash == ([32]byte{}) {
		return consensus.ValidationQuorumProof{}, ErrRecoveryProofUnavailable
	}

	a.mu.Lock()
	historian := a.validationHistorian
	a.mu.Unlock()
	rechecker, ok := historian.(consensus.ValidationQuorumRechecker)
	if !ok {
		return consensus.ValidationQuorumProof{}, ErrRecoveryProofUnavailable
	}
	trusted, trustedMasterKeys, quorum, negativeUNL := a.validationProofTrustContext()
	if quorum <= 0 || a.IsQuorumUnavailable() {
		return consensus.ValidationQuorumProof{}, ErrRecoveryProofUnavailable
	}
	validations, trackerQuorum, accepted := rechecker.RecheckFullyValidated(consensus.LedgerID(hash), seq)
	if !accepted || trackerQuorum != quorum || len(validations) < quorum {
		return consensus.ValidationQuorumProof{}, ErrRecoveryProofUnavailable
	}

	proof := consensus.ValidationQuorumProof{
		Version:           consensus.ValidationQuorumProofVersion,
		NetworkID:         a.validationNetworkID(),
		LedgerID:          consensus.LedgerID(hash),
		LedgerSeq:         seq,
		Quorum:            quorum,
		Trusted:           append([]consensus.NodeID(nil), trusted...),
		TrustedMasterKeys: append([][33]byte(nil), trustedMasterKeys...),
		NegativeUNL:       append([]consensus.NodeID(nil), negativeUNL...),
	}
	proof.TrustHash = validationTrustHash(proof.Trusted, proof.TrustedMasterKeys, proof.Quorum, proof.NegativeUNL)
	proof.Validations = make([]consensus.SignedValidationEvidence, 0, len(validations))
	for _, validation := range validations {
		evidence, err := a.signedValidationEvidence(validation, proof.LedgerID, seq)
		if err != nil {
			return consensus.ValidationQuorumProof{}, fmt.Errorf("capture validator evidence: %w", err)
		}
		proof.Validations = append(proof.Validations, evidence)
	}
	if err := a.validateValidationProof(proof); err != nil {
		return consensus.ValidationQuorumProof{}, err
	}
	return proof, nil
}

// CaptureValidationQuorumEvidence returns the proof in the form stored in a
// replay-fault record. The bytes are copied so a caller may persist them
// without retaining mutable proof state.
func (a *Adaptor) CaptureValidationQuorumEvidence(seq uint32, hash [32]byte) (json.RawMessage, error) {
	proof, err := a.CaptureValidationQuorumProof(seq, hash)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return nil, fmt.Errorf("marshal validation quorum proof: %w", err)
	}
	return append(json.RawMessage(nil), raw...), nil
}

// RecheckValidationQuorumProof authenticates a durable proof against the
// current trust snapshot. It deliberately does not apply the short ingress
// freshness window: this is restart recovery of an already signed transition,
// not admission of a new network validation.
func (a *Adaptor) RecheckValidationQuorumProof(proof consensus.ValidationQuorumProof) error {
	if a == nil {
		return ErrRecoveryProofUnavailable
	}
	return a.validateValidationProof(proof)
}

// AuthenticateValidationQuorumEvidence rechecks persisted proof bytes after
// restart. The target arguments come from the durable recovery target and are
// checked separately from the proof payload so an operator cannot retarget a
// recovery by replacing only its evidence.
func (a *Adaptor) AuthenticateValidationQuorumEvidence(seq uint32, hash [32]byte, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: evidence is empty", ErrRecoveryProofInvalid)
	}
	var proof consensus.ValidationQuorumProof
	if err := json.Unmarshal(raw, &proof); err != nil {
		return fmt.Errorf("%w: decode evidence: %v", ErrRecoveryProofInvalid, err)
	}
	if proof.LedgerSeq != seq || proof.LedgerID != consensus.LedgerID(hash) {
		return fmt.Errorf("%w: evidence target does not match recovery target", ErrRecoveryProofInvalid)
	}
	return a.RecheckValidationQuorumProof(proof)
}

func (a *Adaptor) validateValidationProof(proof consensus.ValidationQuorumProof) error {
	if proof.Version != consensus.ValidationQuorumProofVersion {
		return fmt.Errorf("%w: unsupported proof version %d", ErrRecoveryProofInvalid, proof.Version)
	}
	if proof.LedgerSeq == 0 || proof.LedgerID == (consensus.LedgerID{}) || proof.Quorum <= 0 {
		return fmt.Errorf("%w: target and quorum are required", ErrRecoveryProofInvalid)
	}
	if proof.NetworkID != a.validationNetworkID() {
		return fmt.Errorf("%w: network ID changed from %d to %d", ErrRecoveryProofInvalid, proof.NetworkID, a.validationNetworkID())
	}
	trusted, trustedMasterKeys, quorum, negativeUNL := a.validationProofTrustContext()
	if quorum <= 0 || a.IsQuorumUnavailable() {
		return ErrRecoveryProofUnavailable
	}
	if proof.Quorum != quorum {
		return fmt.Errorf("%w: quorum changed from %d to %d", ErrRecoveryProofInvalid, proof.Quorum, quorum)
	}
	if !sameNodeSet(proof.Trusted, trusted) || !sameNodeSet(proof.NegativeUNL, negativeUNL) || !sameMasterSet(proof.Trusted, proof.TrustedMasterKeys, trusted, trustedMasterKeys) {
		return fmt.Errorf("%w: trust context changed", ErrRecoveryProofInvalid)
	}
	if proof.TrustHash != validationTrustHash(trusted, trustedMasterKeys, quorum, negativeUNL) {
		return fmt.Errorf("%w: trust context hash mismatch", ErrRecoveryProofInvalid)
	}
	if len(proof.Validations) < quorum {
		return fmt.Errorf("%w: %d signed validations do not meet quorum %d", ErrRecoveryProofInvalid, len(proof.Validations), quorum)
	}

	trustedSet := make(map[consensus.NodeID]struct{}, len(trusted))
	for _, node := range trusted {
		trustedSet[node] = struct{}{}
	}
	negativeSet := make(map[consensus.NodeID]struct{}, len(negativeUNL))
	for _, node := range negativeUNL {
		negativeSet[node] = struct{}{}
	}
	seen := make(map[consensus.NodeID]struct{}, len(proof.Validations))
	for i, evidence := range proof.Validations {
		if evidence.NodeID == (consensus.NodeID{}) || evidence.MasterPubKey == ([33]byte{}) || len(evidence.Raw) == 0 {
			return fmt.Errorf("%w: validation %d has no signer or raw bytes", ErrRecoveryProofInvalid, i)
		}
		if _, ok := trustedSet[evidence.NodeID]; !ok {
			return fmt.Errorf("%w: validation %d is not in the trusted set", ErrRecoveryProofInvalid, i)
		}
		if _, disabled := negativeSet[evidence.NodeID]; disabled {
			return fmt.Errorf("%w: validation %d is in the negative UNL", ErrRecoveryProofInvalid, i)
		}
		if _, duplicate := seen[evidence.NodeID]; duplicate {
			return fmt.Errorf("%w: duplicate signer %x", ErrRecoveryProofInvalid, evidence.NodeID)
		}
		seen[evidence.NodeID] = struct{}{}

		validation, err := parseSTValidation(evidence.Raw)
		if err != nil {
			return fmt.Errorf("%w: parse validation %d: %v", ErrRecoveryProofInvalid, i, err)
		}
		if validation.SigningPubKey != evidence.SigningPubKey {
			return fmt.Errorf("%w: validation %d signing key changed", ErrRecoveryProofInvalid, i)
		}
		if validation.LedgerID != proof.LedgerID || validation.LedgerSeq != proof.LedgerSeq {
			return fmt.Errorf("%w: validation %d target changed", ErrRecoveryProofInvalid, i)
		}
		if !validation.Full {
			return fmt.Errorf("%w: validation %d is partial", ErrRecoveryProofInvalid, i)
		}
		if !hasSignedValidationTime(validation.SignTime) {
			return fmt.Errorf("%w: validation %d has no signing time", ErrRecoveryProofInvalid, i)
		}
		// NodeID is resolved from a validator manifest before a validation
		// enters the tracker. Restore that resolved identity from the durable
		// evidence before checking trust membership and the signature itself.
		if !validManifestAuthorization(evidence, validation.SigningPubKey) {
			return fmt.Errorf("%w: validation %d signer identity is not authorized", ErrRecoveryProofInvalid, i)
		}
		validation.NodeID = evidence.NodeID
		if err := verifyValidation(validation); err != nil {
			return fmt.Errorf("%w: validation %d signature: %v", ErrRecoveryProofInvalid, i, err)
		}
	}
	return nil
}

// resolveValidationSigner resolves a live validation's ephemeral key to the
// master identity and signed manifest that will be persisted with the proof.
// Without a resolver, only seed-only identities whose signing key is their
// master key are accepted.
func (a *Adaptor) resolveValidationSigner(signing consensus.SigningPubKey) (consensus.NodeID, [33]byte, []byte, bool) {
	a.mu.Lock()
	resolver := a.validationSignerResolver
	a.mu.Unlock()
	if resolver != nil {
		return resolver(signing)
	}
	return consensus.CalcNodeID([33]byte(signing)), [33]byte(signing), nil, true
}

func (a *Adaptor) validationNetworkID() uint32 {
	if a == nil {
		return 0
	}
	return a.networkID
}

// validationProofTrustContext returns the validator/master-key pairing and
// quorum inputs from one trust snapshot. Calling GetValidationConfig and
// GetTrustedMasterKeys independently could observe two adjacent trust
// transitions and combine unrelated entries in a persisted proof.
func (a *Adaptor) validationProofTrustContext() ([]consensus.NodeID, [][33]byte, int, []consensus.NodeID) {
	a.trustUpdateMu.Lock()
	defer a.trustUpdateMu.Unlock()

	trusted, quorum, negativeUNL := a.validationConfig()
	a.mu.Lock()
	trustedMasterKeys := append([][33]byte(nil), a.trustedMasterKeys...)
	a.mu.Unlock()
	return trusted, trustedMasterKeys, quorum, negativeUNL
}

func (a *Adaptor) signedValidationEvidence(v *consensus.Validation, target consensus.LedgerID, seq uint32) (consensus.SignedValidationEvidence, error) {
	if v == nil || v.LedgerID != target || v.LedgerSeq != seq || !v.Full || !hasSignedValidationTime(v.SignTime) {
		return consensus.SignedValidationEvidence{}, errors.New("validation is not a full vote for the target")
	}
	if v.NodeID == (consensus.NodeID{}) || len(v.Raw) == 0 {
		return consensus.SignedValidationEvidence{}, errors.New("validation has no resolved signer or raw bytes")
	}
	canonical, err := CanonicalSTValidation(v)
	if err != nil {
		return consensus.SignedValidationEvidence{}, fmt.Errorf("canonicalize validation: %w", err)
	}
	parsed, err := parseSTValidation(canonical)
	if err != nil {
		return consensus.SignedValidationEvidence{}, fmt.Errorf("parse canonical validation: %w", err)
	}
	if parsed.LedgerID != target || parsed.LedgerSeq != seq || !parsed.Full ||
		!hasSignedValidationTime(parsed.SignTime) || parsed.SigningPubKey != v.SigningPubKey {
		return consensus.SignedValidationEvidence{}, errors.New("canonical validation changed its signed target")
	}
	parsed.NodeID = v.NodeID
	if err := verifyValidation(parsed); err != nil {
		return consensus.SignedValidationEvidence{}, err
	}
	_, master, serializedManifest, ok := a.resolveValidationSigner(parsed.SigningPubKey)
	if !ok || consensus.CalcNodeID(master) != v.NodeID {
		return consensus.SignedValidationEvidence{}, errors.New("validation signer identity is not authorized")
	}
	evidence := consensus.SignedValidationEvidence{
		NodeID:        v.NodeID,
		MasterPubKey:  master,
		SigningPubKey: parsed.SigningPubKey,
		Manifest:      append([]byte(nil), serializedManifest...),
		Raw:           append([]byte(nil), canonical...),
	}
	if !validManifestAuthorization(evidence, parsed.SigningPubKey) {
		return consensus.SignedValidationEvidence{}, errors.New("validation signer manifest is not authorized")
	}
	return evidence, nil
}

func hasSignedValidationTime(signTime time.Time) bool {
	return !signTime.IsZero() && protocol.RippleSeconds(signTime) > 0
}

func validManifestAuthorization(evidence consensus.SignedValidationEvidence, signing consensus.SigningPubKey) bool {
	if evidence.MasterPubKey == ([33]byte{}) || evidence.NodeID != consensus.CalcNodeID(evidence.MasterPubKey) {
		return false
	}
	if len(evidence.Manifest) == 0 {
		return evidence.MasterPubKey == [33]byte(signing)
	}
	m, err := manifest.Deserialize(evidence.Manifest)
	if err != nil || m.Revoked() || m.MasterKey() != evidence.MasterPubKey || m.SigningKey() != [33]byte(signing) {
		return false
	}
	return m.Verify() == nil
}

func sameNodeSet(a, b []consensus.NodeID) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]consensus.NodeID(nil), a...)
	right := append([]consensus.NodeID(nil), b...)
	sort.Slice(left, func(i, j int) bool { return bytes.Compare(left[i][:], left[j][:]) < 0 })
	sort.Slice(right, func(i, j int) bool { return bytes.Compare(right[i][:], right[j][:]) < 0 })
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sameMasterSet(nodesA []consensus.NodeID, mastersA [][33]byte, nodesB []consensus.NodeID, mastersB [][33]byte) bool {
	if len(nodesA) != len(nodesB) || len(mastersA) != len(mastersB) {
		return false
	}
	if len(mastersA) == 0 {
		return true
	}
	// A master-key slice is only meaningful when it is index-aligned with
	// its validator slice. Reject malformed persisted snapshots before the
	// pair construction below can index past either validator list.
	if len(mastersA) != len(nodesA) || len(mastersB) != len(nodesB) {
		return false
	}
	type identity struct {
		node   consensus.NodeID
		master [33]byte
	}
	left := make([]identity, len(mastersA))
	right := make([]identity, len(mastersB))
	for i := range mastersA {
		left[i] = identity{node: nodesA[i], master: mastersA[i]}
		right[i] = identity{node: nodesB[i], master: mastersB[i]}
	}
	less := func(values []identity) func(int, int) bool {
		return func(i, j int) bool {
			if bytes.Compare(values[i].node[:], values[j].node[:]) != 0 {
				return bytes.Compare(values[i].node[:], values[j].node[:]) < 0
			}
			return bytes.Compare(values[i].master[:], values[j].master[:]) < 0
		}
	}
	sort.Slice(left, less(left))
	sort.Slice(right, less(right))
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validationTrustHash(trusted []consensus.NodeID, trustedMasterKeys [][33]byte, quorum int, negativeUNL []consensus.NodeID) [32]byte {
	trusted = append([]consensus.NodeID(nil), trusted...)
	trustedMasterKeys = append([][33]byte(nil), trustedMasterKeys...)
	negativeUNL = append([]consensus.NodeID(nil), negativeUNL...)
	sort.Slice(negativeUNL, func(i, j int) bool { return bytes.Compare(negativeUNL[i][:], negativeUNL[j][:]) < 0 })
	h := sha256.New()
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(quorum))
	h.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(len(trusted)))
	h.Write(number[:])
	if len(trusted) == len(trustedMasterKeys) {
		type identity struct {
			node   consensus.NodeID
			master [33]byte
		}
		identities := make([]identity, len(trusted))
		for i := range trusted {
			identities[i] = identity{node: trusted[i], master: trustedMasterKeys[i]}
		}
		sort.Slice(identities, func(i, j int) bool {
			if bytes.Compare(identities[i].node[:], identities[j].node[:]) != 0 {
				return bytes.Compare(identities[i].node[:], identities[j].node[:]) < 0
			}
			return bytes.Compare(identities[i].master[:], identities[j].master[:]) < 0
		})
		for _, identity := range identities {
			h.Write(identity.node[:])
			h.Write(identity.master[:])
		}
	} else {
		sort.Slice(trusted, func(i, j int) bool { return bytes.Compare(trusted[i][:], trusted[j][:]) < 0 })
		for _, node := range trusted {
			h.Write(node[:])
		}
		binary.BigEndian.PutUint64(number[:], uint64(len(trustedMasterKeys)))
		h.Write(number[:])
		for _, master := range trustedMasterKeys {
			h.Write(master[:])
		}
	}
	binary.BigEndian.PutUint64(number[:], uint64(len(negativeUNL)))
	h.Write(number[:])
	for _, node := range negativeUNL {
		h.Write(node[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

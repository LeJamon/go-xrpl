package consensus

// SignedValidationEvidence is the durable form of a validation used to prove
// a recovery target. Raw is the canonical STValidation blob, including the
// signature, so restart verification does not depend on a parsed in-memory
// validation or optional-field defaults.
type SignedValidationEvidence struct {
	NodeID        NodeID        `json:"node_id"`
	MasterPubKey  [33]byte      `json:"master_pub_key"`
	SigningPubKey SigningPubKey `json:"signing_pub_key"`
	Manifest      []byte        `json:"manifest,omitempty"`
	Raw           []byte        `json:"raw"`
}

// ValidationQuorumProofVersion is the persisted evidence schema version.
const ValidationQuorumProofVersion uint8 = 1

// ValidationQuorumProof binds signed validation evidence to the trust view
// that counted it. The trust hash covers the complete trusted and negative
// UNL sets plus quorum, so evidence captured under another UNL cannot be
// reused after restart or a trust transition.
type ValidationQuorumProof struct {
	Version           uint8                      `json:"version"`
	NetworkID         uint32                     `json:"network_id"`
	LedgerID          LedgerID                   `json:"ledger_id"`
	LedgerSeq         uint32                     `json:"ledger_seq"`
	Quorum            int                        `json:"quorum"`
	Trusted           []NodeID                   `json:"trusted"`
	TrustedMasterKeys [][33]byte                 `json:"trusted_master_keys,omitempty"`
	NegativeUNL       []NodeID                   `json:"negative_unl,omitempty"`
	TrustHash         [32]byte                   `json:"trust_hash"`
	Validations       []SignedValidationEvidence `json:"validations"`
}

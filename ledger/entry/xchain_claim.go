package entry

import (
	"errors"

)

// XChainClaimAttestation represents a single attestation for a cross-chain claim
type XChainClaimAttestation struct {
	AttestationSignerAccount [20]byte // Account of the attestation signer
	PublicKey                [33]byte // Public key of the signer
	Amount                   uint64   // Amount being claimed
	Destination              [20]byte // Destination account
	WasLockingChainSend      bool     // True if sent from locking chain
}

// XChainOwnedClaimID represents a cross-chain claim ID ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltXCHAIN_OWNED_CLAIM_ID
type XChainOwnedClaimID struct {
	BaseEntry

	// Required fields
	Account                 [20]byte                 // Account that owns this claim ID
	XChainBridge            XChainBridge             // Bridge specification
	XChainClaimID           uint64                   // The claim ID
	OtherChainSource        [20]byte                 // Source account on the other chain
	XChainClaimAttestations []XChainClaimAttestation // Attestations for this claim
	SignatureReward         uint64                   // Reward for attestations
	OwnerNode               uint64                   // Directory node hint
}

func (x *XChainOwnedClaimID) Type() Type {
	return TypeXChainOwnedClaimID
}

func (x *XChainOwnedClaimID) Validate() error {
	if x.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if x.OtherChainSource == [20]byte{} {
		return errors.New("other chain source is required")
	}
	return nil
}

func (x *XChainOwnedClaimID) Hash() ([32]byte, error) {
	hash := x.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= x.Account[i]
	}
	return hash, nil
}

// XChainCreateAccountAttestation represents a single attestation for cross-chain account creation
type XChainCreateAccountAttestation struct {
	AttestationSignerAccount [20]byte // Account of the attestation signer
	PublicKey                [33]byte // Public key of the signer
	Amount                   uint64   // Amount being sent
	Destination              [20]byte // Destination account to create
	SignatureReward          uint64   // Reward amount
	WasLockingChainSend      bool     // True if sent from locking chain
}

// XChainOwnedCreateAccountClaimID represents a cross-chain account creation claim ID
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltXCHAIN_OWNED_CREATE_ACCOUNT_CLAIM_ID
type XChainOwnedCreateAccountClaimID struct {
	BaseEntry

	// Required fields
	Account                         [20]byte                         // Account that owns this claim ID
	XChainBridge                    XChainBridge                     // Bridge specification
	XChainAccountCreateCount        uint64                           // Create account transaction count
	XChainCreateAccountAttestations []XChainCreateAccountAttestation // Attestations
	OwnerNode                       uint64                           // Directory node hint
}

func (x *XChainOwnedCreateAccountClaimID) Type() Type {
	return TypeXChainOwnedCreateAccountClaimID
}

func (x *XChainOwnedCreateAccountClaimID) Validate() error {
	if x.Account == [20]byte{} {
		return errors.New("account is required")
	}
	return nil
}

func (x *XChainOwnedCreateAccountClaimID) Hash() ([32]byte, error) {
	hash := x.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= x.Account[i]
	}
	return hash, nil
}

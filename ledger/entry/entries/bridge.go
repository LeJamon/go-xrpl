package entry

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// XChainBridge represents the bridge specification
type XChainBridge struct {
	LockingChainDoor  [20]byte // Account on locking chain
	LockingChainIssue Issue    // Issue on locking chain
	IssuingChainDoor  [20]byte // Account on issuing chain
	IssuingChainIssue Issue    // Issue on issuing chain
}

// Issue represents a currency/issuer pair
type Issue struct {
	Currency [20]byte // Currency code (standard currency or 160-bit code)
	Issuer   [20]byte // Issuer account (empty for XRP)
}

// Bridge represents a cross-chain bridge ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltBRIDGE
type Bridge struct {
	BaseEntry

	// Required fields
	Account                  [20]byte     // Account that owns this bridge
	SignatureReward          uint64       // Reward for providing attestations (in drops)
	XChainBridge             XChainBridge // Bridge specification
	XChainClaimID            uint64       // Next claim ID to be allocated
	XChainAccountCreateCount uint64       // Number of account create transactions
	XChainAccountClaimCount  uint64       // Number of account claim transactions
	OwnerNode                uint64       // Directory node hint

	// Optional fields
	MinAccountCreateAmount *uint64 // Minimum amount to create an account (in drops)
}

func (b *Bridge) Type() entry.Type {
	return entry.TypeBridge
}

func (b *Bridge) Validate() error {
	if b.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if b.SignatureReward == 0 {
		return errors.New("signature reward is required")
	}
	return nil
}

func (b *Bridge) Hash() ([32]byte, error) {
	hash := b.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= b.Account[i]
	}
	return hash, nil
}

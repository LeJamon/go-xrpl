package entry

import (
	"encoding/binary"
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

var (
	// ErrTooManyHashes is returned when LedgerHashes contains more than 256 hashes
	ErrTooManyHashes = errors.New("LedgerHashes entry contains more than 256 hashes")
)

// LedgerHashes represents the skip list / ledger hashes ledger entry.
// This entry stores a list of ledger hashes for efficient historical lookups.
// The "short" skip list stores up to 256 recent ledger hashes.
type LedgerHashes struct {
	BaseEntry

	// Hashes is a list of ledger hashes (up to 256)
	Hashes [][32]byte

	// LastLedgerSequence is the sequence of the most recent ledger in the list
	LastLedgerSequence uint32

	// FirstLedgerSequence is optional, used for long skip lists
	FirstLedgerSequence *uint32
}

// NewLedgerHashes creates a new empty LedgerHashes entry.
func NewLedgerHashes() *LedgerHashes {
	return &LedgerHashes{
		Hashes: make([][32]byte, 0, 256),
	}
}

// Type returns the ledger entry type for LedgerHashes.
func (lh *LedgerHashes) Type() entry.Type {
	return entry.TypeLedgerHashes
}

// Validate checks that the LedgerHashes entry is valid.
func (lh *LedgerHashes) Validate() error {
	// Hashes should not exceed 256 entries
	if len(lh.Hashes) > 256 {
		return ErrTooManyHashes
	}
	return nil
}

// Hash computes the hash for this LedgerHashes entry.
func (lh *LedgerHashes) Hash() ([32]byte, error) {
	hash := lh.BaseEntry.Hash()

	// Include hashes in the computation
	for i, h := range lh.Hashes {
		for j := 0; j < 32 && i*32+j < 32; j++ {
			hash[i*32%32+j] ^= h[j]
		}
	}

	// Include LastLedgerSequence
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], lh.LastLedgerSequence)
	for i := 0; i < 4; i++ {
		hash[i] ^= buf[i]
	}

	return hash, nil
}

// UpdateSkipList updates the skip list with a new parent hash.
// This should be called when creating a new ledger.
// It adds the parent hash to the list and trims to 256 if needed.
func (lh *LedgerHashes) UpdateSkipList(parentHash [32]byte, prevLedgerSeq uint32) {
	// If we have 256 hashes, remove the oldest (first)
	if len(lh.Hashes) >= 256 {
		lh.Hashes = lh.Hashes[1:]
	}

	// Add the new parent hash
	lh.Hashes = append(lh.Hashes, parentHash)

	// Update the last ledger sequence
	lh.LastLedgerSequence = prevLedgerSeq
}

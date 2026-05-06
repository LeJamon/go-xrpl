package adaptor

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// trustedVotesTimeout is how long a validator's amendment vote is
// retained after their last validation. Mirrors rippled's
// expiresAfter = 24h at
// rippled/src/xrpld/app/misc/detail/AmendmentTable.cpp:172.
const trustedVotesTimeout = 24 * time.Hour

// upvotesAndTimeout is the per-validator entry — last-seen vote set
// and the close-time at which it expires. Mirrors UpvotesAndTimeout
// at rippled/src/xrpld/app/misc/detail/AmendmentTable.cpp:98-107.
type upvotesAndTimeout struct {
	upVotes [][32]byte
	// timeout zero-value means "unseated" — either we have never
	// seen a validation from this validator, or its previous vote
	// already expired and was cleared.
	timeout time.Time
}

func (u *upvotesAndTimeout) hasTimeout() bool { return !u.timeout.IsZero() }

// TrustedVotes records the most recent amendment vote from each
// trusted validator and applies a 24h timeout. The cache prevents
// amendment "flapping" — when a flaky validator drops connectivity
// briefly, their last vote is retained for up to 24h so a borderline
// amendment doesn't oscillate between GotMajority and LostMajority
// across consecutive flag ledgers. Mirrors rippled's TrustedVotes
// class at rippled/src/xrpld/app/misc/detail/AmendmentTable.cpp:75-286.
type TrustedVotes struct {
	mu sync.Mutex
	// recordedVotes maps trusted-validator NodeID to its retained
	// vote. Membership is reconciled by TrustChanged; non-trusted
	// validators are never inserted.
	recordedVotes map[consensus.NodeID]*upvotesAndTimeout
}

// NewTrustedVotes constructs an empty TrustedVotes. Call
// TrustChanged with the initial UNL before recording any votes —
// otherwise every validation will be ignored as untrusted.
func NewTrustedVotes() *TrustedVotes {
	return &TrustedVotes{recordedVotes: map[consensus.NodeID]*upvotesAndTimeout{}}
}

// TrustChanged reconciles the recordedVotes set against the current
// trusted-validator list. Existing entries for still-trusted
// validators are preserved verbatim; entries for removed validators
// are dropped; newly-trusted validators get an empty entry with
// unseated timeout. Mirrors trustChanged at
// AmendmentTable.cpp:119-147.
func (t *TrustedVotes) TrustChanged(allTrusted []consensus.NodeID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	newSet := make(map[consensus.NodeID]*upvotesAndTimeout, len(allTrusted))
	for _, id := range allTrusted {
		if existing, ok := t.recordedVotes[id]; ok {
			newSet[id] = existing
		} else {
			newSet[id] = &upvotesAndTimeout{}
		}
	}
	t.recordedVotes = newSet
}

// RecordVotes ingests this round's validations. For each validation
// signed by a trusted validator: timeout is reset to closeTime + 24h
// and upVotes are replaced with the validation's amendments. Then
// any entry whose timeout is past closeTime is cleared (timeout
// unseated, upVotes emptied) so its votes no longer count. Mirrors
// recordVotes at AmendmentTable.cpp:152-261.
func (t *TrustedVotes) RecordVotes(
	closeTime time.Time,
	validations []*consensus.Validation,
) {
	t.mu.Lock()
	defer t.mu.Unlock()

	newTimeout := closeTime.Add(trustedVotesTimeout)
	for _, v := range validations {
		entry, ok := t.recordedVotes[v.NodeID]
		if !ok {
			continue // ignore untrusted-validator validations
		}
		entry.timeout = newTimeout
		if len(v.Amendments) == 0 {
			// Validator emitted no sfAmendments — equivalent to
			// rippled's "validator has no amendment votes" branch
			// at AmendmentTable.cpp:206-211 which clears upVotes.
			entry.upVotes = nil
			continue
		}
		entry.upVotes = append(entry.upVotes[:0], v.Amendments...)
	}
	for _, entry := range t.recordedVotes {
		if !entry.hasTimeout() {
			continue
		}
		if closeTime.After(entry.timeout) {
			entry.timeout = time.Time{}
			entry.upVotes = nil
		}
	}
}

// GetVotes returns (availableValidatorCount, votesPerAmendment).
// availableValidatorCount is the count of entries whose timeout is
// set (i.e., we've seen a recent enough validation).
// votesPerAmendment sums upVotes across all entries — only entries
// with a set timeout contribute, by RecordVotes's invariant
// (cleared entries have empty upVotes). Mirrors getVotes at
// AmendmentTable.cpp:266-285.
func (t *TrustedVotes) GetVotes() (int, map[[32]byte]int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	votes := map[[32]byte]int{}
	available := 0
	for _, entry := range t.recordedVotes {
		if entry.hasTimeout() {
			available++
		}
		for _, h := range entry.upVotes {
			votes[h]++
		}
	}
	return available, votes
}

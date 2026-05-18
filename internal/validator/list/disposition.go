// Package list implements the publisher-trust validator-list subsystem —
// the rippled-faithful counterpart of rippled/src/xrpld/app/misc/
// ValidatorList.cpp + ValidatorSite.cpp.
//
// A "validator list" is a signed bundle of validator master public keys
// distributed by an out-of-band publisher (e.g. vl.ripple.com). Operators
// configure a trusted set of publishers and a threshold; the Aggregator
// ingests lists (via peer gossip or HTTP polling), verifies the
// publisher manifest + blob signature, and recomputes the effective
// trusted UNL as the union of validators present in lists from at least
// `threshold` distinct trusted publishers. The result is pushed into
// Adaptor.SetTrustedValidators so the consensus engine sees the delta.
package list

// Disposition reports the outcome of applying a single validator list
// blob. Mirrors rippled's ListDisposition enum
// (rippled/src/xrpld/app/misc/ValidatorList.h:62-90).
//
// The ordering matters: dispositions strictly greater than Pending mean
// "no state change" (the caller should NOT update publisherLists_),
// while Accepted / Expired / Pending are all "store-and-act" outcomes
// (see ValidatorList.cpp:1169 — `if (result > ListDisposition::pending)
// return early`).
type Disposition uint8

const (
	// Accepted: the blob is the newest valid list from this publisher and
	// has been stored. The caller should relay the originating wire
	// message to other peers (matches rippled applyListsAndBroadcast).
	Accepted Disposition = iota

	// Expired: the blob is structurally and cryptographically valid but
	// its expiration is in the past. The publisher entry is still
	// updated (so the RPC can surface "expired"), but no new validators
	// are admitted to the trusted set from this list.
	Expired

	// Pending: a future-dated list whose `effective` time has not yet
	// arrived. The blob is stored in the publisher's `remaining` queue
	// and applied when its effective time is reached.
	Pending

	// SameSequence: we already have a stored list at this exact
	// sequence. Harmless no-op (peer gossip is lossy and chatty).
	SameSequence

	// KnownSequence: we already have a pending entry at this sequence.
	// Equivalent of rippled's known_sequence — no state change.
	KnownSequence

	// Stale: the blob's sequence is below our current accepted sequence
	// from this publisher. Drop silently (the peer hasn't seen our
	// latest yet, which is fine).
	Stale

	// Untrusted: the publisher master key extracted from the manifest is
	// not in our configured trust set. Per rippled this drops without
	// charging the peer — a peer may gossip lists from publishers we
	// don't trust, and that's not malicious.
	Untrusted

	// Invalid: the manifest's master/ephemeral signature failed
	// verification, or the blob's signature failed against the
	// publisher's current ephemeral key, or the inner JSON is
	// structurally invalid. Charge the sender for bad data.
	Invalid

	// UnsupportedVersion: the protocol version field is outside the
	// supported range (1 or 2). Charge the sender — newer rippled may
	// emit versions we don't grok, but per rippled we still treat this
	// as malformed because the wire format itself was rejected.
	UnsupportedVersion

	// Malformed: the wire message itself could not be parsed (proto
	// decode failed, blob isn't base64, manifest isn't base64, etc.).
	// Charge the sender.
	Malformed
)

// String returns a short, lowercase label suitable for logs and metrics.
func (d Disposition) String() string {
	switch d {
	case Accepted:
		return "accepted"
	case Expired:
		return "expired"
	case Pending:
		return "pending"
	case SameSequence:
		return "same_sequence"
	case KnownSequence:
		return "known_sequence"
	case Stale:
		return "stale"
	case Untrusted:
		return "untrusted"
	case Invalid:
		return "invalid"
	case UnsupportedVersion:
		return "unsupported_version"
	case Malformed:
		return "malformed"
	default:
		return "unknown"
	}
}

// IsBadData reports whether the disposition warrants charging the
// originating peer for malformed / cryptographically-invalid data.
// Used by the router to decide whether to call IncPeerBadData.
func (d Disposition) IsBadData() bool {
	switch d {
	case Invalid, UnsupportedVersion, Malformed:
		return true
	default:
		return false
	}
}

// ShouldRelay reports whether an accepted disposition warrants
// rebroadcasting the originating wire frame to other peers. Mirrors
// rippled's broadcastBlobs branch in applyListsAndBroadcast — only
// strictly-accepted lists propagate; expired/pending/same do not.
func (d Disposition) ShouldRelay() bool {
	return d == Accepted
}

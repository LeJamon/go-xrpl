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
// blob. The numeric ordering mirrors rippled's ListDisposition
// (rippled/src/xrpld/app/misc/ValidatorList.h:55-82): smaller values
// are "more desirable" outcomes.
//
// ORDERING INVARIANT (load-bearing — do not reorder without auditing
// every consumer of Severity() / ShouldRelay() / Charge()):
//
//	Accepted < Expired < Pending < SameSequence < KnownSequence
//	         < Stale < Untrusted < UnsupportedVersion < Invalid
//	         < Malformed  (goXRPL-only)
//
// `ShouldRelay()` is `Severity() <= KnownSequence.Severity()`, mirroring
// rippled's `disposition <= ListDisposition::known_sequence` gate at
// ValidatorList.cpp:973. Re-arranging this iota would silently flip the
// relay set.
//
// Malformed is a goXRPL-only summary disposition emitted exclusively
// by the HTTP site poller for wire-level envelope failures (HTTP
// transport error, JSON body undecodable, required envelope fields
// absent). The aggregator itself never returns Malformed: a corrupt
// publisher manifest folds into Untrusted (rippled
// ValidatorList.cpp:1363-1366) and a corrupt blob envelope folds into
// Invalid (rippled ValidatorList.cpp:1386-1392). Malformed is folded
// back to "invalid" by the RPC adapter so external consumers see only
// rippled-emitted labels.
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
	// from this publisher.
	Stale

	// Untrusted: the publisher master key extracted from the manifest is
	// not in our configured trust set, OR the publisher's manifest is
	// revoked. Rippled returns untrusted in both cases
	// (ValidatorList.cpp:1366,1382-1383) — revocation is legitimate
	// gossip and must not punish the forwarding peer.
	Untrusted

	// UnsupportedVersion: the protocol version field is outside the
	// supported range. Charge the sender.
	UnsupportedVersion

	// Invalid: the blob's signature failed verification, or the inner
	// JSON is structurally invalid. Charge the sender for bad data.
	Invalid

	// Malformed: goXRPL-only. The wire envelope itself could not be
	// parsed (manifest not base64, blob not base64, message decode
	// failure). Charge the sender.
	Malformed
)

// MaxValidRelaySeverity is the upper severity bound (inclusive) for
// dispositions that warrant rebroadcasting the originating frame.
// Mirrors rippled's `disposition <= ListDisposition::known_sequence`
// gate at ValidatorList.cpp:973.
const MaxValidRelaySeverity = KnownSequence

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
	case UnsupportedVersion:
		return "unsupported_version"
	case Invalid:
		return "invalid"
	case Malformed:
		return "malformed"
	default:
		return "unknown"
	}
}

// Severity returns the numeric "worse-is-larger" rank used to pick
// summary dispositions (worst-wins for peer bad-data attribution,
// best-wins for site-poller RPC). The underlying iota ordering is the
// canonical source; this method exists so callers don't open-code
// rank tables that drift from the enum.
func (d Disposition) Severity() int { return int(d) }

// IsBadData reports whether the disposition warrants charging the
// originating peer for malformed / cryptographically-invalid data.
// Mirrors the worst-case charge tiers in PeerImp::onValidatorListMessage
// (PeerImp.cpp:2141-2183). Untrusted / SameSequence / KnownSequence /
// Stale are charged but at lower tiers — see ChargeCategory.
//
// Malformed is poller-only and never reaches a peer-attribution path;
// it is retained here as a defensive "true" so any accidental router
// use surfaces as bad data rather than silently passing.
func (d Disposition) IsBadData() bool {
	switch d {
	case Invalid, UnsupportedVersion, Malformed:
		return true
	default:
		return false
	}
}

// ChargeCategory is the rippled fee tier a disposition maps to in
// PeerImp::onValidatorListMessage. The router translates this into a
// distinct label passed to IncPeerBadData so operators can distinguish
// the misbehavior types in metrics.
type ChargeCategory uint8

const (
	// ChargeNone means the disposition is "good data" — no peer charge.
	ChargeNone ChargeCategory = iota
	// ChargeUselessData mirrors rippled feeUselessData (light): duplicate
	// list, untrusted publisher, same/known sequence.
	ChargeUselessData
	// ChargeInvalidData mirrors rippled feeInvalidData (medium): stale
	// list, unsupported version.
	ChargeInvalidData
	// ChargeInvalidSignature mirrors rippled feeInvalidSignature
	// (heaviest): malformed wire / invalid cryptography.
	ChargeInvalidSignature
)

// Charge returns the rippled fee tier this disposition maps to in
// PeerImp::onValidatorListMessage (PeerImp.cpp:2141-2183). Malformed
// is poller-only (the aggregator never emits it) so it maps to
// ChargeNone — the poller has no peer to charge.
func (d Disposition) Charge() ChargeCategory {
	switch d {
	case Accepted, Expired, Pending, Malformed:
		return ChargeNone
	case SameSequence, KnownSequence, Untrusted:
		return ChargeUselessData
	case Stale, UnsupportedVersion:
		return ChargeInvalidData
	case Invalid:
		return ChargeInvalidSignature
	default:
		return ChargeNone
	}
}

// ShouldRelay reports whether the disposition warrants rebroadcasting
// the originating frame. Mirrors rippled's
// `broadcast = disposition <= ListDisposition::known_sequence` gate
// at ValidatorList.cpp:973 — Accepted, Expired, Pending, SameSequence
// and KnownSequence all relay. Per-peer filtering (skip peers already
// at the same sequence) is done at broadcast time, not here.
func (d Disposition) ShouldRelay() bool {
	return d.Severity() <= MaxValidRelaySeverity.Severity()
}

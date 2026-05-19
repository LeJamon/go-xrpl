package list

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// PublisherKey is the 33-byte master public key of a list publisher
// (including the key-type prefix byte: 0xED for ed25519, 0x02/0x03 for
// secp256k1). Used as the map key for per-publisher state.
type PublisherKey [33]byte

// PublisherStatus tracks per-publisher availability. The label set
// (unavailable / available / expired / revoked) matches rippled's
// PublisherStatus enum at rippled/src/xrpld/app/misc/ValidatorList.h:87-100,
// but the underlying iota values are not aligned with rippled — goXRPL
// never compares PublisherStatus by ordinal so the numeric mapping is
// not load-bearing.
type PublisherStatus uint8

const (
	// StatusUnavailable: configured but no valid list has been ingested
	// yet. The publisher's validators do not contribute to the trusted
	// set.
	StatusUnavailable PublisherStatus = iota

	// StatusAvailable: at least one fresh, signature-verified, non-
	// expired list has been ingested. The publisher's validators
	// contribute to the trusted set on every recompute.
	StatusAvailable

	// StatusExpired: the most recent list passed verification but its
	// expiration is in the past. The publisher no longer contributes
	// new validators, but RPC surfaces the staleness.
	StatusExpired

	// StatusRevoked: the publisher's master key has been revoked by a
	// signed manifest. The entry is retained for RPC visibility but
	// contributes nothing.
	StatusRevoked
)

// String returns a short lowercase label for logs and RPC output.
func (s PublisherStatus) String() string {
	switch s {
	case StatusUnavailable:
		return "unavailable"
	case StatusAvailable:
		return "available"
	case StatusExpired:
		return "expired"
	case StatusRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// PublisherState is the per-publisher state the aggregator maintains.
// Tracks the current accepted list plus a queue of future-dated
// "remaining" lists (rippled's PublisherList.remaining at
// ValidatorList.h:75-83) so a publisher rotation announced ahead of
// time can be applied at the right moment.
//
// Exposed via Snapshot() for RPC and observability — callers receive a
// deep copy and may NOT mutate the aggregator's internal state.
type PublisherState struct {
	MasterKey  PublisherKey
	SigningKey [33]byte
	Status     PublisherStatus

	// Sequence is the strictly-monotonic version of the currently
	// effective list. Zero before the first accepted list.
	Sequence uint32

	// Effective is the Unix timestamp at which the current list became
	// effective. Treated as a sentinel ("not set") when EffectiveSet is
	// false; rippled gates `validFrom != TimeKeeper::time_point{}` at
	// ValidatorList.cpp:1682 and a Go-side zero-value time.Time cannot
	// stand in for the C++ zero sentinel because rippleSecondsToUnix(0)
	// resolves to 2000-01-01 UTC, not Go's epoch.
	Effective time.Time
	// EffectiveSet records whether the accepted blob carried an
	// `effective` field. False means rippled would omit it from the
	// `validators` RPC `effective` key.
	EffectiveSet bool

	// Expiration is the Unix timestamp after which the current list is
	// considered expired and contributes nothing further.
	Expiration time.Time

	// Validators is the 33-byte master pubkey set published by this
	// publisher in the current accepted list, sorted lexicographically
	// for deterministic union computation.
	Validators [][33]byte

	// SiteURI is where this list came from — a publisher URL, or "peer"
	// when ingested from TMValidatorList gossip.
	SiteURI string

	// LastUpdate is when we accepted this publisher's most recent list.
	LastUpdate time.Time

	// Version is the protocol version of the most recently applied
	// list. Mirrors rippled's PublisherListCollection.rawVersion at
	// ValidatorList.h:74. Zero before the first accepted list.
	Version uint32

	// RawManifest / RawBlob / RawSignature are the wire-form bytes of
	// the most recently accepted list, retained so the aggregator can
	// rebroadcast the canonical accepted form to peers (mirrors
	// rippled's PublisherList.rawManifest / rawBlob / rawSignature at
	// ValidatorList.h:184-191). Cleared on revocation. Nil before the
	// first accepted list.
	//
	// `RawManifest` and `RawBlob` are base64-encoded ASCII as received;
	// `RawSignature` is hex-encoded ASCII. The aggregator stores them
	// verbatim — no re-encoding — so what we relay is byte-identical
	// to what an honest peer would have sent us.
	RawManifest  []byte
	RawBlob      []byte
	RawSignature []byte

	// Remaining is the queue of future-dated lists for this publisher,
	// keyed by sequence and ordered by validFrom. Mirrors rippled's
	// PublisherListCollection.remaining at ValidatorList.h:75-83. A
	// rotation announced ahead of `effective` time lands here and is
	// promoted into the current slot once its validFrom passes — see
	// promoteRemainingLocked. Empty when no rotation is pending.
	Remaining map[uint32]*PendingList

	// MaxSequence is the largest sequence ever stored in `Remaining`.
	// Mirrors rippled's PublisherListCollection.maxSequence; combined
	// with `Remaining` it drives the pending-vs-known_sequence decision
	// at applyList (ValidatorList.cpp:1414-1432).
	MaxSequence uint32
	// MaxSequenceSet records whether MaxSequence has ever been
	// populated (a future-dated blob has been observed). Distinct from
	// `MaxSequence == 0` because sequence 0 is not a valid published
	// list — but we keep the sentinel explicit to mirror rippled's
	// std::optional<size_t>.
	MaxSequenceSet bool
}

// PendingList is one entry in PublisherState.Remaining — a fully-verified
// future-dated list that will become current at validFrom. Wire bytes are
// retained so the post-promotion broadcast can re-emit the canonical form.
type PendingList struct {
	Sequence     uint32
	Effective    time.Time
	EffectiveSet bool
	Expiration   time.Time
	Validators   [][33]byte
	SiteURI      string
	Version      uint32
	SigningKey   [33]byte
	RawManifest  []byte
	RawBlob      []byte
	RawSignature []byte
}

// SiteState is the per-URL polling state surfaced via the
// validator_list_sites RPC.
type SiteState struct {
	URI             string
	LastFetched     time.Time
	LastSuccess     time.Time
	LastError       string
	LastDisposition Disposition
	// LastDispositionSet is the sentinel rippled mirrors via
	// `std::optional<Site::Status>::has_value()` at
	// ValidatorSite.cpp:690 — `last_refresh_status` is omitted from the
	// RPC until the first fetch attempt completes. Without the sentinel
	// the zero-value `Disposition` (== Accepted) would emit a false
	// "accepted" status before any poll runs.
	LastDispositionSet bool
	RefreshSeconds     int
	// NextRefresh is the wall-clock time at which the next poll attempt
	// is scheduled. Mirrors rippled's ValidatorSite::Site::nextRefresh
	// surfaced via `next_refresh_time` in the validator_list_sites RPC.
	NextRefresh time.Time
}

// Aggregator is the central publisher-trust subsystem. It owns the
// configured publisher trust set and threshold, tracks per-publisher
// state, exposes a writable surface (ApplyList / ApplyCollection) the
// router and HTTP poller call into, and emits a recomputed trusted
// validator set via OnChange every time the set changes.
//
// Safe for concurrent use; the single mutex covers all maps. Signature
// verification happens outside the lock so concurrent applies don't
// serialize on the (potentially expensive) ed25519/secp256k1 verify.
type Aggregator struct {
	mu sync.Mutex

	// publishers is the configured trust set: every key here is a
	// publisher whose lists we will accept. Populated once at startup
	// from the [validators] config stanza's validator_list_keys field
	// and never mutated thereafter — adding/removing publishers
	// requires a SIGHUP reload.
	publishers map[PublisherKey]struct{}

	// state holds per-publisher state for every publisher whose key is
	// in `publishers`. Pre-populated with empty StatusUnavailable
	// entries so the RPC surface is non-empty from the moment of
	// startup.
	state map[PublisherKey]*PublisherState

	// sites holds per-URL polling state for every URL in
	// validator_list_sites. Updated by the HTTP poller; read by RPC.
	sites []*SiteState

	// manifests is the validator manifest cache. Used to:
	//   1. Apply incoming publisher manifests (verify + cache the
	//      ephemeral signing key for blob verification).
	//   2. Resolve ephemeral validator signing keys back to master keys
	//      when emitting the trusted set for consensus.
	//
	// Shared with the consensus engine and the TMManifests gossip path
	// so the same manifest seen via a VL applies everywhere.
	manifests *manifest.Cache

	// threshold is the minimum number of publishers from `publishers`
	// that must list a validator before that validator is admitted to
	// the effective trusted UNL. Mirrors rippled's listThreshold_
	// (ValidatorList.h:140-141 / ValidatorList.cpp:289).
	threshold int

	// onChange is invoked whenever the recomputed trusted set differs
	// from the previously emitted one. Wired by Components.NewFromConfig
	// to push into Adaptor.SetTrustedValidators.
	//
	// LOCK INVARIANT: the callback runs *under* a.mu. Implementations
	// must not, directly or transitively, call back into the aggregator
	// (any exported method here re-locks a.mu and would deadlock). The
	// production wiring satisfies this by routing the call to
	// Adaptor.SetTrustedValidators, which takes its own lock and does
	// not reach back into the aggregator. Any future caller that
	// reschedules work or fans out to other subscribers must hop off
	// this goroutine before re-entering the aggregator.
	onChange func(validators []consensus.NodeID, masterKeys [][33]byte)

	// lastEmitted is the most recently published trusted set (master
	// keys, sorted). Cached so we suppress no-op OnChange callbacks
	// when a publisher list update doesn't move any validator into or
	// out of the union.
	lastEmitted [][33]byte

	// clock returns the wall-clock time the aggregator uses to gate
	// effective / expiration comparisons. Overridable for tests.
	clock func() time.Time

	logger *slog.Logger

	// bcaster is the overlay/encoder surface BroadcastLatest delivers
	// frames through. Optional — nil disables outgoing relay (suits
	// tests and standalone deployments with no peers).
	bcaster PeerBroadcaster

	// peerSeqMu guards peerSeq. Held briefly during ingress
	// (RecordPeerSequence), disconnect (ForgetPeer), and broadcast
	// (snapshot + post-send update). Distinct from `mu` so a
	// long-running broadcast never blocks publisher-list ingest.
	peerSeqMu sync.Mutex

	// peerSeq[peerID][publisherKey] is the highest list sequence we
	// know the peer has for that publisher. Updated on every accepted
	// ingress (peer told us) and after every send (we told peer).
	// Mirrors rippled's per-PeerImp publisherListSequences_ map
	// (PeerImp.h:183, PeerImp.cpp:2102-2110); kept centrally here so
	// BroadcastLatest can consult it without reaching into peer
	// internals.
	peerSeq map[uint64]map[PublisherKey]uint32

	// cacheDir is the on-disk path where accepted publisher lists are
	// persisted. Set via SetCacheDir; empty disables the cache.
	// Mirrors rippled's ValidatorList::dataPath_ at
	// rippled/src/xrpld/app/misc/ValidatorList.h:155 + the
	// cacheValidatorFile / loadLists pair at
	// rippled/src/xrpld/app/misc/detail/ValidatorList.cpp:368-396 and
	// 1300-1351. Read under a.mu so a SetCacheDir call doesn't race
	// with an in-flight writeCacheLocked.
	cacheDir string
}

// Config carries Aggregator construction parameters. All fields are
// optional; Defaults handle nil Logger / Clock / Manifests so the type
// is usable in narrowly-scoped tests.
type Config struct {
	PublisherKeys []PublisherKey
	SiteURIs      []string
	Threshold     int
	Manifests     *manifest.Cache
	Clock         func() time.Time
	Logger        *slog.Logger
}

// New constructs an Aggregator from the operator-supplied config.
// Returns an error if a publisher key has an unrecognized key-type
// prefix — this is a configuration bug that should fail boot rather
// than silently disable the publisher.
func New(cfg Config) (*Aggregator, error) {
	publishers := make(map[PublisherKey]struct{}, len(cfg.PublisherKeys))
	state := make(map[PublisherKey]*PublisherState, len(cfg.PublisherKeys))
	for _, k := range cfg.PublisherKeys {
		var zero PublisherKey
		if k == zero {
			return nil, errors.New("publisher key is all zero")
		}
		publishers[k] = struct{}{}
		state[k] = &PublisherState{
			MasterKey: k,
			Status:    StatusUnavailable,
		}
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	// Seed NextRefresh at construction so the validator_list_sites RPC
	// surfaces a real value before the first poll fires. Mirrors
	// rippled ValidatorSite.cpp:83 (`nextRefresh = clock_type::now() +
	// refreshInterval`).
	initialNextRefresh := clock().Add(DefaultRefreshInterval)
	// Seed RefreshSeconds with the default refresh interval so the
	// `refresh_interval_min` RPC field reports the configured cadence
	// from boot, before any envelope-supplied override is observed.
	// Mirrors rippled ValidatorSite.cpp:81 where Site::refreshInterval
	// is initialised to default_refresh_interval (5 minutes) at
	// construction and emitted unconditionally in getJson.
	defaultRefreshSec := int(DefaultRefreshInterval / time.Second)
	sites := make([]*SiteState, 0, len(cfg.SiteURIs))
	for _, u := range cfg.SiteURIs {
		sites = append(sites, &SiteState{
			URI:            u,
			NextRefresh:    initialNextRefresh,
			RefreshSeconds: defaultRefreshSec,
		})
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "validator-list-aggregator")
	}
	threshold := cfg.Threshold
	if threshold <= 0 && len(publishers) > 0 {
		// Mirror rippled's default: ceil(N/2 + 1) for N >= 3, else 1.
		// Matches config.ValidatorsConfig.GetValidatorListThreshold().
		if len(publishers) < 3 {
			threshold = 1
		} else {
			threshold = (len(publishers) / 2) + 1
		}
	}
	if threshold > len(publishers) {
		return nil, fmt.Errorf("threshold %d exceeds publisher count %d", threshold, len(publishers))
	}
	return &Aggregator{
		publishers: publishers,
		state:      state,
		sites:      sites,
		manifests:  cfg.Manifests,
		threshold:  threshold,
		clock:      clock,
		logger:     logger,
		peerSeq:    make(map[uint64]map[PublisherKey]uint32),
	}, nil
}

// SetBroadcaster wires the overlay/encoder surface BroadcastLatest
// uses to deliver frames. Pass nil to disable relay (the default).
// Safe to call multiple times; not safe to race with BroadcastLatest
// — wire once at startup.
func (a *Aggregator) SetBroadcaster(b PeerBroadcaster) {
	a.mu.Lock()
	a.bcaster = b
	a.mu.Unlock()
}

// OnChange registers (or replaces) the callback fired when the
// recomputed trusted UNL differs from the previously emitted one.
// Passing nil clears the callback. Safe to call before or after Start.
func (a *Aggregator) OnChange(cb func(validators []consensus.NodeID, masterKeys [][33]byte)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onChange = cb
}

// PublisherCount returns the number of configured publishers in the
// trust set — a constant for the lifetime of the aggregator.
func (a *Aggregator) PublisherCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.publishers)
}

// Threshold returns the configured publisher threshold.
func (a *Aggregator) Threshold() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threshold
}

// HasConfiguredPublishers reports whether any publishers were
// configured at startup. False means the publisher-trust subsystem is
// inert — the trusted UNL comes entirely from the static [validators]
// stanza or SIGHUP reload.
func (a *Aggregator) HasConfiguredPublishers() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.publishers) > 0
}

// PublisherSnapshot returns a deep copy of the per-publisher state for
// RPC and observability. Order is sorted by publisher master key for
// stable output. Safe to call concurrently with ingest.
//
// Lazily runs the Remaining → current promotion so the RPC sees a
// timely view even when no new lists have been ingested in this
// process tick. The promotion is purely a state shift inside the
// publisher entry; it does not emit OnChange (Tick is the explicit
// emit-on-time entry point).
func (a *Aggregator) PublisherSnapshot() []PublisherState {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock()
	for _, s := range a.state {
		a.promoteRemainingLocked(s, now)
	}
	out := make([]PublisherState, 0, len(a.state))
	for _, s := range a.state {
		cp := *s
		if len(s.Validators) > 0 {
			cp.Validators = make([][33]byte, len(s.Validators))
			copy(cp.Validators, s.Validators)
		}
		if len(s.RawManifest) > 0 {
			cp.RawManifest = append([]byte(nil), s.RawManifest...)
		}
		if len(s.RawBlob) > 0 {
			cp.RawBlob = append([]byte(nil), s.RawBlob...)
		}
		if len(s.RawSignature) > 0 {
			cp.RawSignature = append([]byte(nil), s.RawSignature...)
		}
		if len(s.Remaining) > 0 {
			cp.Remaining = make(map[uint32]*PendingList, len(s.Remaining))
			for seq, p := range s.Remaining {
				pcopy := *p
				if len(p.Validators) > 0 {
					pcopy.Validators = append([][33]byte(nil), p.Validators...)
				}
				if len(p.RawManifest) > 0 {
					pcopy.RawManifest = append([]byte(nil), p.RawManifest...)
				}
				if len(p.RawBlob) > 0 {
					pcopy.RawBlob = append([]byte(nil), p.RawBlob...)
				}
				if len(p.RawSignature) > 0 {
					pcopy.RawSignature = append([]byte(nil), p.RawSignature...)
				}
				cp.Remaining[seq] = &pcopy
			}
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].MasterKey[:]) < string(out[j].MasterKey[:])
	})
	return out
}

// SiteSnapshot returns a deep copy of the per-URL polling state for the
// validator_list_sites RPC.
func (a *Aggregator) SiteSnapshot() []SiteState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]SiteState, len(a.sites))
	for i, s := range a.sites {
		out[i] = *s
	}
	return out
}

// SetNextRefresh schedules the next poll time for the given URL without
// touching other site-state fields. Called by the poller at the start
// of each fetch attempt so the validator_list_sites RPC reports the
// upcoming refresh time even while the in-flight fetch is outstanding.
// Mirrors rippled's onTimer ordering at ValidatorSite.cpp:354-355 where
// `nextRefresh` is updated before `makeRequest` is invoked.
//
// Idempotent for unknown URIs.
func (a *Aggregator) SetNextRefresh(uri string, nextRefresh time.Time) {
	if nextRefresh.IsZero() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sites {
		if s.URI == uri {
			s.NextRefresh = nextRefresh
			return
		}
	}
}

// UpdateSiteState records the outcome of an HTTP poll attempt against a
// configured publisher URL. The poller goroutine calls this after each
// fetch attempt; the data flows through to the validator_list_sites
// RPC.
//
// Idempotent for unknown URIs — the call is silently dropped rather
// than erroring, so a poller cannot panic the server by being out of
// sync with the configured site set.
func (a *Aggregator) UpdateSiteState(uri string, lastFetched, lastSuccess time.Time, lastErr string, lastDisp Disposition, refreshSec int, nextRefresh time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sites {
		if s.URI != uri {
			continue
		}
		s.LastFetched = lastFetched
		if !lastSuccess.IsZero() {
			s.LastSuccess = lastSuccess
		}
		s.LastError = lastErr
		s.LastDisposition = lastDisp
		s.LastDispositionSet = true
		if refreshSec > 0 {
			s.RefreshSeconds = refreshSec
		}
		if !nextRefresh.IsZero() {
			s.NextRefresh = nextRefresh
		}
		return
	}
}

// ApplyList ingests a single (manifest, blob, signature) triple and
// returns the resulting disposition along with the publisher master
// key (when extractable) and the blob's sequence (when extractable).
// Wraps the rippled-faithful applyList algorithm: verify the publisher
// manifest, look up its current ephemeral signing key, verify the blob
// signature, JSON-parse the blob, then update per-publisher state and
// trigger an OnChange if the trusted union changed.
//
// `manifestBytes` and `blob` carry the WIRE-FORM ascii strings as
// received in TMValidatorList / TMValidatorListCollection (base64-
// encoded). `signature` carries the WIRE-FORM hex string. `version`
// is the protocol version negotiated at the message level.
//
// `siteURI` is recorded on the per-publisher state for RPC visibility
// — pass "peer:<id>" for gossip-sourced lists and the URL for
// HTTP-polled ones.
//
// Returns the publisher master key on every disposition where it's
// extractable (i.e. the manifest decoded), so the caller can attribute
// metrics / bad-data charges with publisher-level granularity even on
// failure paths. Returns the zero PublisherKey when the manifest
// itself could not be parsed. The sequence return is non-zero only
// when the blob was decoded; the router uses it to record per-peer
// publisher sequences regardless of accept/expired/pending outcome.
func (a *Aggregator) ApplyList(manifestBytes, blob, signature []byte, version uint32, siteURI string) (Disposition, PublisherKey, uint32) {
	if !isSupportedVersion(version) {
		return UnsupportedVersion, PublisherKey{}, 0
	}

	// Decode the publisher manifest. The manifest is base64-encoded on
	// the wire; the inner STObject is what manifest.Deserialize wants.
	// Both failure modes mirror rippled ValidatorList.cpp:1363-1366
	// which folds bad-manifest into Untrusted (no extractable master
	// key, no usable trust decision) charged at feeUselessData — never
	// at the heavier feeInvalidSignature reserved for bad cryptography
	// over a structurally-sound list.
	manifestRaw, err := decodeBase64Tolerant(manifestBytes)
	if err != nil {
		a.logger.Debug("validator list: manifest base64 decode failed", "error", err, "site", siteURI)
		return Untrusted, PublisherKey{}, 0
	}
	parsed, err := manifest.Deserialize(manifestRaw)
	if err != nil {
		a.logger.Debug("validator list: manifest deserialize failed", "error", err, "site", siteURI)
		return Untrusted, PublisherKey{}, 0
	}
	pubKey := PublisherKey(parsed.MasterKey)

	// Reject lists from publishers we don't trust. Per rippled this is
	// a silent drop — gossip carries lists from many publishers and we
	// shouldn't penalize peers for forwarding lists we choose not to
	// trust ourselves.
	a.mu.Lock()
	_, trusted := a.publishers[pubKey]
	a.mu.Unlock()
	if !trusted {
		return Untrusted, pubKey, 0
	}

	// Apply the publisher manifest to the manifest cache. This both
	// caches the manifest for later use and gives us the current
	// ephemeral signing key. A revoked manifest invalidates the
	// publisher entirely.
	//
	// Track the cache disposition so revocation only flips publisher
	// state when the manifest cache actually accepted the revocation.
	// Mirrors rippled ValidatorList.cpp:1373-1378 — `removePublisherList`
	// runs only under `revoked && result == ManifestDisposition::accepted`;
	// a stale revocation (cache already holds a higher-sequence
	// non-revoked manifest) returns untrusted without clearing state.
	manifestAccepted := false
	if a.manifests != nil {
		switch d := a.manifests.ApplyManifest(parsed); d {
		case manifest.Accepted:
			manifestAccepted = true
		case manifest.Stale:
			// Already had this or a newer one — cache state is
			// unchanged; don't let an old revocation manifest flip
			// the publisher's status.
		case manifest.Invalid:
			// Rippled ValidatorList.cpp:1382-1383 returns
			// `untrusted` strictly for `result == ManifestDisposition::invalid`.
			// Untrusted maps to feeUselessData (light), Invalid would map
			// to feeInvalidSignature (heavy) — using Untrusted avoids
			// overcharging honest peers that forward a list whose manifest
			// the cache cannot accept.
			return Untrusted, pubKey, 0
		case manifest.BadMasterKey, manifest.BadEphemeralKey:
			// Cache state is unchanged for these (Manifest.cpp:436-477
			// returns before any mutation), so `getSigningKey(masterPubKey)`
			// below will still return the previously-cached signing key if
			// any. Rippled's check at ValidatorList.cpp:1380-1383 gates only
			// on `result == invalid`, NOT on badMasterKey/badEphemeralKey,
			// so we fall through to the signing-key lookup. If the cache
			// has no key for this master, the lookup branch a few lines
			// below returns Untrusted; if it has one, blob verification
			// proceeds against that cached key — matching rippled.
		}
	} else {
		// Fall back to direct verification when no cache is wired
		// (tests). The signing key in the manifest is what we'd have
		// pulled from the cache. Mirrors rippled's invalid-manifest →
		// untrusted mapping at ValidatorList.cpp:1382-1383.
		if err := parsed.Verify(); err != nil {
			return Untrusted, pubKey, 0
		}
		// No cache means every fresh verify is "accepted" for the
		// purpose of the revocation gate.
		manifestAccepted = true
	}

	if parsed.Revoked() {
		// Rippled returns ListDisposition::untrusted on revocation
		// (ValidatorList.cpp:1382-1383). Revocations are legitimate
		// gossip; punishing the forwarding peer would cascade across
		// every honest hop in the mesh. The state-clearing side effect
		// only runs when the manifest cache actually accepted the
		// revocation — mirrors rippled's `revoked && result == accepted`
		// gate at ValidatorList.cpp:1373.
		if manifestAccepted {
			a.handleRevocation(pubKey)
		}
		return Untrusted, pubKey, 0
	}

	// Pull the current ephemeral signing key. With a cache: this is
	// the freshest signing key we've ever seen for the publisher,
	// which might be NEWER than the one in this very manifest if a
	// later manifest arrived first via gossip. Rippled also uses
	// publisherManifests_.getSigningKey here, which is the latest
	// cached key, not the one in `manifestBytes`.
	signingKey := parsed.SigningKey
	if a.manifests != nil {
		if k, ok := a.manifests.GetSigningKey(parsed.MasterKey); ok {
			signingKey = k
		} else {
			// Cache says the master is unknown or revoked. If revoked
			// we already handled above; if unknown despite the apply,
			// treat as untrusted (no usable signing key from a trusted
			// publisher means we cannot verify the blob; rippled's
			// equivalent at ValidatorList.cpp:1382 is also untrusted).
			return Untrusted, pubKey, 0
		}
	}

	if err := verifyBlobSignature(signingKey, blob, signature); err != nil {
		a.logger.Debug("validator list: blob signature invalid", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return Invalid, pubKey, 0
	}

	parsedBlob, disp, err := parseBlob(blob)
	if err != nil {
		a.logger.Debug("validator list: blob parse failed", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return disp, pubKey, 0
	}

	now := a.clock()
	validFrom := time.Unix(rippleSecondsToUnix(parsedBlob.Effective), 0).UTC()
	validUntil := time.Unix(rippleSecondsToUnix(parsedBlob.Expiration), 0).UTC()

	a.mu.Lock()
	defer a.mu.Unlock()

	// New() pre-populates state[pubKey] for every trusted publisher and
	// the entry is never deleted, so a missing key would be an internal
	// invariant break — surface it loudly rather than silently re-create.
	current := a.state[pubKey]

	// Promote any pending Remaining entries that have reached their
	// validFrom before the disposition check — mirrors rippled's lazy
	// rotation in updateTrusted at ValidatorList.cpp:1929-1991, except
	// driven by ingest rather than ledger close. Without this a fresh
	// blob arriving after a queued rotation's effective time would see
	// a stale `current.sequence` and decide pending/known_sequence on
	// the wrong baseline.
	a.promoteRemainingLocked(current, now)

	// Determine disposition by sequence + time ordering. Mirrors the
	// rippled state machine at ValidatorList.cpp:1394-1437. The
	// SameSequence branch is intentionally unguarded by status —
	// rippled returns same_sequence for every repeat of the current
	// sequence regardless of `pubCollection.status`.
	if parsedBlob.Sequence < current.Sequence {
		return Stale, pubKey, parsedBlob.Sequence
	}
	if parsedBlob.Sequence == current.Sequence {
		return SameSequence, pubKey, parsedBlob.Sequence
	}
	if validUntil.Before(now) || validUntil.Equal(now) {
		// Even an expired list still updates the publisher entry so
		// the RPC can surface "expired" — but the validators do NOT
		// flow into the trusted union (status is StatusExpired).
		applied := a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version, manifestBytes, blob, signature)
		_ = applied // applyAcceptedLocked sets Status; trusted set recompute below skips expired.
		current.Status = StatusExpired
		// Mirrors rippled removePublisherList(StatusExpired) at
		// ValidatorList.cpp:1529-1542 — on expiry the publisher's
		// validator list is cleared. Without this the `validators`
		// RPC would show stale keys under `available=false`.
		current.Validators = nil
		a.recomputeAndEmitLocked()
		return Expired, pubKey, parsedBlob.Sequence
	}
	if validFrom.After(now) {
		// Future-dated. Mirrors rippled ValidatorList.cpp:1414-1432
		// pending-vs-known_sequence: a list is "pending" the first
		// time it lands or whenever its sequence is the largest seen,
		// and "known_sequence" only for re-arrivals at an already-
		// queued sequence. The queued blob is retained so the next
		// promoteRemainingLocked pass can rotate it into `current`.
		return a.applyPendingLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, version, manifestBytes, blob, signature), pubKey, parsedBlob.Sequence
	}

	a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version, manifestBytes, blob, signature)
	a.recomputeAndEmitLocked()
	return Accepted, pubKey, parsedBlob.Sequence
}

// applyAcceptedLocked materializes the parsed blob into the
// publisher's state. Caller must hold a.mu. Does NOT emit OnChange —
// that's done by recomputeAndEmitLocked once the caller has decided
// the disposition warrants a trusted-set recompute.
//
// rawManifest / rawBlob / rawSignature are the wire-form bytes from
// the original TMValidatorList / envelope; they are copied (not
// referenced) so the caller may reuse its slices safely. Used later
// by BroadcastLatest to re-emit the canonical accepted form to peers.
func (a *Aggregator) applyAcceptedLocked(s *PublisherState, blob *blobJSON, signingKey [33]byte, validFrom, validUntil time.Time, siteURI string, now time.Time, version uint32, rawManifest, rawBlob, rawSignature []byte) bool {
	prevCount := len(s.Validators)
	s.Sequence = blob.Sequence
	s.Effective = validFrom
	s.EffectiveSet = blob.EffectiveSet
	s.Expiration = validUntil
	s.SigningKey = signingKey
	s.SiteURI = siteURI
	s.LastUpdate = now
	s.Status = StatusAvailable
	if version > s.Version {
		s.Version = version
	}
	s.RawManifest = append(s.RawManifest[:0], rawManifest...)
	s.RawBlob = append(s.RawBlob[:0], rawBlob...)
	s.RawSignature = append(s.RawSignature[:0], rawSignature...)

	keys := make([][33]byte, 0, len(blob.Validators))
	for i, v := range blob.Validators {
		raw, err := hex.DecodeString(v.ValidationPublicKey)
		if err != nil || !validatorKeyValid(raw) {
			// Mirrors rippled ValidatorList.cpp:1250-1273 which logs
			// `Invalid node identity` and silently skips the entry
			// rather than rejecting the surrounding blob.
			a.logger.Debug("validator list: skipping invalid validator entry",
				"index", i,
				"pubkey", v.ValidationPublicKey)
			continue
		}
		var k [33]byte
		copy(k[:], raw)
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i][:]) < string(keys[j][:])
	})
	s.Validators = keys

	// Also seed any embedded validator manifests into the manifest
	// cache so consensus can resolve ephemeral signing keys
	// immediately, without waiting for the validator to gossip its
	// own manifest. Mirrors rippled ValidatorList.cpp:1117-1133 which
	// gates the apply on `keyListings_.count(m->masterKey)` — the
	// embedded manifest's master key must be listed by some publisher
	// (or by this publisher's new list, which is already incorporated
	// above via `keys`). Manifests for unlisted validators are dropped:
	// a malicious publisher must not be able to pollute the cache with
	// manifests for validators they don't actually attest to.
	if a.manifests != nil {
		listed := make(map[[33]byte]struct{}, len(keys)+8)
		for _, k := range keys {
			listed[k] = struct{}{}
		}
		for pubMaster, ps := range a.state {
			if pubMaster == s.MasterKey {
				continue
			}
			for _, k := range ps.Validators {
				listed[k] = struct{}{}
			}
		}
		for _, v := range blob.Validators {
			if v.Manifest == "" {
				continue
			}
			raw, err := decodeBase64Tolerant([]byte(v.Manifest))
			if err != nil {
				continue
			}
			parsed, err := manifest.Deserialize(raw)
			if err != nil {
				continue
			}
			if _, ok := listed[parsed.MasterKey]; !ok {
				a.logger.Debug("validator list: dropping embedded manifest for unlisted master",
					"master", hex.EncodeToString(parsed.MasterKey[:]))
				continue
			}
			_ = a.manifests.ApplyManifest(parsed)
		}
	}

	a.writeCacheLocked(s)
	return prevCount != len(keys)
}

// applyPendingLocked stores a future-dated blob in the publisher's
// Remaining queue and returns Pending vs KnownSequence per rippled
// ValidatorList.cpp:1414-1432:
//
//   - Pending: no MaxSequence yet, or sequence > MaxSequence, or
//     sequence is unknown AND validFrom precedes the current
//     MaxSequence entry's validFrom (out-of-order delivery).
//   - KnownSequence: re-arrival at a sequence already queued.
//
// The blob is only stored on the Pending branch; KnownSequence is a
// no-op since we already have the same sequence queued. Caller must
// hold a.mu. Does NOT emit OnChange — promotion drives the trusted-set
// update.
func (a *Aggregator) applyPendingLocked(s *PublisherState, blob *blobJSON, signingKey [33]byte, validFrom, validUntil time.Time, siteURI string, version uint32, rawManifest, rawBlob, rawSignature []byte) Disposition {
	known := false
	if s.MaxSequenceSet {
		if _, hit := s.Remaining[blob.Sequence]; hit {
			known = true
		} else if blob.Sequence <= s.MaxSequence {
			// New sequence below max — pending only if it lands ahead
			// of the current max-stored validFrom (out-of-order).
			if maxEntry, ok := s.Remaining[s.MaxSequence]; !ok || !validFrom.Before(maxEntry.Effective) {
				known = true
			}
		}
	}
	if known {
		return KnownSequence
	}

	keys := make([][33]byte, 0, len(blob.Validators))
	for i, v := range blob.Validators {
		raw, err := hex.DecodeString(v.ValidationPublicKey)
		if err != nil || !validatorKeyValid(raw) {
			a.logger.Debug("validator list: skipping invalid validator entry in pending blob",
				"index", i,
				"pubkey", v.ValidationPublicKey)
			continue
		}
		var k [33]byte
		copy(k[:], raw)
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i][:]) < string(keys[j][:])
	})

	if s.Remaining == nil {
		s.Remaining = make(map[uint32]*PendingList, 2)
	}
	s.Remaining[blob.Sequence] = &PendingList{
		Sequence:     blob.Sequence,
		Effective:    validFrom,
		EffectiveSet: blob.EffectiveSet,
		Expiration:   validUntil,
		Validators:   keys,
		SiteURI:      siteURI,
		Version:      version,
		SigningKey:   signingKey,
		RawManifest:  append([]byte(nil), rawManifest...),
		RawBlob:      append([]byte(nil), rawBlob...),
		RawSignature: append([]byte(nil), rawSignature...),
	}
	a.writeCacheLocked(s)
	if !s.MaxSequenceSet || blob.Sequence > s.MaxSequence {
		s.MaxSequence = blob.Sequence
		s.MaxSequenceSet = true
	}
	return Pending
}

// promoteRemainingLocked rotates ready Remaining entries (those whose
// validFrom <= now) into the publisher's current slot, mirroring
// rippled's updateTrusted loop at ValidatorList.cpp:1929-1991.
// Operates on a single publisher; caller drives publisher iteration if
// promoting for the whole set.
//
// Walks Remaining in ascending sequence order so a chain of stacked
// rotations resolves to the LAST ready entry (rippled's iter/next
// scan). Earlier entries are skipped and discarded — rippled
// likewise erases [firstIter, std::next(iter)] after the rotation.
//
// Caller must hold a.mu. Does not emit OnChange — caller decides when
// to recompute (typically immediately after the call when invoked at
// ingest, or via Tick → recomputeAndEmitLocked on the time-driven
// path).
func (a *Aggregator) promoteRemainingLocked(s *PublisherState, now time.Time) (promoted bool) {
	if len(s.Remaining) == 0 {
		return false
	}
	seqs := make([]uint32, 0, len(s.Remaining))
	for seq := range s.Remaining {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	// Find the LAST entry that is ready to promote.
	pickIdx := -1
	for i, seq := range seqs {
		p := s.Remaining[seq]
		if !p.Effective.After(now) {
			pickIdx = i
			continue
		}
		break
	}
	if pickIdx < 0 {
		return false
	}

	chosen := s.Remaining[seqs[pickIdx]]
	s.Sequence = chosen.Sequence
	s.Effective = chosen.Effective
	s.EffectiveSet = chosen.EffectiveSet
	s.Expiration = chosen.Expiration
	s.SigningKey = chosen.SigningKey
	s.SiteURI = chosen.SiteURI
	if chosen.Version > s.Version {
		s.Version = chosen.Version
	}
	s.RawManifest = append(s.RawManifest[:0], chosen.RawManifest...)
	s.RawBlob = append(s.RawBlob[:0], chosen.RawBlob...)
	s.RawSignature = append(s.RawSignature[:0], chosen.RawSignature...)
	s.Validators = append([][33]byte(nil), chosen.Validators...)
	s.Status = StatusAvailable
	// Mirrors rippled ValidatorList.cpp:1970 — if the promoted list is
	// itself already expired, clear the validators so the next
	// expiration sweep can downgrade the publisher cleanly.
	if !s.Expiration.IsZero() && !s.Expiration.After(now) {
		s.Status = StatusExpired
		s.Validators = nil
	}

	// Erase all entries up to and including the chosen one — rippled
	// remaining.erase(firstIter, std::next(iter)).
	for i := 0; i <= pickIdx; i++ {
		delete(s.Remaining, seqs[i])
	}
	if len(s.Remaining) == 0 {
		s.Remaining = nil
	}
	a.writeCacheLocked(s)
	return true
}

// Tick performs a time-driven promotion sweep across every publisher
// and emits OnChange if the resulting trusted union changed. Callers
// should invoke this periodically (rippled drives it from updateTrusted
// at every ledger close, ValidatorList.cpp:1910-1928); without an
// external tick a pending rotation announced during a quiet period
// would only promote on the next ApplyList / TrustedValidators call.
//
// Safe to call from any goroutine. Briefly takes the aggregator lock.
func (a *Aggregator) Tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock()
	for _, s := range a.state {
		a.promoteRemainingLocked(s, now)
	}
	a.recomputeAndEmitLocked()
}

// handleRevocation removes a publisher's contribution when its master
// key is revoked by a fresh manifest. Mirrors rippled's
// removePublisherList(StatusRevoked) branch in verify(). Also clears
// the retained wire bytes so a revoked publisher is never rebroadcast.
func (a *Aggregator) handleRevocation(pubKey PublisherKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.state[pubKey]
	if !ok {
		return
	}
	s.Status = StatusRevoked
	a.removeCacheLocked(s.MasterKey)
	s.Validators = nil
	s.RawManifest = nil
	s.RawBlob = nil
	s.RawSignature = nil
	s.Remaining = nil
	s.MaxSequence = 0
	s.MaxSequenceSet = false
	a.recomputeAndEmitLocked()
}

// recomputeAndEmitLocked walks the per-publisher state, computes the
// union of validators present in at least `threshold` publishers' lists,
// and — if the result differs from the last emitted set — invokes the
// OnChange callback with sorted NodeID and master-key slices ready for
// Adaptor.SetTrustedValidators.
//
// Caller MUST hold a.mu. The OnChange callback runs under the lock; the
// adaptor.SetTrustedValidators path takes a different mutex so the
// nesting is safe.
func (a *Aggregator) recomputeAndEmitLocked() {
	if a.threshold <= 0 || len(a.publishers) == 0 {
		return
	}

	now := a.clock()
	// Promote any Remaining entries whose validFrom has passed before
	// counting — without this a rotation queued at applyPendingLocked
	// would only feed the trusted set on the next ApplyList /
	// TrustedValidators call, even if the caller (Tick or ingest path)
	// invoked us specifically to refresh.
	for _, s := range a.state {
		a.promoteRemainingLocked(s, now)
	}
	counts := make(map[[33]byte]int, 64)
	for _, s := range a.state {
		if s.Status != StatusAvailable {
			continue
		}
		if !s.Expiration.IsZero() && !s.Expiration.After(now) {
			continue
		}
		if !s.Effective.IsZero() && s.Effective.After(now) {
			continue
		}
		// Use a set per publisher so duplicate entries in one
		// publisher's list don't double-count toward the threshold.
		seen := make(map[[33]byte]struct{}, len(s.Validators))
		for _, k := range s.Validators {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			counts[k]++
		}
	}

	trusted := make([][33]byte, 0, len(counts))
	for k, c := range counts {
		if c >= a.threshold {
			trusted = append(trusted, k)
		}
	}
	sort.Slice(trusted, func(i, j int) bool {
		return string(trusted[i][:]) < string(trusted[j][:])
	})

	if mastersEqual(trusted, a.lastEmitted) {
		return
	}
	a.lastEmitted = trusted

	if a.onChange == nil {
		return
	}

	nodeIDs := make([]consensus.NodeID, len(trusted))
	for i, k := range trusted {
		nodeIDs[i] = consensus.CalcNodeID(k)
	}
	a.logger.Info("validator-list publisher trust recomputed",
		"trusted_count", len(trusted),
		"publisher_count", len(a.publishers),
		"threshold", a.threshold)
	a.onChange(nodeIDs, trusted)
}

// TrustedValidators returns the current effective trusted set as
// NodeIDs + master keys, both sorted by master key for determinism.
// Recomputes on every call from the current per-publisher state.
// Mirrors rippled's ValidatorList::getQuorumKeys() shape.
func (a *Aggregator) TrustedValidators() ([]consensus.NodeID, [][33]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.threshold <= 0 || len(a.publishers) == 0 {
		return nil, nil
	}

	now := a.clock()
	// Lazy-promote ready Remaining entries so the trusted set reflects
	// rotations that became effective since the last ingest. Tick()
	// handles the OnChange emission on the time-driven path; here we
	// just need a fresh snapshot.
	for _, s := range a.state {
		a.promoteRemainingLocked(s, now)
	}
	counts := make(map[[33]byte]int, 64)
	for _, s := range a.state {
		if s.Status != StatusAvailable {
			continue
		}
		if !s.Expiration.IsZero() && !s.Expiration.After(now) {
			continue
		}
		if !s.Effective.IsZero() && s.Effective.After(now) {
			continue
		}
		seen := make(map[[33]byte]struct{}, len(s.Validators))
		for _, k := range s.Validators {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			counts[k]++
		}
	}

	masters := make([][33]byte, 0, len(counts))
	for k, c := range counts {
		if c >= a.threshold {
			masters = append(masters, k)
		}
	}
	sort.Slice(masters, func(i, j int) bool {
		return string(masters[i][:]) < string(masters[j][:])
	})
	nodeIDs := make([]consensus.NodeID, len(masters))
	for i, k := range masters {
		nodeIDs[i] = consensus.CalcNodeID(k)
	}
	return nodeIDs, masters
}

// ApplyCollection processes a v2 collection (TMValidatorListCollection),
// applying each blob individually with the collection's shared
// publisher manifest. Returns the per-blob dispositions in the same
// order as the collection's blob array, plus the publisher key once
// the manifest decoded and the highest sequence observed across the
// collection. The router uses the dispositions to decide whether to
// charge the sender (any Invalid / Malformed) and whether to relay
// (at least one Accepted), and uses the max-sequence value to update
// the per-peer publisherListSequences entry.
//
// Mirrors rippled's applyLists at ValidatorList.cpp:998-1070.
func (a *Aggregator) ApplyCollection(coll *message.ValidatorListCollection, siteURI string) ([]Disposition, PublisherKey, uint32) {
	if coll == nil {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	if !isSupportedVersion(coll.Version) {
		return []Disposition{UnsupportedVersion}, PublisherKey{}, 0
	}
	if len(coll.Blobs) == 0 {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	// Anti-abuse cap. Matches rippled ValidatorList.h:272
	// `static constexpr std::size_t maxSupportedBlobs = 5;` enforced
	// at ValidatorList.cpp:428 (v2 JSON path) and 472-473 (parseBlobs).
	// A peer that sends a collection larger than this would force the
	// aggregator to run N signature verifications; reject before any
	// crypto work.
	if len(coll.Blobs) > MaxSupportedBlobs {
		return []Disposition{Malformed}, PublisherKey{}, 0
	}
	out := make([]Disposition, len(coll.Blobs))
	var pubKey PublisherKey
	var maxSeq uint32
	for i, blob := range coll.Blobs {
		// Per blob: prefer the embedded local manifest when present,
		// else fall back to the collection's shared manifest. Matches
		// rippled applyList(globalManifest, localManifest, ...) at
		// ValidatorList.cpp:1140-1151.
		mf := blob.Manifest
		if len(mf) == 0 {
			mf = coll.Manifest
		}
		disp, pk, seq := a.ApplyList(mf, blob.Blob, blob.Signature, coll.Version, siteURI)
		out[i] = disp
		if pk != (PublisherKey{}) {
			pubKey = pk
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return out, pubKey, maxSeq
}

// isSupportedVersion reports whether version is in SupportedVersions.
func isSupportedVersion(v uint32) bool {
	for _, sv := range SupportedVersions {
		if sv == v {
			return true
		}
	}
	return false
}

// RecordPeerSequence remembers that `peerID` has at least sequence
// `seq` for `pubKey`. Called by the router after a successful ingress
// of a TMValidatorList / TMValidatorListCollection — the peer has
// proved possession of that sequence, so subsequent BroadcastLatest
// passes for the same or older sequence can skip them.
//
// Monotonic: lower sequences are ignored. Zero `seq` is a no-op (we
// only ever record a confirmed sequence). Mirrors rippled
// PeerImp.cpp:2102-2110.
func (a *Aggregator) RecordPeerSequence(peerID uint64, pubKey PublisherKey, seq uint32) {
	if seq == 0 {
		return
	}
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	m, ok := a.peerSeq[peerID]
	if !ok {
		m = make(map[PublisherKey]uint32)
		a.peerSeq[peerID] = m
	}
	if seq > m[pubKey] {
		m[pubKey] = seq
	}
}

// ForgetPeer drops every per-publisher sequence record for `peerID`.
// Called by the router from the peer-disconnect callback so the
// per-peer map doesn't grow unbounded across the lifetime of the
// process. Idempotent for unknown peers.
func (a *Aggregator) ForgetPeer(peerID uint64) {
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	delete(a.peerSeq, peerID)
}

// PeerSequence returns the highest sequence we believe `peerID` has
// for `pubKey`, or 0 if unknown. Read-only accessor for tests and
// observability; production code consults peerSeq via BroadcastLatest.
func (a *Aggregator) PeerSequence(peerID uint64, pubKey PublisherKey) uint32 {
	a.peerSeqMu.Lock()
	defer a.peerSeqMu.Unlock()
	if m, ok := a.peerSeq[peerID]; ok {
		return m[pubKey]
	}
	return 0
}

// BroadcastLatest pushes the most recently accepted list for `pubKey`
// to every connected peer that (a) negotiated ValidatorListPropagation
// and (b) is known to be at a lower sequence than ours. `exceptPeer`
// is the peer ID we just received the list from (or 0 for site-polled
// lists) and is always skipped to avoid echoing back to the sender.
//
// No-op when no broadcaster is wired, when the publisher has no
// accepted list to retain, or when the stored raw bytes are empty.
// Mirrors rippled's ValidatorList::broadcastBlobs at
// ValidatorList.cpp:872-937 — the per-peer publisherListSequence gate
// + the "send the latest stored blob, not the inbound frame"
// invariant. Per-peer state is updated after each successful send so
// subsequent calls naturally skip the just-served peer.
//
// MUST NOT be called with a.mu held — the call snapshots state under
// a.mu briefly then releases it, and may hold peerSeqMu and call into
// the broadcaster (which writes to peer sockets) without either lock.
func (a *Aggregator) BroadcastLatest(pubKey PublisherKey, exceptPeer uint64) {
	a.mu.Lock()
	bcaster := a.bcaster
	if bcaster == nil {
		a.mu.Unlock()
		return
	}
	s, ok := a.state[pubKey]
	if !ok || s.Sequence == 0 || s.Status == StatusRevoked ||
		len(s.RawManifest) == 0 || len(s.RawBlob) == 0 || len(s.RawSignature) == 0 {
		a.mu.Unlock()
		return
	}
	sequence := s.Sequence
	blobVersion := s.Version
	if blobVersion == 0 {
		blobVersion = 1
	}
	rawManifest := append([]byte(nil), s.RawManifest...)
	rawBlob := append([]byte(nil), s.RawBlob...)
	rawSignature := append([]byte(nil), s.RawSignature...)
	// Snapshot the publisher's current blob plus any Remaining blobs
	// into an ordered BroadcastBlob list so v2-capable peers always
	// receive a TMValidatorListCollection (single-entry when no
	// Remaining). Mirrors rippled's sendValidatorList at
	// ValidatorList.cpp:752-757 which selects messageVersion=2 purely
	// on the peer's ValidatorList2Propagation feature, regardless of
	// whether the publisher has Remaining blobs, and buildBlobInfos
	// at ValidatorList.cpp:852-857 which serialises current + remaining
	// into a single map<sequence,…>.
	collBlobs := make([]BroadcastBlob, 0, len(s.Remaining)+1)
	collBlobs = append(collBlobs, BroadcastBlob{
		Blob:      append([]byte(nil), s.RawBlob...),
		Signature: append([]byte(nil), s.RawSignature...),
	})
	maxSeq := sequence
	if len(s.Remaining) > 0 {
		seqs := make([]uint32, 0, len(s.Remaining))
		for seq := range s.Remaining {
			seqs = append(seqs, seq)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		for _, seq := range seqs {
			rb := s.Remaining[seq]
			collBlobs = append(collBlobs, BroadcastBlob{
				Blob:      append([]byte(nil), rb.RawBlob...),
				Signature: append([]byte(nil), rb.RawSignature...),
			})
			if seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	collVersion := blobVersion
	if collVersion < 2 {
		collVersion = 2
	}
	logger := a.logger
	a.mu.Unlock()

	active := bcaster.ActivePeers()
	sent := 0
	for _, peerID := range active {
		if peerID == exceptPeer {
			continue
		}
		if !bcaster.PeerSupportsVL(peerID) {
			continue
		}
		// v2-capable peer: always send the collection (single-entry
		// when there are no Remaining blobs, multi-entry otherwise)
		// so the wire shape matches rippled's broadcastBlobs at
		// ValidatorList.cpp:872-937 which picks the message type
		// purely on the peer's ValidatorList2Propagation feature.
		if bcaster.PeerSupportsV2(peerID) {
			if a.PeerSequence(peerID, pubKey) >= maxSeq {
				continue
			}
			if err := bcaster.SendCollection(peerID, rawManifest, collBlobs, collVersion); err != nil {
				logger.Debug("validator list collection broadcast: send failed",
					"peer", peerID,
					"publisher", hex.EncodeToString(pubKey[:]),
					"max_sequence", maxSeq,
					"error", err)
				continue
			}
			a.RecordPeerSequence(peerID, pubKey, maxSeq)
			sent++
			continue
		}
		if a.PeerSequence(peerID, pubKey) >= sequence {
			continue
		}
		if err := bcaster.SendList(peerID, rawManifest, rawBlob, rawSignature, blobVersion); err != nil {
			logger.Debug("validator list broadcast: send failed",
				"peer", peerID,
				"publisher", hex.EncodeToString(pubKey[:]),
				"sequence", sequence,
				"error", err)
			continue
		}
		// Even if the wire bytes happened to coincide with a frame the
		// peer just sent us, the in-flight RecordPeerSequence call
		// will reach the same target sequence — so updating here is
		// idempotent.
		a.RecordPeerSequence(peerID, pubKey, sequence)
		sent++
	}

	if sent > 0 {
		logger.Debug("validator list broadcast",
			"publisher", hex.EncodeToString(pubKey[:]),
			"sequence", sequence,
			"remaining", len(collBlobs)-1,
			"peers_sent", sent)
	}
}

// mastersEqual reports whether two sorted master-key slices contain
// the same elements in the same order. Used to short-circuit no-op
// OnChange emissions.
func mastersEqual(a, b [][33]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

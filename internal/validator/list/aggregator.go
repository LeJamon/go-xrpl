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
	// effective. Zero when the publisher hasn't specified one (rippled
	// treats absent `effective` as "immediately").
	Effective time.Time

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
	}, nil
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
func (a *Aggregator) PublisherSnapshot() []PublisherState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]PublisherState, 0, len(a.state))
	for _, s := range a.state {
		cp := *s
		if len(s.Validators) > 0 {
			cp.Validators = make([][33]byte, len(s.Validators))
			copy(cp.Validators, s.Validators)
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
// key (when extractable). Wraps the rippled-faithful applyList
// algorithm: verify the publisher manifest, look up its current
// ephemeral signing key, verify the blob signature, JSON-parse the
// blob, then update per-publisher state and trigger an OnChange if the
// trusted union changed.
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
// itself could not be parsed.
func (a *Aggregator) ApplyList(manifestBytes, blob, signature []byte, version uint32, siteURI string) (Disposition, PublisherKey) {
	if !isSupportedVersion(version) {
		return UnsupportedVersion, PublisherKey{}
	}

	// Decode the publisher manifest. The manifest is base64-encoded on
	// the wire; the inner STObject is what manifest.Deserialize wants.
	manifestRaw, err := decodeBase64Tolerant(manifestBytes)
	if err != nil {
		a.logger.Debug("validator list: manifest base64 decode failed", "error", err, "site", siteURI)
		return Malformed, PublisherKey{}
	}
	parsed, err := manifest.Deserialize(manifestRaw)
	if err != nil {
		a.logger.Debug("validator list: manifest deserialize failed", "error", err, "site", siteURI)
		return Malformed, PublisherKey{}
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
		return Untrusted, pubKey
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
		case manifest.Invalid, manifest.BadMasterKey, manifest.BadEphemeralKey:
			// Rippled ValidatorList.cpp:1382-1383 returns
			// `untrusted` for `result == ManifestDisposition::invalid`
			// (and implicitly for badMasterKey/badEphemeralKey via the
			// `!signingKey` fallback). Untrusted maps to feeUselessData
			// (light), Invalid would map to feeInvalidSignature (heavy)
			// — using Untrusted avoids overcharging honest peers that
			// forward a list whose manifest the cache cannot accept.
			return Untrusted, pubKey
		}
	} else {
		// Fall back to direct verification when no cache is wired
		// (tests). The signing key in the manifest is what we'd have
		// pulled from the cache. Mirrors rippled's invalid-manifest →
		// untrusted mapping at ValidatorList.cpp:1382-1383.
		if err := parsed.Verify(); err != nil {
			return Untrusted, pubKey
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
		return Untrusted, pubKey
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
			return Untrusted, pubKey
		}
	}

	if err := verifyBlobSignature(signingKey, blob, signature); err != nil {
		a.logger.Debug("validator list: blob signature invalid", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return Invalid, pubKey
	}

	parsedBlob, disp, err := parseBlob(blob)
	if err != nil {
		a.logger.Debug("validator list: blob parse failed", "error", err, "publisher", hex.EncodeToString(pubKey[:]))
		return disp, pubKey
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

	// Determine disposition by sequence + time ordering. Mirrors the
	// rippled state machine at ValidatorList.cpp:1394-1437. The
	// SameSequence branch is intentionally unguarded by status —
	// rippled returns same_sequence for every repeat of the current
	// sequence regardless of `pubCollection.status`.
	if parsedBlob.Sequence < current.Sequence {
		return Stale, pubKey
	}
	if parsedBlob.Sequence == current.Sequence {
		return SameSequence, pubKey
	}
	if validUntil.Before(now) || validUntil.Equal(now) {
		// Even an expired list still updates the publisher entry so
		// the RPC can surface "expired" — but the validators do NOT
		// flow into the trusted union (status is StatusExpired).
		applied := a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version)
		_ = applied // applyAcceptedLocked sets Status; trusted set recompute below skips expired.
		current.Status = StatusExpired
		a.recomputeAndEmitLocked()
		return Expired, pubKey
	}
	if validFrom.After(now) {
		// Future-dated. Rippled stores this in `remaining` so it can
		// be promoted later. goXRPL's first-cut subsystem keeps a
		// simpler model: drop pending lists (the publisher will
		// republish at effective time, and the 5-min poller cadence
		// limits the lag). Tracked for future work but not blocking
		// for the issue.
		return Pending, pubKey
	}

	a.applyAcceptedLocked(current, parsedBlob, signingKey, validFrom, validUntil, siteURI, now, version)
	a.recomputeAndEmitLocked()
	return Accepted, pubKey
}

// applyAcceptedLocked materializes the parsed blob into the
// publisher's state. Caller must hold a.mu. Does NOT emit OnChange —
// that's done by recomputeAndEmitLocked once the caller has decided
// the disposition warrants a trusted-set recompute.
func (a *Aggregator) applyAcceptedLocked(s *PublisherState, blob *blobJSON, signingKey [33]byte, validFrom, validUntil time.Time, siteURI string, now time.Time, version uint32) bool {
	prevCount := len(s.Validators)
	s.Sequence = blob.Sequence
	s.Effective = validFrom
	s.Expiration = validUntil
	s.SigningKey = signingKey
	s.SiteURI = siteURI
	s.LastUpdate = now
	s.Status = StatusAvailable
	if version > s.Version {
		s.Version = version
	}

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
	// own manifest. Mirrors rippled ValidatorList.cpp:1242-1273.
	if a.manifests != nil {
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
			_ = a.manifests.ApplyManifest(parsed)
		}
	}

	return prevCount != len(keys)
}

// handleRevocation removes a publisher's contribution when its master
// key is revoked by a fresh manifest. Mirrors rippled's
// removePublisherList(StatusRevoked) branch in verify().
func (a *Aggregator) handleRevocation(pubKey PublisherKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.state[pubKey]
	if !ok {
		return
	}
	s.Status = StatusRevoked
	s.Validators = nil
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
// the manifest decoded. The router uses the dispositions to decide
// whether to charge the sender (any Invalid / Malformed) and whether
// to relay (at least one Accepted).
//
// Mirrors rippled's applyLists at ValidatorList.cpp:998-1070.
func (a *Aggregator) ApplyCollection(coll *message.ValidatorListCollection, siteURI string) ([]Disposition, PublisherKey) {
	if coll == nil {
		return []Disposition{Malformed}, PublisherKey{}
	}
	if !isSupportedVersion(coll.Version) {
		return []Disposition{UnsupportedVersion}, PublisherKey{}
	}
	if len(coll.Blobs) == 0 {
		return []Disposition{Malformed}, PublisherKey{}
	}
	out := make([]Disposition, len(coll.Blobs))
	var pubKey PublisherKey
	for i, blob := range coll.Blobs {
		// Per blob: prefer the embedded local manifest when present,
		// else fall back to the collection's shared manifest. Matches
		// rippled applyList(globalManifest, localManifest, ...) at
		// ValidatorList.cpp:1140-1151.
		mf := blob.Manifest
		if len(mf) == 0 {
			mf = coll.Manifest
		}
		disp, pk := a.ApplyList(mf, blob.Blob, blob.Signature, coll.Version, siteURI)
		out[i] = disp
		if pk != (PublisherKey{}) {
			pubKey = pk
		}
	}
	return out, pubKey
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

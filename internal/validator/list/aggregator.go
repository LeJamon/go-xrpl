package list

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
)

// PublisherKey is the 33-byte master public key of a list publisher
// (including the key-type prefix byte: 0xED for ed25519, 0x02/0x03 for
// secp256k1). Used as the map key for per-publisher state.
type PublisherKey [33]byte

// PublisherStatus tracks per-publisher availability. The label set
// (unavailable / available / expired / revoked) matches rippled's
// PublisherStatus enum at rippled/src/xrpld/app/misc/ValidatorList.h:87-100,
// but the underlying iota values are not aligned with rippled — go-xrpl
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

// Tracks the current accepted list plus a queue of future-dated
// "remaining" lists (rippled's PublisherList.remaining at
// ValidatorList.h:75-83) so a publisher rotation announced ahead of
// time can be applied at the right moment.
type publisherState struct {
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
	// RawManifest is the collection-level publisher manifest. A v2 blob may
	// carry a per-blob manifest override; that value is retained separately
	// so relay/cache code can preserve the nil-vs-present distinction.
	RawManifest         []byte
	RawLocalManifest    []byte
	RawLocalManifestSet bool
	RawBlob             []byte
	RawSignature        []byte

	// Remaining is the queue of future-dated lists for this publisher,
	// keyed by sequence and ordered by validFrom. Mirrors rippled's
	// PublisherListCollection.remaining at ValidatorList.h:75-83. A
	// rotation announced ahead of `effective` time lands here and is
	// promoted into the current slot once its validFrom passes — see
	// promoteRemainingLocked. Empty when no rotation is pending.
	Remaining map[uint32]*pendingList

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

type pendingList struct {
	Sequence            uint32
	Effective           time.Time
	EffectiveSet        bool
	Expiration          time.Time
	Validators          [][33]byte
	SiteURI             string
	Version             uint32
	SigningKey          [33]byte
	RawManifest         []byte
	RawLocalManifest    []byte
	RawLocalManifestSet bool
	RawBlob             []byte
	RawSignature        []byte
	EmbeddedManifests   []pendingEmbeddedManifest
}

// pendingEmbeddedManifest retains a validator manifest from a future-dated
// blob until that blob becomes current. The manifest is applied to the
// validator cache only at promotion, matching rippled's updatePublisherList
// timing.
type pendingEmbeddedManifest struct {
	Raw []byte
}

type siteInfoState struct {
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

// RemainingInfo is the immutable value projection of a future-dated list.
// Wire buffers remain private to relay/cache code.
type RemainingInfo struct {
	Sequence            uint32
	Effective           time.Time
	EffectiveSet        bool
	Expiration          time.Time
	Validators          [][33]byte
	SiteURI             string
	Version             uint32
	RawLocalManifestSet bool
}

// PublisherInfo is an immutable value projection of publisher state. Slices
// and maps are copied for each snapshot.
type PublisherInfo struct {
	MasterKey           PublisherKey
	SigningKey          [33]byte
	Status              PublisherStatus
	Sequence            uint32
	Effective           time.Time
	EffectiveSet        bool
	Expiration          time.Time
	Validators          [][33]byte
	SiteURI             string
	LastUpdate          time.Time
	Version             uint32
	RawLocalManifestSet bool
	Remaining           map[uint32]*RemainingInfo
	MaxSequence         uint32
	MaxSequenceSet      bool
}

type SiteInfo struct {
	URI                string
	LastFetched        time.Time
	LastSuccess        time.Time
	LastError          string
	LastDisposition    Disposition
	LastDispositionSet bool
	RefreshSeconds     int
	NextRefresh        time.Time
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
	state map[PublisherKey]*publisherState

	// sites holds per-URL polling state for every URL in
	// validator_list_sites. Updated by the HTTP poller; read by RPC.
	sites []*siteInfoState

	validatorManifests *manifest.Cache
	publisherManifests *manifest.Cache

	// threshold is the minimum number of publishers from `publishers`
	// that must list a validator before that validator is admitted to
	// the effective trusted UNL. Mirrors rippled's listThreshold_
	// (ValidatorList.h:140-141 / ValidatorList.cpp:289).
	threshold int

	staticValidatorCount int

	// onChange is captured into immutable change events while a.mu is held.
	// Events are delivered after the mutation unlocks, so callbacks may
	// re-enter the aggregator and slow callbacks do not hold a.mu.
	onChange func(validators []consensus.NodeID, masterKeys [][33]byte)

	// lastEmitted is the most recently published trusted set (master
	// keys, sorted). Cached so we suppress no-op OnChange callbacks
	// when a publisher list update doesn't move any validator into or
	// out of the union.
	lastEmitted [][33]byte

	// listed is the NodeID snapshot of every validator carried by at
	// least one live publisher list (count >= 1 — rippled's "listed", as
	// opposed to "trusted" at count >= threshold). Refreshed on every
	// count recompute and published via atomic pointer: IsListed serves
	// the consensus validation path, which runs under the engine's lock
	// while the OnChange → SetTrustedValidators → engine trust-refresh
	// chain runs under a.mu — reading a.mu here would be an ABBA deadlock.
	listed        atomic.Pointer[map[consensus.NodeID]struct{}]
	listedMasters atomic.Pointer[map[[33]byte]struct{}]

	// unlBlocked mirrors rippled's NetworkOPs unlBlocked_ (NetworkOPs.cpp:750):
	// the sticky UNL lock-down. Written under a.mu by recomputeAndEmitLocked
	// but stored atomically so IsUNLBlocked reads it lock-free — the consensus
	// bow-out polls it while holding the engine lock, and taking a.mu there
	// would ABBA against the onChange -> onTrustChanged -> e.mu fan-out (same
	// reason IsListed is lock-free). recomputeAndEmitLocked commits the flag in
	// a single Store so readers never see a transient intermediate.
	unlBlocked atomic.Bool
	// quorumUnavailable tracks rippled's stricter calculateQuorum cutoff,
	// which can differ from unlBlocked with multiple publishers.
	quorumUnavailable atomic.Bool

	// clock returns the wall-clock time the aggregator uses to gate
	// effective / expiration comparisons. Overridable for tests.
	clock func() time.Time

	// beforeListCommit is a private deterministic test seam. Production code
	// leaves it nil; tests use it to mutate the independent manifest cache
	// after verification and before the commit-time recheck.
	beforeListCommit func()

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

	// pendingCacheWrites holds marshaled cache-file mutations produced
	// under a.mu by writeCacheLocked / removeCacheLocked, keyed by
	// publisher so only the latest mutation per publisher is retained.
	// flushCacheWrites drains and applies them to disk after a.mu is
	// released, so cache disk I/O never stalls VL ingest or the validators
	// RPC read path. cacheWriteSeq is a monotonic stamp (guarded by a.mu)
	// used to order mutations across concurrent flushers.
	pendingCacheWrites map[PublisherKey]pendingCacheWrite
	cacheWriteSeq      uint64
	cacheGeneration    uint64

	// cacheWriteMu serializes the disk syscalls in flushCacheWrites;
	// cacheWritten records the highest cacheWriteSeq already persisted per
	// publisher so a superseded mutation is dropped. Both guarded by
	// cacheWriteMu, never by a.mu.
	cacheWriteMu sync.Mutex
	cacheWritten map[PublisherKey]uint64

	cacheOps cacheFileOps

	changes changeDispatcher
}

// Config carries Aggregator construction parameters. All fields are
// optional; defaults handle nil logger, clock, and manifest caches so the type
// is usable in narrowly-scoped tests.
type Config struct {
	PublisherKeys        []PublisherKey
	SiteURIs             []string
	Threshold            int
	StaticValidatorCount int
	ValidatorManifests   *manifest.Cache
	PublisherManifests   *manifest.Cache
	Clock                func() time.Time
	Logger               *slog.Logger
}

// New constructs an Aggregator from the operator-supplied config.
// Returns an error if a publisher key has an unrecognized key-type
// prefix — this is a configuration bug that should fail boot rather
// than silently disable the publisher.
func New(cfg Config) (*Aggregator, error) {
	publishers := make(map[PublisherKey]struct{}, len(cfg.PublisherKeys))
	state := make(map[PublisherKey]*publisherState, len(cfg.PublisherKeys))
	for _, k := range cfg.PublisherKeys {
		var zero PublisherKey
		if k == zero {
			return nil, errors.New("publisher key is all zero")
		}
		if crypto.PublicKeyType(k[:]) == crypto.KeyTypeUnknown {
			return nil, fmt.Errorf("publisher key has unknown key type prefix 0x%02x", k[0])
		}
		publishers[k] = struct{}{}
		state[k] = &publisherState{
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
	initialNextRefresh := clock().Add(defaultRefreshInterval)
	// Seed RefreshSeconds with the default refresh interval so the
	// `refresh_interval_min` RPC field reports the configured cadence
	// from boot, before any envelope-supplied override is observed.
	// Mirrors rippled ValidatorSite.cpp:81 where Site::refreshInterval
	// is initialised to default_refresh_interval (5 minutes) at
	// construction and emitted unconditionally in getJson.
	defaultRefreshSec := int(defaultRefreshInterval / time.Second)
	sites := make([]*siteInfoState, 0, len(cfg.SiteURIs))
	for _, u := range cfg.SiteURIs {
		if err := validateSiteURI(u); err != nil {
			return nil, fmt.Errorf("invalid validator site uri %q: %w", u, err)
		}
		sites = append(sites, &siteInfoState{
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
	if threshold < 0 {
		return nil, fmt.Errorf("threshold cannot be negative: %d", threshold)
	}
	if threshold == 0 && len(publishers) > 0 {
		// Mirror rippled's default: ceil(N/2 + 1) for N >= 3, else 1.
		// Matches config.ValidatorsConfig.EffectiveListThreshold().
		if len(publishers) < 3 {
			threshold = 1
		} else {
			threshold = (len(publishers) / 2) + 1
		}
	}
	if threshold > len(publishers) {
		return nil, fmt.Errorf("threshold %d exceeds publisher count %d", threshold, len(publishers))
	}
	validatorManifests := cfg.ValidatorManifests
	publisherManifests := cfg.PublisherManifests
	if validatorManifests == nil {
		validatorManifests = manifest.NewCache()
	}
	if publisherManifests == nil {
		publisherManifests = manifest.NewCache()
	}
	for key := range publishers {
		if publisherManifests.Revoked([33]byte(key)) {
			state[key].Status = StatusRevoked
		}
	}
	staticValidatorCount := cfg.StaticValidatorCount
	if staticValidatorCount < 0 {
		staticValidatorCount = 0
	}
	return &Aggregator{
		publishers:           publishers,
		state:                state,
		sites:                sites,
		validatorManifests:   validatorManifests,
		publisherManifests:   publisherManifests,
		threshold:            threshold,
		staticValidatorCount: staticValidatorCount,
		clock:                clock,
		logger:               logger,
		peerSeq:              make(map[uint64]map[PublisherKey]uint32),
		pendingCacheWrites:   make(map[PublisherKey]pendingCacheWrite),
		cacheWritten:         make(map[PublisherKey]uint64),
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
// Passing nil clears the callback. Safe to call before or after polling starts.
func (a *Aggregator) OnChange(cb func(validators []consensus.NodeID, masterKeys [][33]byte)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onChange = cb
}

func (a *Aggregator) SetStaticValidatorCount(count int) {
	if count < 0 {
		count = 0
	}
	a.mu.Lock()
	defer a.dispatchChanges()
	defer a.flushCacheWrites()
	defer a.mu.Unlock()
	a.staticValidatorCount = count
	a.recomputeAndEmitLocked()
}

// PublisherCount returns the number of configured publishers in the
// trust set — a constant for the lifetime of the aggregator.
func (a *Aggregator) PublisherCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.publishers)
}

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

// IsUNLBlocked reports rippled's NetworkOPs UNL-blocked flag
// (NetworkOPs.cpp:1862-1864) — the sticky lock-down maintained by
// ValidatorList::updateTrusted and mirrored here in recomputeAndEmitLocked. It
// latches true the moment a configured publisher's live list expires
// (ValidatorList.cpp:1996-2001) or the effective trusted union becomes empty
// (ValidatorList.cpp:2096-2101), and clears only once every configured
// publisher again carries an available list (ValidatorList.cpp:2002-2006). A
// node with no publishers configured is never blocked. The flag is sticky on
// purpose: rippled prioritizes safety over liveness, so e.g. a publisher
// revoked after its list expired keeps the node blocked even while another
// publisher stays healthy — a state a stateless snapshot cannot reproduce.
func (a *Aggregator) IsUNLBlocked() bool {
	return a.unlBlocked.Load()
}

// IsQuorumUnavailable reports whether too many configured publisher lists are
// unavailable to calculate a safe validation quorum.
func (a *Aggregator) IsQuorumUnavailable() bool {
	return a.quorumUnavailable.Load()
}

// PublisherSnapshot returns a deep copy of the per-publisher state for
// RPC and observability. Order is sorted by publisher master key for
// stable output. Safe to call concurrently with ingest.
func (a *Aggregator) PublisherSnapshot() []PublisherInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]PublisherInfo, 0, len(a.state))
	for _, s := range a.state {
		cp := PublisherInfo{
			MasterKey:           s.MasterKey,
			SigningKey:          s.SigningKey,
			Status:              s.Status,
			Sequence:            s.Sequence,
			Effective:           s.Effective,
			EffectiveSet:        s.EffectiveSet,
			Expiration:          s.Expiration,
			SiteURI:             s.SiteURI,
			LastUpdate:          s.LastUpdate,
			Version:             s.Version,
			RawLocalManifestSet: s.RawLocalManifestSet,
			MaxSequence:         s.MaxSequence,
			MaxSequenceSet:      s.MaxSequenceSet,
		}
		if len(s.Validators) > 0 {
			cp.Validators = make([][33]byte, len(s.Validators))
			copy(cp.Validators, s.Validators)
		}
		if len(s.Remaining) > 0 {
			cp.Remaining = make(map[uint32]*RemainingInfo, len(s.Remaining))
			for seq, p := range s.Remaining {
				pcopy := &RemainingInfo{
					Sequence:            p.Sequence,
					Effective:           p.Effective,
					EffectiveSet:        p.EffectiveSet,
					Expiration:          p.Expiration,
					SiteURI:             p.SiteURI,
					Version:             p.Version,
					RawLocalManifestSet: p.RawLocalManifestSet,
				}
				if len(p.Validators) > 0 {
					pcopy.Validators = append([][33]byte(nil), p.Validators...)
				}
				cp.Remaining[seq] = pcopy
			}
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].MasterKey[:]) < string(out[j].MasterKey[:])
	})
	return out
}

func (a *Aggregator) SiteSnapshot() []SiteInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]SiteInfo, len(a.sites))
	for i, s := range a.sites {
		out[i] = SiteInfo{
			URI:                s.URI,
			LastFetched:        s.LastFetched,
			LastSuccess:        s.LastSuccess,
			LastError:          s.LastError,
			LastDisposition:    s.LastDisposition,
			LastDispositionSet: s.LastDispositionSet,
			RefreshSeconds:     s.RefreshSeconds,
			NextRefresh:        s.NextRefresh,
		}
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

func (a *Aggregator) siteOccurrenceLocked(uri string, occurrence int) *siteInfoState {
	for _, site := range a.sites {
		if site.URI != uri {
			continue
		}
		if occurrence == 0 {
			return site
		}
		occurrence--
	}
	return nil
}

func (a *Aggregator) setNextRefreshOccurrence(uri string, occurrence int, nextRefresh time.Time) {
	if nextRefresh.IsZero() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	site := a.siteOccurrenceLocked(uri, occurrence)
	if site == nil {
		return
	}
	site.NextRefresh = nextRefresh
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

func (a *Aggregator) updateSiteStateOccurrence(uri string, occurrence int, lastFetched, lastSuccess time.Time, lastErr string, lastDisp Disposition, refreshSec int, nextRefresh time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.siteOccurrenceLocked(uri, occurrence)
	if s == nil {
		return
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
}

// Tick performs a time-driven promotion sweep across every publisher
// and emits OnChange if the resulting trusted union changed. Callers
// should invoke this periodically (rippled drives it from updateTrusted
// at every ledger close, ValidatorList.cpp:1910-1928); without an
// external tick a pending rotation announced during a quiet period
// remains pending.
//
// Safe to call from any goroutine. Briefly takes the aggregator lock.
func (a *Aggregator) Tick() {
	a.mu.Lock()
	now := a.clock()
	type promotion struct {
		publisher PublisherKey
		sequence  uint32
	}
	promoted := make([]promotion, 0, len(a.state))
	for pubKey, s := range a.state {
		if sequence := a.promoteRemainingLocked(s, now); sequence != 0 {
			promoted = append(promoted, promotion{publisher: pubKey, sequence: sequence})
		}
	}
	a.recomputeAndEmitLocked()
	a.mu.Unlock()
	a.flushCacheWrites()
	a.dispatchChanges()
	for _, item := range promoted {
		a.broadcastLatest(item.publisher, 0, item.sequence)
	}
}

// handleRevocation removes a publisher's contribution when its master
// key is revoked by a fresh manifest. Mirrors rippled's
// removePublisherList(StatusRevoked) branch in verify(). Also clears
// the retained wire bytes so a revoked publisher is never rebroadcast.
func (a *Aggregator) handleRevocation(pubKey PublisherKey, asyncDispatch bool) {
	a.mu.Lock()
	if asyncDispatch {
		defer a.dispatchChangesAsync()
	} else {
		defer a.dispatchChanges()
	}
	defer a.flushCacheWrites()
	defer a.mu.Unlock()
	s, ok := a.state[pubKey]
	if !ok {
		return
	}
	s.Status = StatusRevoked
	a.removeCacheLocked(s.MasterKey)
	s.Validators = nil
	s.RawManifest = nil
	s.RawLocalManifest = nil
	s.RawLocalManifestSet = false
	s.RawBlob = nil
	s.RawSignature = nil
	s.Remaining = nil
	s.MaxSequence = 0
	s.MaxSequenceSet = false
	a.recomputeAndEmitLocked()
}

// computeValidatorCountsLocked counts, per validator master key, how many
// publishers with a live (available, effective, unexpired) list carry
// it. Caller must hold a.mu and filters by threshold afterwards. As a
// side effect the listed-NodeID snapshot is republished, so IsListed
// tracks every count recompute (ingest, Tick, trusted-set reads).
func (a *Aggregator) computeValidatorCountsLocked(now time.Time) map[[33]byte]int {
	counts := make(map[[33]byte]int, 64)
	listedMasters := make(map[[33]byte]struct{}, 64)
	for _, s := range a.state {
		for _, k := range s.Validators {
			listedMasters[k] = struct{}{}
		}
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
	listed := make(map[consensus.NodeID]struct{}, len(listedMasters))
	for k := range listedMasters {
		listed[consensus.CalcNodeID(k)] = struct{}{}
	}
	a.listed.Store(&listed)
	a.listedMasters.Store(&listedMasters)
	if a.validatorManifests != nil {
		for master := range listedMasters {
			a.validatorManifests.PromoteToTrusted(master)
		}
	}
	return counts
}

// IsListed reports whether node's master key appears in at least one retained
// publisher list — rippled's ValidatorList::listed, one tier below trusted.
// Lock-free (atomic snapshot) so the consensus validation path can query it
// without ordering against a.mu.
func (a *Aggregator) IsListed(node consensus.NodeID) bool {
	listed := a.listed.Load()
	if listed == nil {
		return false
	}
	_, ok := (*listed)[node]
	return ok
}

// IsMasterListed reports whether a validator master key appears in a retained
// configured publisher list.
func (a *Aggregator) IsMasterListed(master [33]byte) bool {
	listed := a.listedMasters.Load()
	if listed == nil {
		return false
	}
	_, ok := (*listed)[master]
	return ok
}

// recomputeAndEmitLocked walks the per-publisher state, computes the
// union of validators present in at least `threshold` publishers' lists,
// and queues an OnChange event when either the trusted set or UNL-blocked
// state changes. Caller MUST hold a.mu; delivery happens after the caller
// unlocks via dispatchChanges.
func (a *Aggregator) recomputeAndEmitLocked() {
	if a.threshold <= 0 || len(a.publishers) == 0 {
		return
	}

	now := a.clock()
	// UNL-blocked accounting — mirrors rippled updateTrusted's per-publisher
	// expiry pass and lock-down flag (ValidatorList.cpp:1929-2006, 2096-2101).
	// good holds only while every configured publisher carries a live list; a
	// list that times out flips to expired and latches the block immediately
	// (line 1970 clears the list, 1996-2001 sets the flag), and the flag clears
	// only when all publishers are available again — safety over liveness. Run
	// before the no-op early-out below so the flag tracks state even when the
	// trusted union is unchanged.
	// Seed from the current (sticky) value: the latch only sets on an
	// expiry transition or an empty union and only clears when every
	// publisher is available again, so the flag carries across recomputes.
	// Accumulate in the local and commit once below.
	blocked := a.unlBlocked.Load()
	good := true
	unavailable := 0
	for _, s := range a.state {
		if s.Status == StatusAvailable && !s.Expiration.IsZero() && !s.Expiration.After(now) {
			s.Status = StatusExpired
			s.Validators = nil
			blocked = true
		}
		if s.Status != StatusAvailable {
			good = false
			unavailable++
		}
	}
	if good {
		blocked = false
	}

	counts := a.computeValidatorCountsLocked(now)

	trusted := make([][33]byte, 0, len(counts))
	for k, c := range counts {
		if c >= a.threshold {
			trusted = append(trusted, k)
		}
	}
	sort.Slice(trusted, func(i, j int) bool {
		return string(trusted[i][:]) < string(trusted[j][:])
	})

	// "No validators. Lock down." — publishers are configured (guaranteed by
	// the guard above) but the effective trusted union is empty
	// (ValidatorList.cpp:2096-2101). Latch after the good/clear pass so an
	// empty union always wins, matching rippled's ordering (clear at line 2006,
	// then set at 2100). Single Store so lock-free IsUNLBlocked never sees a
	// transient.
	if len(trusted) == 0 && a.staticValidatorCount == 0 {
		blocked = true
	}
	blockedChanged := a.unlBlocked.Swap(blocked) != blocked

	previousQuorumUnavailable := a.quorumUnavailable.Load()
	errorThreshold := min(a.threshold, len(a.publishers)-a.threshold+1)
	quorumUnavailable := unavailable >= errorThreshold
	a.quorumUnavailable.Store(quorumUnavailable)

	if slices.Equal(trusted, a.lastEmitted) && !blockedChanged && quorumUnavailable == previousQuorumUnavailable {
		return
	}
	a.lastEmitted = append(a.lastEmitted[:0], trusted...)

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
	a.changes.enqueue(changeEvent{
		callback:   a.onChange,
		validators: append([]consensus.NodeID(nil), nodeIDs...),
		masterKeys: append([][33]byte(nil), trusted...),
	})
}

// TrustedValidators returns the current effective trusted set as
// NodeIDs + master keys, both sorted by master key for determinism.
// Recomputes on every call from the current per-publisher state.
// Mirrors rippled's ValidatorList::getQuorumKeys() shape.
func (a *Aggregator) TrustedValidators() ([]consensus.NodeID, [][33]byte) {
	a.mu.Lock()
	defer a.flushCacheWrites()
	defer a.mu.Unlock()

	if a.threshold <= 0 || len(a.publishers) == 0 {
		return nil, nil
	}

	counts := a.computeValidatorCountsLocked(a.clock())

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

func isSupportedVersion(v uint32) bool {
	return v == supportedVersionV1 || v == supportedVersionV2
}

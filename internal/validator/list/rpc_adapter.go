package list

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// rippledTimeLayout mirrors rippled's to_string(NetClock::time_point)
// format `%Y-%b-%d %T %Z` (chrono.h:75-88) — e.g.
// "2026-May-18 10:30:00 UTC". Used for every timestamp field exposed
// via the validators / validator_list_sites RPCs.
const rippledTimeLayout = "2006-Jan-02 15:04:05 UTC"

// RPCReader wraps an *Aggregator so it satisfies the
// rpctypes.ValidatorListReader interface used by the validators and
// validator_list_sites RPC methods. Lives in this package (not in
// internal/rpc) so the dependency direction stays
// rpc → validator/list, never the reverse.
//
// Always pass through nil-safely: a nil *RPCReader returns the same
// empty-shape responses the legacy stub did, which lets server bootstrap
// install the adapter unconditionally and have it degrade gracefully
// when no publisher trust is configured.
type RPCReader struct {
	agg *Aggregator
}

// NewRPCReader constructs an RPCReader. Passing nil is allowed and
// yields an inert reader.
func NewRPCReader(agg *Aggregator) *RPCReader {
	return &RPCReader{agg: agg}
}

func (r *RPCReader) HasConfiguredPublishers() bool {
	if r == nil || r.agg == nil {
		return false
	}
	return r.agg.HasConfiguredPublishers()
}

func (r *RPCReader) PublisherCount() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.PublisherCount()
}

func (r *RPCReader) Threshold() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.Threshold()
}

func (r *RPCReader) Publishers() []rpctypes.ValidatorListPublisherInfo {
	if r == nil || r.agg == nil {
		return nil
	}
	snap := r.agg.PublisherSnapshot()
	out := make([]rpctypes.ValidatorListPublisherInfo, 0, len(snap))
	for _, s := range snap {
		info := rpctypes.ValidatorListPublisherInfo{
			PublicKeyHex:     strings.ToUpper(hex.EncodeToString(s.MasterKey[:])),
			Available:        s.Status == StatusAvailable,
			Status:           s.Status.String(),
			Sequence:         s.Sequence,
			Version:          s.Version,
			SiteURI:          s.SiteURI,
			ValidatorsBase58: encodeNodeKeysBase58(s.Validators),
			EffectiveSet:     s.EffectiveSet,
		}
		// Mirrors rippled ValidatorList.cpp:1682-1683 which gates the
		// JSON emit on `validFrom != TimeKeeper::time_point{}`. The Go
		// zero-value time.Time is not a usable sentinel here because
		// rippleSecondsToUnix(0) resolves to year 2000.
		if s.EffectiveSet && !s.Effective.IsZero() {
			info.EffectiveUnix = s.Effective.Unix()
			info.EffectiveISO = s.Effective.UTC().Format(rippledTimeLayout)
		}
		if !s.Expiration.IsZero() {
			info.ExpirationUnix = s.Expiration.Unix()
			info.ExpirationISO = s.Expiration.UTC().Format(rippledTimeLayout)
		}
		// Mirror rippled's `remaining` array per publisher
		// (ValidatorList.cpp:1699-1713). Each entry is the future-dated
		// list waiting to be promoted into the current slot. Sorted by
		// sequence for stable RPC output.
		if len(s.Remaining) > 0 {
			seqs := make([]uint32, 0, len(s.Remaining))
			for seq := range s.Remaining {
				seqs = append(seqs, seq)
			}
			sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
			rem := make([]rpctypes.ValidatorListRemainingInfo, 0, len(seqs))
			for _, seq := range seqs {
				p := s.Remaining[seq]
				e := rpctypes.ValidatorListRemainingInfo{
					Sequence:         p.Sequence,
					Version:          p.Version,
					SiteURI:          p.SiteURI,
					EffectiveSet:     p.EffectiveSet,
					ValidatorsBase58: encodeNodeKeysBase58(p.Validators),
				}
				if p.EffectiveSet && !p.Effective.IsZero() {
					e.EffectiveUnix = p.Effective.Unix()
					e.EffectiveISO = p.Effective.UTC().Format(rippledTimeLayout)
				}
				if !p.Expiration.IsZero() {
					e.ExpirationUnix = p.Expiration.Unix()
					e.ExpirationISO = p.Expiration.UTC().Format(rippledTimeLayout)
				}
				rem = append(rem, e)
			}
			info.Remaining = rem
		}
		out = append(out, info)
	}
	return out
}

func (r *RPCReader) Sites() []rpctypes.ValidatorListSiteInfo {
	if r == nil || r.agg == nil {
		return nil
	}
	snap := r.agg.SiteSnapshot()
	out := make([]rpctypes.ValidatorListSiteInfo, 0, len(snap))
	for _, s := range snap {
		info := rpctypes.ValidatorListSiteInfo{
			URI:                s.URI,
			LastError:          s.LastError,
			LastDispositionSet: s.LastDispositionSet,
			RefreshIntervalSec: s.RefreshSeconds,
			// Truncate (not ceiling) to match rippled which stores
			// `std::chrono::minutes` directly — no sub-minute precision
			// exists upstream so the rounding mode matters only for the
			// never-clamped initial value (0 → 0).
			RefreshIntervalMin: s.RefreshSeconds / 60,
		}
		if s.LastDispositionSet {
			info.LastDisposition = dispositionRPCLabel(s.LastDisposition)
		}
		if !s.LastFetched.IsZero() {
			info.LastRefreshUnix = s.LastFetched.Unix()
			info.LastRefreshISO = s.LastFetched.UTC().Format(rippledTimeLayout)
		}
		if !s.LastSuccess.IsZero() {
			info.LastSuccessUnix = s.LastSuccess.Unix()
		}
		if !s.NextRefresh.IsZero() {
			info.NextRefreshUnix = s.NextRefresh.Unix()
			info.NextRefreshISO = s.NextRefresh.UTC().Format(rippledTimeLayout)
		}
		out = append(out, info)
	}
	return out
}

// TrustedMasterKeys returns the publisher-contributed validator master
// keys currently in the effective trusted UNL.
func (r *RPCReader) TrustedMasterKeys() [][33]byte {
	if r == nil || r.agg == nil {
		return nil
	}
	_, masters := r.agg.TrustedValidators()
	return masters
}

// ListedValidators returns the union of every validator master key listed by
// any publisher, each tagged with whether it is currently trusted. Mirrors
// rippled ValidatorList::for_each_listed (ValidatorList.cpp:1750): the listed
// set is the union across publisher lists (rippled's keyListings_) and the
// trusted flag is membership in the effective UNL (trustedMasterKeys_).
func (r *RPCReader) ListedValidators() []rpctypes.ListedValidator {
	if r == nil || r.agg == nil {
		return nil
	}
	_, trustedMasters := r.agg.TrustedValidators()
	trusted := make(map[[33]byte]bool, len(trustedMasters))
	for _, k := range trustedMasters {
		trusted[k] = true
	}

	seen := make(map[[33]byte]struct{})
	var out []rpctypes.ListedValidator
	for _, p := range r.agg.PublisherSnapshot() {
		// rippled's keyListings_ only counts keys from currently-applied
		// (available) publisher lists; expired / unavailable lists are
		// decremented out. Mirror that by skipping non-available publishers.
		if p.Status != StatusAvailable {
			continue
		}
		for _, mk := range p.Validators {
			if _, dup := seen[mk]; dup {
				continue
			}
			seen[mk] = struct{}{}
			out = append(out, rpctypes.ListedValidator{MasterKey: mk, Trusted: trusted[mk]})
		}
	}
	return out
}

// dispositionRPCLabel returns the wire string for a Disposition as it
// appears in the validator_list_sites RPC. Folds the goXRPL-only
// Malformed back into rippled's "invalid" so external consumers parsing
// rippled's ListDisposition enum see only labels rippled would emit.
func dispositionRPCLabel(d Disposition) string {
	if d == Malformed {
		return Invalid.String()
	}
	return d.String()
}

// encodeNodeKeysBase58 returns base58-encoded NodePublic strings for a
// slice of 33-byte master keys. Entries that fail to encode (wrong
// length, etc.) are skipped silently — the alternative would be to
// return an error from a read-only RPC adapter, which doesn't match
// the rest of the interface.
func encodeNodeKeysBase58(keys [][33]byte) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		enc, err := addresscodec.EncodeNodePublicKey(k[:])
		if err != nil {
			continue
		}
		out = append(out, enc)
	}
	return out
}

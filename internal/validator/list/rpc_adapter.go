package list

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	rpctypes "github.com/LeJamon/goXRPLd/internal/rpc/types"
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

// HasConfiguredPublishers reports whether the underlying aggregator was
// initialized with at least one publisher key.
func (r *RPCReader) HasConfiguredPublishers() bool {
	if r == nil || r.agg == nil {
		return false
	}
	return r.agg.HasConfiguredPublishers()
}

// PublisherCount returns the number of configured publishers in the
// aggregator's trust set.
func (r *RPCReader) PublisherCount() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.PublisherCount()
}

// Threshold returns the configured publisher threshold.
func (r *RPCReader) Threshold() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.Threshold()
}

// Publishers translates the aggregator's PublisherSnapshot into the
// RPC-facing ValidatorListPublisherInfo shape.
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
		}
		if !s.Effective.IsZero() {
			info.EffectiveUnix = s.Effective.Unix()
			info.EffectiveISO = s.Effective.UTC().Format(rippledTimeLayout)
		}
		if !s.Expiration.IsZero() {
			info.ExpirationUnix = s.Expiration.Unix()
			info.ExpirationISO = s.Expiration.UTC().Format(rippledTimeLayout)
		}
		out = append(out, info)
	}
	return out
}

// Sites translates the aggregator's SiteSnapshot into the RPC-facing
// ValidatorListSiteInfo shape.
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

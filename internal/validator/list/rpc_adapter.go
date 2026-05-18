package list

import (
	"encoding/hex"
	"strings"

	rpctypes "github.com/LeJamon/goXRPLd/internal/rpc/types"
)

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
			PublicKey:      strings.ToUpper(hex.EncodeToString(s.MasterKey[:])),
			Status:         s.Status.String(),
			Sequence:       s.Sequence,
			ValidatorCount: len(s.Validators),
			SiteURI:        s.SiteURI,
		}
		if !s.Effective.IsZero() {
			info.EffectiveUnix = s.Effective.Unix()
		}
		if !s.Expiration.IsZero() {
			info.ExpirationUnix = s.Expiration.Unix()
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
			LastDisposition:    s.LastDisposition.String(),
			RefreshIntervalSec: s.RefreshSeconds,
		}
		if !s.LastFetched.IsZero() {
			info.LastRefreshUnix = s.LastFetched.Unix()
		}
		if !s.LastSuccess.IsZero() {
			info.LastSuccessUnix = s.LastSuccess.Unix()
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

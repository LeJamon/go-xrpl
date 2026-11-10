package node

import (
	"bytes"
	"encoding/hex"
	"slices"
	"strings"

	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
)

// rippledTimeLayout mirrors rippled's to_string(NetClock::time_point)
// format `%Y-%b-%d %T %Z` — e.g. "2026-May-18 10:30:00 UTC".
const rippledTimeLayout = "2006-Jan-02 15:04:05.000000000 UTC"

var _ rpctypes.ValidatorListReader = (*validatorListReader)(nil)

type validatorListReader struct {
	agg               *validatorlist.Aggregator
	trustedMasterKeys func() [][33]byte
	revokedMasterKey  func([33]byte) bool
}

func (r *validatorListReader) HasConfiguredPublishers() bool {
	if r == nil || r.agg == nil {
		return false
	}
	return r.agg.HasConfiguredPublishers()
}

func (r *validatorListReader) PublisherCount() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.PublisherCount()
}

func (r *validatorListReader) Threshold() int {
	if r == nil || r.agg == nil {
		return 0
	}
	return r.agg.Threshold()
}

func (r *validatorListReader) IsUNLBlocked() bool {
	return r != nil && r.agg != nil && r.agg.IsUNLBlocked()
}

func (r *validatorListReader) Publishers() []rpctypes.ValidatorListPublisherInfo {
	if r == nil || r.agg == nil {
		return nil
	}
	return publisherInfos(r.agg.PublisherSnapshot())
}

func (r *validatorListReader) Sites() []rpctypes.ValidatorListSiteInfo {
	if r == nil || r.agg == nil {
		return nil
	}
	return siteInfos(r.agg.SiteSnapshot())
}

// TrustedMasterKeys returns the publisher-contributed validator master
// keys currently in the effective trusted UNL.
func (r *validatorListReader) TrustedMasterKeys() [][33]byte {
	if r == nil || r.agg == nil {
		return nil
	}
	_, masters := r.agg.TrustedValidators()
	return masters
}

// ListedValidators returns the union of every publisher-listed key and every
// key in the effective trusted set, each tagged with current trust membership.
// The latter includes static and local validators, matching rippled's
// keyListings_ entries used by unl_list when no publisher list is configured.
func (r *validatorListReader) ListedValidators() []rpctypes.ListedValidator {
	if r == nil {
		return nil
	}
	var trustedMasters [][33]byte
	if r.trustedMasterKeys != nil {
		trustedMasters = r.trustedMasterKeys()
	}
	var publishers []validatorlist.PublisherInfo
	if r.agg != nil {
		publishers = r.agg.PublisherSnapshot()
	}
	return projectListedValidators(publishers, trustedMasters, r.revokedMasterKey)
}

func projectListedValidators(
	publishers []validatorlist.PublisherInfo,
	trustedMasters [][33]byte,
	revokedMasterKey func([33]byte) bool,
) []rpctypes.ListedValidator {
	listed := make(map[[33]byte]bool, len(trustedMasters))
	for _, publisher := range publishers {
		for _, key := range publisher.Validators {
			listed[key] = false
		}
	}
	for _, key := range trustedMasters {
		listed[key] = revokedMasterKey == nil || !revokedMasterKey(key)
	}

	if len(listed) == 0 {
		return nil
	}
	out := make([]rpctypes.ListedValidator, 0, len(listed))
	for mk, trusted := range listed {
		out = append(out, rpctypes.ListedValidator{MasterKey: mk, Trusted: trusted})
	}
	slices.SortFunc(out, func(a, b rpctypes.ListedValidator) int {
		return bytes.Compare(a.MasterKey[:], b.MasterKey[:])
	})
	return out
}

func publisherInfos(snap []validatorlist.PublisherInfo) []rpctypes.ValidatorListPublisherInfo {
	out := make([]rpctypes.ValidatorListPublisherInfo, 0, len(snap))
	for _, s := range snap {
		info := rpctypes.ValidatorListPublisherInfo{
			PublicKeyHex:     strings.ToUpper(hex.EncodeToString(s.MasterKey[:])),
			Available:        s.Status == validatorlist.StatusAvailable,
			Status:           s.Status.String(),
			Sequence:         s.Sequence,
			Version:          s.Version,
			SiteURI:          s.SiteURI,
			ValidatorsBase58: nodeKeysBase58(s.Validators),
			EffectiveSet:     s.EffectiveSet,
		}
		// Rippled's zero validFrom is the ripple epoch, so explicit effective=0
		// is omitted just like an absent field.
		if s.EffectiveSet && protocol.RippleSeconds(s.Effective) != 0 {
			info.EffectiveUnix = s.Effective.Unix()
			info.EffectiveISO = s.Effective.UTC().Format(rippledTimeLayout)
		}
		if !s.Expiration.IsZero() {
			info.ExpirationUnix = s.Expiration.Unix()
			info.ExpirationISO = s.Expiration.UTC().Format(rippledTimeLayout)
		}
		// Future-dated lists awaiting promotion, sorted by sequence for
		// stable output.
		if len(s.Remaining) > 0 {
			seqs := make([]uint32, 0, len(s.Remaining))
			for seq := range s.Remaining {
				seqs = append(seqs, seq)
			}
			slices.Sort(seqs)
			rem := make([]rpctypes.ValidatorListRemainingInfo, 0, len(seqs))
			for _, seq := range seqs {
				p := s.Remaining[seq]
				e := rpctypes.ValidatorListRemainingInfo{
					Sequence:         p.Sequence,
					Version:          p.Version,
					SiteURI:          p.SiteURI,
					EffectiveSet:     p.EffectiveSet,
					ValidatorsBase58: nodeKeysBase58(p.Validators),
				}
				if p.EffectiveSet && protocol.RippleSeconds(p.Effective) != 0 {
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

func siteInfos(snap []validatorlist.SiteInfo) []rpctypes.ValidatorListSiteInfo {
	out := make([]rpctypes.ValidatorListSiteInfo, 0, len(snap))
	for _, s := range snap {
		info := rpctypes.ValidatorListSiteInfo{
			URI:                s.URI,
			LastError:          s.LastError,
			LastDispositionSet: s.LastDispositionSet,
			RefreshIntervalSec: s.RefreshSeconds,
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

// dispositionRPCLabel folds the go-xrpl-only Malformed disposition into
// rippled's "invalid" so consumers only see labels rippled would emit.
func dispositionRPCLabel(d validatorlist.Disposition) string {
	if d == validatorlist.Malformed {
		return validatorlist.Invalid.String()
	}
	return d.String()
}

// nodeKeysBase58 is validatorKeysBase58 with nil for an empty input.
func nodeKeysBase58(keys [][33]byte) []string {
	if len(keys) == 0 {
		return nil
	}
	return validatorKeysBase58(keys)
}

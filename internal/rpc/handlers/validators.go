package handlers

import (
	"encoding/json"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// ValidatorsMethod handles the `validators` admin RPC. Mirrors rippled's
// ValidatorList::getJson at rippled/src/xrpld/app/misc/detail/
// ValidatorList.cpp:1617-1747:
//
//   - validation_quorum (UInt)
//   - validator_list (count / status / expiration / threshold)
//   - local_static_keys (base58 NodePublic, from operator config)
//   - publisher_lists (per-publisher state including the validators
//     each publisher signs as base58 NodePublic keys, plus per-publisher
//     uri / seq / version / effective / expiration)
//   - trusted_validator_keys (base58 NodePublic)
//   - signing_keys (master → signing, base58→base58)
//   - NegativeUNL (base58 NodePublic, omitted when empty)
//
// When the publisher-trust subsystem is not wired (standalone, or no
// validator_list_keys configured), publisher_lists is empty but the
// other fields still surface real state pulled from the static config /
// adaptor.
type ValidatorsMethod struct{ AdminHandler }

func (m *ValidatorsMethod) Handle(ctx *types.RPCContext, _ json.RawMessage) (any, *types.RPCError) {
	var services *types.ServiceContainer
	if ctx != nil {
		services = ctx.Services
	}
	listSnapshot := resolveValidatorListSnapshot(services, time.Now())
	publisherLists := []map[string]any{}
	trustedKeys := []string{}
	signingKeys := map[string]any{}
	negativeUNL := []string{}

	if ctx != nil && ctx.Services != nil {
		if vl := ctx.Services.ValidatorList; vl != nil {
			for _, p := range listSnapshot.publishers {
				entry := map[string]any{
					"pubkey_publisher": p.PublicKeyHex,
					"available":        p.Available,
					"uri":              p.SiteURI,
					"list":             nonNilStrings(p.ValidatorsBase58),
				}
				// Mirrors rippled ValidatorList.cpp:1676-1696: `seq` and
				// `version` are both gated on the publisher having an
				// accepted list (i.e. current.validUntil set), not on
				// whether the value itself is non-zero. The signal is
				// "has the publisher delivered yet", consistent across
				// the two fields.
				if p.ExpirationUnix > 0 {
					entry["seq"] = p.Sequence
					entry["version"] = p.Version
				}
				// `effective` only emitted when the blob carried the field
				// (rippled gates on `validFrom != TimeKeeper::time_point{}`
				// at ValidatorList.cpp:1682; the EffectiveSet sentinel is
				// the Go-side equivalent — see ValidatorListPublisherInfo).
				if p.EffectiveSet && p.EffectiveISO != "" {
					entry["effective"] = p.EffectiveISO
				}
				if p.ExpirationISO != "" {
					entry["expiration"] = p.ExpirationISO
				}
				// Mirrors rippled ValidatorList.cpp:1699-1713 — emit a
				// `remaining` array of future-dated rotations, omitted
				// when empty.
				if len(p.Remaining) > 0 {
					rem := make([]map[string]any, 0, len(p.Remaining))
					for _, r := range p.Remaining {
						// Mirrors rippled appendList at
						// ValidatorList.cpp:1673-1689 which emits
						// uri/seq/expiration/effective/list for every
						// entry. `version` lives on the top-level publisher
						// object (line 1695) and is NOT repeated per
						// remaining entry — keep parity by omitting it here.
						re := map[string]any{
							"uri":  r.SiteURI,
							"list": nonNilStrings(r.ValidatorsBase58),
							"seq":  r.Sequence,
						}
						if r.EffectiveSet && r.EffectiveISO != "" {
							re["effective"] = r.EffectiveISO
						}
						if r.ExpirationISO != "" {
							re["expiration"] = r.ExpirationISO
						}
						rem = append(rem, re)
					}
					entry["remaining"] = rem
				}
				publisherLists = append(publisherLists, entry)
			}
		}
		if fn := ctx.Services.TrustedValidatorKeysBase58; fn != nil {
			trustedKeys = nonNilStrings(fn())
		} else if vl := ctx.Services.ValidatorList; vl != nil {
			for _, mk := range vl.TrustedMasterKeys() {
				if enc, err := addresscodec.EncodeNodePublicKey(mk[:]); err == nil {
					trustedKeys = append(trustedKeys, enc)
				}
			}
		}
		if fn := ctx.Services.SigningKeysBase58; fn != nil {
			for master, signing := range fn() {
				signingKeys[master] = signing
			}
		}
		if fn := ctx.Services.NegativeUNLBase58; fn != nil {
			negativeUNL = nonNilStrings(fn())
		}
	}

	quorum := 0
	if ctx != nil && ctx.Services != nil && ctx.Services.ValidationQuorum != nil {
		quorum = ctx.Services.ValidationQuorum()
	}

	validatorListSummary := listSnapshot.summary
	validatorListSummary["validator_list_threshold"] = listSnapshot.threshold

	resp := map[string]any{
		"trusted_validator_keys": trustedKeys,
		"publisher_lists":        publisherLists,
		"validation_quorum":      quorum,
		"validator_list":         validatorListSummary,
		"local_static_keys":      listSnapshot.localStatic,
		"signing_keys":           signingKeys,
	}
	if len(negativeUNL) > 0 {
		resp["NegativeUNL"] = negativeUNL
	}
	return resp, nil
}

type validatorListSnapshot struct {
	publishers  []types.ValidatorListPublisherInfo
	localStatic []string
	threshold   int
	summary     map[string]any
	expires     uint32
}

func resolveValidatorListSnapshot(services *types.ServiceContainer, now time.Time) validatorListSnapshot {
	snapshot := validatorListSnapshot{localStatic: []string{}}
	if services == nil {
		snapshot.summary = map[string]any{"count": 0, "status": "unknown", "expiration": "unknown"}
		return snapshot
	}
	if services.LocalStaticTrustedKeysBase58 != nil {
		snapshot.localStatic = nonNilStrings(services.LocalStaticTrustedKeysBase58())
	}

	publisherCount := 0
	if services.ValidatorList != nil {
		publisherCount = services.ValidatorList.PublisherCount()
		snapshot.threshold = services.ValidatorList.Threshold()
		snapshot.publishers = services.ValidatorList.Publishers()
	}

	listCount := publisherCount
	if len(snapshot.localStatic) > 0 {
		listCount++
	}
	snapshot.summary = map[string]any{"count": listCount}

	var earliestExpirationUnix int64
	missingExpiration := len(snapshot.publishers) < publisherCount
	for _, publisher := range snapshot.publishers {
		chainedExpiration := publisher.ExpirationUnix
		for _, remaining := range publisher.Remaining {
			if chainedExpiration == 0 || remaining.EffectiveUnix == 0 || remaining.ExpirationUnix == 0 || remaining.EffectiveUnix > chainedExpiration {
				break
			}
			chainedExpiration = remaining.ExpirationUnix
		}
		if chainedExpiration == 0 {
			missingExpiration = true
			continue
		}
		if earliestExpirationUnix == 0 || chainedExpiration < earliestExpirationUnix {
			earliestExpirationUnix = chainedExpiration
		}
	}

	switch {
	case publisherCount == 0 && len(snapshot.localStatic) > 0:
		snapshot.summary["status"] = "active"
		snapshot.summary["expiration"] = "never"
		snapshot.expires = ^uint32(0)
	case publisherCount == 0 || missingExpiration || earliestExpirationUnix == 0:
		snapshot.summary["status"] = "unknown"
		snapshot.summary["expiration"] = "unknown"
	default:
		expiry := time.Unix(earliestExpirationUnix, 0)
		snapshot.summary["expiration"] = formatRippledTime(expiry)
		if expiry.After(now) {
			snapshot.summary["status"] = "active"
		} else {
			snapshot.summary["status"] = "expired"
		}
		rippleSeconds := earliestExpirationUnix - protocol.RippleEpochUnix
		if rippleSeconds > 0 && rippleSeconds <= int64(^uint32(0)) {
			snapshot.expires = uint32(rippleSeconds)
		}
	}
	return snapshot
}

// ValidatorListSitesMethod handles the `validator_list_sites` admin
// RPC. Mirrors rippled's ValidatorSite::getJson at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:672-705.
type ValidatorListSitesMethod struct{ AdminHandler }

func (m *ValidatorListSitesMethod) Handle(ctx *types.RPCContext, _ json.RawMessage) (any, *types.RPCError) {
	sites := []map[string]any{}

	if ctx != nil && ctx.Services != nil && ctx.Services.ValidatorList != nil {
		for _, s := range ctx.Services.ValidatorList.Sites() {
			entry := map[string]any{
				"uri":                  s.URI,
				"refresh_interval_min": s.RefreshIntervalMin,
			}
			// Mirrors rippled's `if (site.lastRefreshStatus)` gate at
			// ValidatorSite.cpp:690-697 — last_refresh_time,
			// last_refresh_status, and last_refresh_message share a
			// single condition: they appear together once the first
			// fetch attempt completes, or are all absent.
			if s.LastDispositionSet {
				entry["last_refresh_time"] = s.LastRefreshISO
				entry["last_refresh_status"] = s.LastDisposition
				if s.LastError != "" {
					entry["last_refresh_message"] = s.LastError
				}
			}
			// next_refresh_time is emitted unconditionally to match
			// rippled ValidatorSite.cpp:689 (`to_string(site.nextRefresh)`,
			// no opt gate). Sites are constructed with nextRefresh set to
			// the construction clock so the field is never empty after
			// startup.
			entry["next_refresh_time"] = s.NextRefreshISO
			sites = append(sites, entry)
		}
	}

	return map[string]any{"validator_sites": sites}, nil
}

// nonNilStrings returns an empty []string instead of nil so JSON
// serialization yields `[]` rather than `null`.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// rippledTimeLayout matches rippled's to_string(NetClock::time_point)
// at rippled/include/xrpl/basics/chrono.h:75-88 — `date::format("%Y-%b-%d %T %Z", tp)`
// which produces strings like `"2026-May-18 10:30:00 UTC"`. Use for
// the `expiration` / `effective` / `last_refresh_time` / `next_refresh_time`
// fields exposed by the validators and validator_list_sites RPCs.
const rippledTimeLayout = "2006-Jan-02 15:04:05 UTC"

func formatRippledTime(t time.Time) string {
	return t.UTC().Format(rippledTimeLayout)
}

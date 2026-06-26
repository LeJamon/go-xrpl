package handlers

import (
	"encoding/json"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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

func (m *ValidatorsMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
	publisherLists := []map[string]any{}
	trustedKeys := []string{}
	signingKeys := map[string]any{}
	localStatic := []string{}
	negativeUNL := []string{}

	var earliestExpirationUnix int64
	anyMissingExpiration := false
	publisherCount := 0
	threshold := 0

	if ctx != nil && ctx.Services != nil {
		if vl := ctx.Services.ValidatorList; vl != nil {
			publisherCount = vl.PublisherCount()
			threshold = vl.Threshold()
			for _, p := range vl.Publishers() {
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
				// Chained-extension walk for `expires()` (rippled
				// ValidatorList.cpp:1560-1607): if a `remaining` entry's
				// validFrom <= the chained validUntil, extend the chain
				// to that entry's validUntil. The `validator_list`
				// summary at the end uses earliestExpirationUnix.
				chainedExp := p.ExpirationUnix
				if chainedExp > 0 && len(p.Remaining) > 0 {
					// p.Remaining is already sorted by sequence by the
					// adapter; effective times within a single publisher's
					// queue are monotonic by construction (validFrom <
					// next validFrom for a rotation chain).
					for _, r := range p.Remaining {
						if r.EffectiveUnix == 0 || r.ExpirationUnix == 0 {
							break
						}
						if r.EffectiveUnix > chainedExp {
							break
						}
						chainedExp = r.ExpirationUnix
					}
				}
				if chainedExp > 0 {
					if earliestExpirationUnix == 0 || chainedExp < earliestExpirationUnix {
						earliestExpirationUnix = chainedExp
					}
				} else {
					anyMissingExpiration = true
				}
				publisherLists = append(publisherLists, entry)
			}
			for _, mk := range vl.TrustedMasterKeys() {
				if enc, err := addresscodec.EncodeNodePublicKey(mk[:]); err == nil {
					trustedKeys = append(trustedKeys, enc)
				}
			}
		}
		if fn := ctx.Services.LocalStaticTrustedKeysBase58; fn != nil {
			localStatic = nonNilStrings(fn())
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

	// Match rippled ValidatorList::count (ValidatorList.cpp:1547-1551):
	// publisherLists_.size() + (localPublisherList non-empty ? 1 : 0).
	// The non-empty local static stanza counts as a single source on top
	// of the publisher set.
	listCount := publisherCount
	if len(localStatic) > 0 {
		listCount++
	}

	validatorListSummary := map[string]any{
		"count":                    listCount,
		"validator_list_threshold": threshold,
	}
	// Status / expiration gating mirrors rippled's expires() +
	// getJson (ValidatorList.cpp:1560-1651). Three cases:
	//
	//  1. No publishers configured but the local [validators] stanza
	//     is populated — rippled's expires() returns the local
	//     validUntil which is TimeKeeper::time_point::max(); getJson
	//     emits status="active" expiration="never".
	//  2. Any publisher's current.validUntil is unset (unfetched) — or
	//     no source of trust at all — emit status="unknown".
	//  3. Otherwise emit the earliest publisher validUntil; status is
	//     "expired" when any publisher is expired/unavailable, else
	//     "active".
	switch {
	case publisherCount == 0 && len(localStatic) > 0:
		validatorListSummary["status"] = "active"
		validatorListSummary["expiration"] = "never"
	case publisherCount == 0 || anyMissingExpiration || earliestExpirationUnix == 0:
		validatorListSummary["status"] = "unknown"
		validatorListSummary["expiration"] = "unknown"
	default:
		expiry := time.Unix(earliestExpirationUnix, 0)
		validatorListSummary["expiration"] = formatRippledTime(expiry)
		// Mirrors rippled ValidatorList.cpp:1641-1644 — status is a pure
		// timestamp comparison of the earliest validUntil against
		// wall-clock now, NOT a join over publisher Status/Available
		// signals. Those latter signals already feed `available` /
		// `expiration` per publisher; mixing them in here would break
		// monitors that key on `validator_list.status`.
		if time.Now().After(expiry) {
			validatorListSummary["status"] = "expired"
		} else {
			validatorListSummary["status"] = "active"
		}
	}

	resp := map[string]any{
		"trusted_validator_keys": trustedKeys,
		"publisher_lists":        publisherLists,
		"validation_quorum":      quorum,
		"validator_list":         validatorListSummary,
		"local_static_keys":      localStatic,
		"signing_keys":           signingKeys,
	}
	if len(negativeUNL) > 0 {
		resp["NegativeUNL"] = negativeUNL
	}
	return resp, nil
}

// ValidatorListSitesMethod handles the `validator_list_sites` admin
// RPC. Mirrors rippled's ValidatorSite::getJson at
// rippled/src/xrpld/app/misc/detail/ValidatorSite.cpp:672-705.
type ValidatorListSitesMethod struct{ AdminHandler }

func (m *ValidatorListSitesMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
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

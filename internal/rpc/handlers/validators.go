package handlers

import (
	"encoding/json"
	"time"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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

func (m *ValidatorsMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
	publisherLists := []map[string]interface{}{}
	trustedKeys := []string{}
	signingKeys := map[string]interface{}{}
	localStatic := []string{}
	negativeUNL := []string{}

	var earliestExpirationUnix int64
	anyExpired := false
	allAvailable := true
	anyMissingExpiration := false
	publisherCount := 0
	threshold := 0

	if ctx != nil && ctx.Services != nil {
		if vl := ctx.Services.ValidatorList; vl != nil {
			publisherCount = vl.PublisherCount()
			threshold = vl.Threshold()
			for _, p := range vl.Publishers() {
				entry := map[string]interface{}{
					"pubkey_publisher": p.PublicKeyHex,
					"available":        p.Available,
					"uri":              p.SiteURI,
					"list":             nonNilStrings(p.ValidatorsBase58),
				}
				if p.Sequence > 0 {
					entry["seq"] = p.Sequence
				}
				// Mirrors rippled ValidatorList.cpp:1693-1696: `version`
				// is only emitted when the publisher's current.validUntil
				// is set (i.e. an accepted/expired list has been ingested).
				if p.ExpirationUnix > 0 && p.Version > 0 {
					entry["version"] = p.Version
				}
				if p.EffectiveISO != "" {
					entry["effective"] = p.EffectiveISO
				}
				if p.ExpirationISO != "" {
					entry["expiration"] = p.ExpirationISO
				}
				if p.ExpirationUnix > 0 {
					if earliestExpirationUnix == 0 || p.ExpirationUnix < earliestExpirationUnix {
						earliestExpirationUnix = p.ExpirationUnix
					}
				} else {
					anyMissingExpiration = true
				}
				if !p.Available {
					allAvailable = false
				}
				if p.Status == "expired" {
					anyExpired = true
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

	validatorListSummary := map[string]interface{}{
		"count":                    listCount,
		"validator_list_threshold": threshold,
	}
	// "unknown" gating mirrors rippled's expires() (ValidatorList.cpp:1560-1591):
	// if any publisher's current.validUntil is unset the whole summary
	// reports unknown. Tracked via anyMissingExpiration so a partial fetch
	// can't paint over an unfetched publisher.
	if publisherCount == 0 || anyMissingExpiration || earliestExpirationUnix == 0 {
		validatorListSummary["status"] = "unknown"
		validatorListSummary["expiration"] = "unknown"
	} else {
		validatorListSummary["expiration"] = formatRippledTime(time.Unix(earliestExpirationUnix, 0))
		if anyExpired || !allAvailable {
			validatorListSummary["status"] = "expired"
		} else {
			validatorListSummary["status"] = "active"
		}
	}

	resp := map[string]interface{}{
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

func (m *ValidatorListSitesMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
	sites := []map[string]interface{}{}

	if ctx != nil && ctx.Services != nil && ctx.Services.ValidatorList != nil {
		for _, s := range ctx.Services.ValidatorList.Sites() {
			entry := map[string]interface{}{
				"uri":                  s.URI,
				"refresh_interval_min": s.RefreshIntervalMin,
			}
			// Mirrors rippled's `if (site.lastRefreshStatus)` gate at
			// ValidatorSite.cpp:690 — the field is absent from the
			// response until the first fetch attempt completes.
			if s.LastDispositionSet {
				entry["last_refresh_status"] = s.LastDisposition
			}
			if s.LastRefreshISO != "" {
				entry["last_refresh_time"] = s.LastRefreshISO
			}
			if s.NextRefreshISO != "" {
				entry["next_refresh_time"] = s.NextRefreshISO
			}
			if s.LastError != "" {
				entry["last_refresh_message"] = s.LastError
			}
			sites = append(sites, entry)
		}
	}

	return map[string]interface{}{"validator_sites": sites}, nil
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

// formatRippledTime renders t in rippled's boost-style timestamp format.
func formatRippledTime(t time.Time) string {
	return t.UTC().Format(rippledTimeLayout)
}

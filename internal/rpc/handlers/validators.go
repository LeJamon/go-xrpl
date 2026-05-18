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
					"status":           p.Status,
					"uri":              p.SiteURI,
					"list":             nonNilStrings(p.ValidatorsBase58),
				}
				if p.Sequence > 0 {
					entry["seq"] = p.Sequence
				}
				if p.Version > 0 {
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

	validatorListSummary := map[string]interface{}{
		"count":                    publisherCount,
		"validator_list_threshold": threshold,
	}
	if publisherCount == 0 || earliestExpirationUnix == 0 {
		validatorListSummary["status"] = "unknown"
		validatorListSummary["expiration"] = "unknown"
	} else {
		validatorListSummary["expiration"] = time.Unix(earliestExpirationUnix, 0).UTC().Format(time.RFC3339)
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
			if s.LastDisposition != "" {
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

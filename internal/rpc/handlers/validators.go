package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ValidatorsMethod handles the `validators` admin RPC. Returns the
// trusted UNL (master pubkeys) plus per-publisher metadata. Mirrors
// rippled's Validators handler (Validators.cpp) which surfaces
// app.validators() state to operators.
//
// When the publisher-trust subsystem isn't wired (standalone mode or
// no validator_list_keys configured) the response still includes
// validation_quorum and trusted_validator_keys derived from the static
// adaptor UNL — only the publisher_lists array is empty.
type ValidatorsMethod struct{ AdminHandler }

func (m *ValidatorsMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
	publisherLists := []map[string]interface{}{}
	trustedKeys := []string{}

	if ctx != nil && ctx.Services != nil && ctx.Services.ValidatorList != nil {
		vl := ctx.Services.ValidatorList
		for _, p := range vl.Publishers() {
			entry := map[string]interface{}{
				"public_key":      p.PublicKey,
				"available":       p.Status == "available",
				"status":          p.Status,
				"seq":             p.Sequence,
				"validator_count": p.ValidatorCount,
				"site":            p.SiteURI,
			}
			if p.EffectiveUnix > 0 {
				entry["effective"] = p.EffectiveUnix
			}
			if p.ExpirationUnix > 0 {
				entry["expiration"] = p.ExpirationUnix
			}
			publisherLists = append(publisherLists, entry)
		}
		for _, mk := range vl.TrustedMasterKeys() {
			trustedKeys = append(trustedKeys, strings.ToUpper(hex.EncodeToString(mk[:])))
		}
	}

	quorum := 0
	if ctx != nil && ctx.Services != nil && ctx.Services.ValidationQuorum != nil {
		quorum = ctx.Services.ValidationQuorum()
	}

	return map[string]interface{}{
		"trusted_validator_keys": trustedKeys,
		"publisher_lists":        publisherLists,
		"validation_quorum":      quorum,
	}, nil
}

// ValidatorListSitesMethod handles the `validator_list_sites` admin
// RPC. Returns the configured publisher URLs along with the most
// recent fetch status. Mirrors rippled's ValidatorListSites handler
// (ValidatorListSites.cpp).
type ValidatorListSitesMethod struct{ AdminHandler }

func (m *ValidatorListSitesMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
	sites := []map[string]interface{}{}

	if ctx != nil && ctx.Services != nil && ctx.Services.ValidatorList != nil {
		for _, s := range ctx.Services.ValidatorList.Sites() {
			entry := map[string]interface{}{
				"uri":                  s.URI,
				"last_disposition":     s.LastDisposition,
				"refresh_interval_sec": s.RefreshIntervalSec,
			}
			if s.LastRefreshUnix > 0 {
				entry["last_refresh"] = s.LastRefreshUnix
			}
			if s.LastSuccessUnix > 0 {
				entry["last_success"] = s.LastSuccessUnix
			}
			if s.LastError != "" {
				entry["last_error"] = s.LastError
			}
			sites = append(sites, entry)
		}
	}

	return map[string]interface{}{"validator_sites": sites}, nil
}

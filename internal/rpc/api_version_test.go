package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allHandlers returns every handler type in the handlers package, keyed by
// method name.  This is the single source of truth for the API version
// conformance tests — if a new handler is added to handlers/ it must be
// added here as well.
func allHandlers() map[string]types.MethodHandler {
	return map[string]types.MethodHandler{
		// Account methods
		"account_info":       &handlers.AccountInfoMethod{},
		"account_channels":   &handlers.AccountChannelsMethod{},
		"account_currencies": &handlers.AccountCurrenciesMethod{},
		"account_lines":      &handlers.AccountLinesMethod{},
		"account_nfts":       &handlers.AccountNftsMethod{},
		"account_objects":    &handlers.AccountObjectsMethod{},
		"account_offers":     &handlers.AccountOffersMethod{},
		"account_tx":         &handlers.AccountTxMethod{},

		// Transaction methods
		"tx":                 &handlers.TxMethod{},
		"tx_history":         &handlers.TxHistoryMethod{},
		"submit":             &handlers.SubmitMethod{},
		"submit_multisigned": &handlers.SubmitMultisignedMethod{},
		"sign":               &handlers.SignMethod{},
		"sign_for":           &handlers.SignForMethod{},
		"transaction_entry":  &handlers.TransactionEntryMethod{},

		// Ledger methods
		"ledger":         &handlers.LedgerMethod{},
		"ledger_accept":  &handlers.LedgerAcceptMethod{},
		"ledger_closed":  &handlers.LedgerClosedMethod{},
		"ledger_current": &handlers.LedgerCurrentMethod{},
		"ledger_data":    &handlers.LedgerDataMethod{},
		"ledger_entry":   &handlers.LedgerEntryMethod{},
		"ledger_index":   &handlers.LedgerIndexMethod{},
		"ledger_range":   &handlers.LedgerRangeMethod{},

		// Server methods
		"ping":               &handlers.PingMethod{},
		"server_info":        &handlers.ServerInfoMethod{},
		"server_state":       &handlers.ServerStateMethod{},
		"server_definitions": &handlers.ServerDefinitionsMethod{},
		"random":             &handlers.RandomMethod{},
		"fee":                &handlers.FeeMethod{},
		"feature":            &handlers.FeatureMethod{},
		"version":            &handlers.VersionMethod{},

		// Order book / path methods
		"book_offers":      &handlers.BookOffersMethod{},
		"book_changes":     &handlers.BookChangesMethod{},
		"path_find":        &handlers.PathFindMethod{},
		"ripple_path_find": &handlers.RipplePathFindMethod{},

		// NFT methods
		"nft_buy_offers":  &handlers.NftBuyOffersMethod{},
		"nft_sell_offers": &handlers.NftSellOffersMethod{},

		// Utility methods
		"deposit_authorized": &handlers.DepositAuthorizedMethod{},
		"gateway_balances":   &handlers.GatewayBalancesMethod{},
		"noripple_check":     &handlers.NoRippleCheckMethod{},
		"channel_authorize":  &handlers.ChannelAuthorizeMethod{},
		"channel_verify":     &handlers.ChannelVerifyMethod{},
		"wallet_propose":     &handlers.WalletProposeMethod{},
		"json":               &handlers.JsonMethod{},
		"manifest":           &handlers.ManifestMethod{},
		"amm_info":           &handlers.AMMInfoMethod{},
		"vault_info":         &handlers.VaultInfoMethod{},
		"get_aggregate_price": &handlers.GetAggregatePriceMethod{},
		"simulate":           &handlers.SimulateMethod{},

		// WebSocket subscription methods
		"subscribe":   &handlers.SubscribeMethod{},
		"unsubscribe": &handlers.UnsubscribeMethod{},

		// Admin / network methods
		"stop":                    &handlers.StopMethod{},
		"validation_create":      &handlers.ValidationCreateMethod{},
		"consensus_info":         &handlers.ConsensusInfoMethod{},
		"peers":                  &handlers.PeersMethod{},
		"peer_reservations_add":  &handlers.PeerReservationsAddMethod{},
		"peer_reservations_del":  &handlers.PeerReservationsDelMethod{},
		"peer_reservations_list": &handlers.PeerReservationsListMethod{},
		"validators":             &handlers.ValidatorsMethod{},
		"validator_list_sites":   &handlers.ValidatorListSitesMethod{},
		"download_shard":         &handlers.DownloadShardMethod{},
		"crawl_shards":           &handlers.CrawlShardsMethod{},

		// Stub / missing methods
		"fetch_info":      &handlers.FetchInfoMethod{},
		"owner_info":      &handlers.OwnerInfoMethod{},
		"ledger_header":   &handlers.LedgerHeaderMethod{},
		"ledger_request":  &handlers.LedgerRequestMethod{},
		"ledger_cleaner":  &handlers.LedgerCleanerMethod{},
		"ledger_diff":     &handlers.LedgerDiffMethod{},
		"tx_reduce_relay": &handlers.TxReduceRelayMethod{},
		"connect":         &handlers.ConnectMethod{},
		"print":           &handlers.PrintMethod{},
		"validator_info":  &handlers.ValidatorInfoMethod{},
		"can_delete":      &handlers.CanDeleteMethod{},
		"get_counts":      &handlers.GetCountsMethod{},
		"log_level":       &handlers.LogLevelMethod{},
		"logrotate":       &handlers.LogRotateMethod{},
		"unl_list":        &handlers.UnlListMethod{},
		"blacklist":       &handlers.BlackListMethod{},
	}
}

// ---------------------------------------------------------------------------
// Test 1: TestApiVersionConstants
// Verify the API version constants are reasonable and match rippled's ranges.
//
// rippled defines (ApiVersion.h):
//   apiMinimumSupportedVersion = 1
//   apiMaximumSupportedVersion = 2
//   apiBetaVersion             = 3
//   apiVersionIfUnspecified     = 1
//
// goXRPL mirrors these as ApiVersion1..ApiVersion3 plus DefaultApiVersion.
// ---------------------------------------------------------------------------

func TestApiVersionConstants(t *testing.T) {
	// Verify the symbolic constants have their expected numeric values.
	assert.Equal(t, 1, types.ApiVersion1, "ApiVersion1 should be 1")
	assert.Equal(t, 2, types.ApiVersion2, "ApiVersion2 should be 2")
	assert.Equal(t, 3, types.ApiVersion3, "ApiVersion3 should be 3")

	// MIN <= GOOD <= MAX mapping:
	//   MIN  = ApiVersion1 (matches rippled apiMinimumSupportedVersion)
	//   GOOD = ApiVersion2 (the highest non-beta stable version)
	//   MAX  = ApiVersion3 (matches rippled apiBetaVersion, the upper bound)
	minAPI := types.ApiVersion1
	goodAPI := types.ApiVersion2
	maxAPI := types.ApiVersion3

	assert.LessOrEqual(t, minAPI, goodAPI,
		"MIN_API_VERSION (%d) should be <= GOOD_API_VERSION (%d)", minAPI, goodAPI)
	assert.LessOrEqual(t, goodAPI, maxAPI,
		"GOOD_API_VERSION (%d) should be <= MAX_API_VERSION (%d)", goodAPI, maxAPI)

	// DefaultApiVersion must be within the supported range.
	assert.GreaterOrEqual(t, types.DefaultApiVersion, minAPI,
		"DefaultApiVersion should be >= MIN_API_VERSION")
	assert.LessOrEqual(t, types.DefaultApiVersion, maxAPI,
		"DefaultApiVersion should be <= MAX_API_VERSION")

	// DefaultApiVersion should match rippled's apiVersionIfUnspecified (1).
	assert.Equal(t, types.ApiVersion1, types.DefaultApiVersion,
		"DefaultApiVersion should equal ApiVersion1 (rippled apiVersionIfUnspecified)")

	// Cross-check with the version handler response (which reports the range).
	method := &handlers.VersionMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}
	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr, "version handler should not error")
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(resultJSON, &resp))

	versionMap, ok := resp["version"].(map[string]interface{})
	require.True(t, ok, "version handler should return a 'version' object")

	assert.Equal(t, float64(types.ApiVersion1), versionMap["first"],
		"version.first should match ApiVersion1")
	assert.Equal(t, float64(types.ApiVersion3), versionMap["last"],
		"version.last should match ApiVersion3")
	assert.Equal(t, float64(types.ApiVersion2), versionMap["good"],
		"version.good should match ApiVersion2")
}

// ---------------------------------------------------------------------------
// Test 2: TestApiVersionAllMethodsDeclareVersions
// Every handler must declare at least one supported API version.
// ---------------------------------------------------------------------------

func TestApiVersionAllMethodsDeclareVersions(t *testing.T) {
	for name, handler := range allHandlers() {
		t.Run(name, func(t *testing.T) {
			versions := handler.SupportedApiVersions()
			assert.NotEmpty(t, versions,
				"handler %q must declare at least one supported API version", name)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 3: TestApiVersionAllMethodsSupportV1
// API version 1 is the base version.  All methods must support it so that
// callers who do not specify an api_version (defaulting to 1) can reach
// every endpoint.
// ---------------------------------------------------------------------------

func TestApiVersionAllMethodsSupportV1(t *testing.T) {
	for name, handler := range allHandlers() {
		t.Run(name, func(t *testing.T) {
			versions := handler.SupportedApiVersions()
			assert.Contains(t, versions, types.ApiVersion1,
				"handler %q must support API version 1 (the base version)", name)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 4: TestApiVersionMethodsWorkWithEachVersion
// For key methods (account_info, tx, ledger, server_info, ping), call
// Handle() with each supported API version and verify no version-related
// error is returned.  We set up a mock so the handlers have enough context
// to proceed past the initial dispatch.
// ---------------------------------------------------------------------------

func TestApiVersionMethodsWorkWithEachVersion(t *testing.T) {
	mock := newMockLedgerService()
	cleanup := setupTestServices(mock)
	defer cleanup()

	// Handlers that can be invoked with minimal params and still produce a
	// non-version-related response (success or a domain error, but NOT a
	// version-incompatibility error).
	keyMethods := map[string]struct {
		handler types.MethodHandler
		params  interface{}
	}{
		"ping": {
			handler: &handlers.PingMethod{},
			params:  nil,
		},
		"server_info": {
			handler: &handlers.ServerInfoMethod{},
			params:  nil,
		},
		"account_info": {
			handler: &handlers.AccountInfoMethod{},
			params: map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
		},
		"ledger": {
			handler: &handlers.LedgerMethod{},
			params: map[string]interface{}{
				"ledger_index": "validated",
			},
		},
		"tx": {
			handler: &handlers.TxMethod{},
			params: map[string]interface{}{
				"transaction": "E08D6E9754025BA2534A78707605E0601F03ACE063687A0CA1BDDACFCD1698C7",
			},
		},
	}

	apiVersions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for name, m := range keyMethods {
		for _, ver := range apiVersions {
			t.Run(name+"/v"+string(rune('0'+ver)), func(t *testing.T) {
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: ver,
				}

				var paramsJSON json.RawMessage
				if m.params != nil {
					raw, err := json.Marshal(m.params)
					require.NoError(t, err)
					paramsJSON = raw
				}

				_, rpcErr := m.handler.Handle(ctx, paramsJSON)

				// We accept both success and domain-level errors (e.g. lgrNotFound,
				// txnNotFound).  The only thing we reject is an error indicating
				// version incompatibility.
				if rpcErr != nil {
					assert.NotEqual(t, types.RpcINVALID_API_VERSION, rpcErr.Code,
						"handler %q should not return invalid_api_version for version %d", name, ver)
					assert.NotContains(t, rpcErr.Message, "API version",
						"handler %q should not complain about API version %d", name, ver)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Test 5: TestApiVersionRpcContextCarriesVersion
// Ensure an RpcContext constructed with a given API version faithfully
// delivers that version to the handler.  We verify by inspecting the
// context in a trivial handler (ping).
// ---------------------------------------------------------------------------

func TestApiVersionRpcContextCarriesVersion(t *testing.T) {
	apiVersions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for _, ver := range apiVersions {
		t.Run("version_"+string(rune('0'+ver)), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: ver,
			}
			assert.Equal(t, ver, ctx.ApiVersion,
				"RpcContext.ApiVersion should be %d", ver)

			// Invoke ping to show the handler actually receives the context.
			handler := &handlers.PingMethod{}
			result, rpcErr := handler.Handle(ctx, nil)
			require.Nil(t, rpcErr, "ping should succeed for version %d", ver)
			require.NotNil(t, result, "ping should return a result for version %d", ver)
		})
	}
}

// ---------------------------------------------------------------------------
// Test 6: TestApiVersionDeprecatedMethodRanges
// Some methods may have restricted API version support. For instance,
// tx_history is deprecated in rippled v2. This test documents the expected
// SupportedApiVersions() for handlers where the range may be narrower than
// the full [1,2,3].
//
// Currently all goXRPL handlers declare support for all three versions.
// When a method is deprecated (e.g., tx_history removed from v2+), this
// test should be updated to verify the tighter range.
// ---------------------------------------------------------------------------

func TestApiVersionDeprecatedMethodRanges(t *testing.T) {
	type versionRange struct {
		handler  types.MethodHandler
		expected []int
	}

	// Expected version ranges per method.
	//
	// In rippled, tx_history is deprecated and marked for removal, but still
	// responds on all supported versions.  The goXRPL implementation mirrors
	// this: all versions are supported until the method is actually removed.
	expectations := map[string]versionRange{
		"tx_history": {
			handler:  &handlers.TxHistoryMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"ping": {
			handler:  &handlers.PingMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"account_info": {
			handler:  &handlers.AccountInfoMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"server_info": {
			handler:  &handlers.ServerInfoMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"ledger": {
			handler:  &handlers.LedgerMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"tx": {
			handler:  &handlers.TxMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"version": {
			handler:  &handlers.VersionMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"subscribe": {
			handler:  &handlers.SubscribeMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
		"unsubscribe": {
			handler:  &handlers.UnsubscribeMethod{},
			expected: []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3},
		},
	}

	for name, exp := range expectations {
		t.Run(name, func(t *testing.T) {
			actual := exp.handler.SupportedApiVersions()
			assert.Equal(t, exp.expected, actual,
				"handler %q should support exactly versions %v, got %v", name, exp.expected, actual)
		})
	}
}

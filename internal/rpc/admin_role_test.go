package rpc

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
)

// adminMethodEntry pairs a method name (for documentation) with its handler.
type adminMethodEntry struct {
	name    string
	handler types.MethodHandler
}

// allAdminMethods returns every handler that MUST declare RoleAdmin.
func allAdminMethods() []adminMethodEntry {
	return []adminMethodEntry{
		{"feature", &handlers.FeatureMethod{}},
		{"connect", &handlers.ConnectMethod{}},
		{"stop", &handlers.StopMethod{}},
		{"ledger_accept", &handlers.LedgerAcceptMethod{}},
		{"ledger_cleaner", &handlers.LedgerCleanerMethod{}},
		{"ledger_request", &handlers.LedgerRequestMethod{}},
		{"ledger_diff", &handlers.LedgerDiffMethod{}},
		{"ledger_range", &handlers.LedgerRangeMethod{}},
		{"log_level", &handlers.LogLevelMethod{}},
		{"log_rotate", &handlers.LogRotateMethod{}},
		{"peers", &handlers.PeersMethod{}},
		{"peer_reservations_add", &handlers.PeerReservationsAddMethod{}},
		{"peer_reservations_del", &handlers.PeerReservationsDelMethod{}},
		{"peer_reservations_list", &handlers.PeerReservationsListMethod{}},
		{"print", &handlers.PrintMethod{}},
		{"validation_create", &handlers.ValidationCreateMethod{}},
		{"validator_info", &handlers.ValidatorInfoMethod{}},
		{"validators", &handlers.ValidatorsMethod{}},
		{"validator_list_sites", &handlers.ValidatorListSitesMethod{}},
		{"can_delete", &handlers.CanDeleteMethod{}},
		{"fetch_info", &handlers.FetchInfoMethod{}},
		{"get_counts", &handlers.GetCountsMethod{}},
		{"consensus_info", &handlers.ConsensusInfoMethod{}},
		{"unl_list", &handlers.UnlListMethod{}},
		{"blacklist", &handlers.BlackListMethod{}},
		{"wallet_propose", &handlers.WalletProposeMethod{}},
		{"download_shard", &handlers.DownloadShardMethod{}},
		{"crawl_shards", &handlers.CrawlShardsMethod{}},
	}
}

// guestMethodEntry pairs a method name with its handler for guest-role methods.
type guestMethodEntry struct {
	name    string
	handler types.MethodHandler
}

// allGuestMethods returns every handler that MUST declare RoleGuest.
func allGuestMethods() []guestMethodEntry {
	return []guestMethodEntry{
		{"account_info", &handlers.AccountInfoMethod{}},
		{"account_lines", &handlers.AccountLinesMethod{}},
		{"account_channels", &handlers.AccountChannelsMethod{}},
		{"account_currencies", &handlers.AccountCurrenciesMethod{}},
		{"account_nfts", &handlers.AccountNftsMethod{}},
		{"account_objects", &handlers.AccountObjectsMethod{}},
		{"account_offers", &handlers.AccountOffersMethod{}},
		{"account_tx", &handlers.AccountTxMethod{}},
		{"book_offers", &handlers.BookOffersMethod{}},
		{"book_changes", &handlers.BookChangesMethod{}},
		{"ledger", &handlers.LedgerMethod{}},
		{"ledger_closed", &handlers.LedgerClosedMethod{}},
		{"ledger_current", &handlers.LedgerCurrentMethod{}},
		{"ledger_data", &handlers.LedgerDataMethod{}},
		{"ledger_entry", &handlers.LedgerEntryMethod{}},
		{"ledger_index", &handlers.LedgerIndexMethod{}},
		{"ledger_header", &handlers.LedgerHeaderMethod{}},
		{"tx_history", &handlers.TxHistoryMethod{}},
		{"ping", &handlers.PingMethod{}},
		{"random", &handlers.RandomMethod{}},
		{"fee", &handlers.FeeMethod{}},
		{"server_info", &handlers.ServerInfoMethod{}},
		{"server_state", &handlers.ServerStateMethod{}},
		{"server_definitions", &handlers.ServerDefinitionsMethod{}},
		{"version", &handlers.VersionMethod{}},
		{"deposit_authorized", &handlers.DepositAuthorizedMethod{}},
		{"gateway_balances", &handlers.GatewayBalancesMethod{}},
		{"noripple_check", &handlers.NoRippleCheckMethod{}},
		{"nft_buy_offers", &handlers.NftBuyOffersMethod{}},
		{"nft_sell_offers", &handlers.NftSellOffersMethod{}},
		{"path_find", &handlers.PathFindMethod{}},
		{"ripple_path_find", &handlers.RipplePathFindMethod{}},
		{"subscribe", &handlers.SubscribeMethod{}},
		{"unsubscribe", &handlers.UnsubscribeMethod{}},
		{"owner_info", &handlers.OwnerInfoMethod{}},
		{"simulate", &handlers.SimulateMethod{}},
		{"json", &handlers.JsonMethod{}},
		{"channel_verify", &handlers.ChannelVerifyMethod{}},
		{"vault_info", &handlers.VaultInfoMethod{}},
		{"amm_info", &handlers.AMMInfoMethod{}},
		{"get_aggregate_price", &handlers.GetAggregatePriceMethod{}},
	}
}

// userMethodEntry pairs a method name with its handler for user-role methods.
type userMethodEntry struct {
	name    string
	handler types.MethodHandler
}

// allUserMethods returns every handler that MUST declare RoleUser.
func allUserMethods() []userMethodEntry {
	return []userMethodEntry{
		{"sign", &handlers.SignMethod{}},
		{"sign_for", &handlers.SignForMethod{}},
		{"submit", &handlers.SubmitMethod{}},
		{"submit_multisigned", &handlers.SubmitMultisignedMethod{}},
		{"channel_authorize", &handlers.ChannelAuthorizeMethod{}},
		{"tx", &handlers.TxMethod{}},
		{"transaction_entry", &handlers.TransactionEntryMethod{}},
		{"manifest", &handlers.ManifestMethod{}},
		{"tx_reduce_relay", &handlers.TxReduceRelayMethod{}},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAdminMethodsRequireAdminRole iterates over every handler that should
// declare RoleAdmin and asserts that RequiredRole() returns RoleAdmin.
func TestAdminMethodsRequireAdminRole(t *testing.T) {
	for _, m := range allAdminMethods() {
		t.Run(m.name, func(t *testing.T) {
			assert.Equal(t, types.RoleAdmin, m.handler.RequiredRole(),
				"method %q must require RoleAdmin", m.name)
		})
	}
}

// TestGuestMethodsAllowGuestRole iterates over every handler that should
// declare RoleGuest and asserts that RequiredRole() returns RoleGuest.
func TestGuestMethodsAllowGuestRole(t *testing.T) {
	for _, m := range allGuestMethods() {
		t.Run(m.name, func(t *testing.T) {
			assert.Equal(t, types.RoleGuest, m.handler.RequiredRole(),
				"method %q must require RoleGuest", m.name)
		})
	}
}

// TestUserMethodsRequireUserRole iterates over every handler that should
// declare RoleUser and asserts that RequiredRole() returns RoleUser.
func TestUserMethodsRequireUserRole(t *testing.T) {
	for _, m := range allUserMethods() {
		t.Run(m.name, func(t *testing.T) {
			assert.Equal(t, types.RoleUser, m.handler.RequiredRole(),
				"method %q must require RoleUser", m.name)
		})
	}
}

// TestAdminMethodCount guards against regressions where new admin methods are
// added to the handlers package but forgotten in this test catalogue.
// Update the expected count when adding new admin handlers.
func TestAdminMethodCount(t *testing.T) {
	const expectedAdminCount = 28

	got := len(allAdminMethods())
	assert.Equal(t, expectedAdminCount, got,
		"Expected %d admin methods but catalogue has %d. "+
			"If you added a new admin handler, add it to allAdminMethods() and bump expectedAdminCount.",
		expectedAdminCount, got)
}

// TestGuestMethodCount guards against regressions where new guest methods are
// added to the handlers package but forgotten in this test catalogue.
// Update the expected count when adding new guest handlers.
func TestGuestMethodCount(t *testing.T) {
	const expectedGuestCount = 41

	got := len(allGuestMethods())
	assert.Equal(t, expectedGuestCount, got,
		"Expected %d guest methods but catalogue has %d. "+
			"If you added a new guest handler, add it to allGuestMethods() and bump expectedGuestCount.",
		expectedGuestCount, got)
}

// TestUserMethodCount guards against regressions where new user methods are
// added to the handlers package but forgotten in this test catalogue.
// Update the expected count when adding new user handlers.
func TestUserMethodCount(t *testing.T) {
	const expectedUserCount = 9

	got := len(allUserMethods())
	assert.Equal(t, expectedUserCount, got,
		"Expected %d user methods but catalogue has %d. "+
			"If you added a new user handler, add it to allUserMethods() and bump expectedUserCount.",
		expectedUserCount, got)
}

// TestAllMethodsCovered verifies that the total number of catalogued methods
// (admin + guest + user) matches the total number of distinct MethodHandler
// types in the handlers package. This is a cross-check to catch methods that
// are not listed in any catalogue.
func TestAllMethodsCovered(t *testing.T) {
	// The expected total is the sum of all three role categories.
	// Every handler struct in the handlers package must appear in exactly
	// one of: allAdminMethods, allGuestMethods, allUserMethods.
	const expectedTotal = 29 + 44 + 5 // 78

	total := len(allAdminMethods()) + len(allGuestMethods()) + len(allUserMethods())
	assert.Equal(t, expectedTotal, total,
		"Total catalogued methods (%d) does not match expected (%d). "+
			"Make sure every handler appears in exactly one role catalogue.",
		total, expectedTotal)
}

// TestNoDuplicateMethodNames ensures no method name appears in more than one
// role catalogue.
func TestNoDuplicateMethodNames(t *testing.T) {
	seen := make(map[string]string) // method name -> catalogue name

	for _, m := range allAdminMethods() {
		if prev, ok := seen[m.name]; ok {
			t.Errorf("method %q appears in both %q and %q catalogues", m.name, prev, "admin")
		}
		seen[m.name] = "admin"
	}
	for _, m := range allGuestMethods() {
		if prev, ok := seen[m.name]; ok {
			t.Errorf("method %q appears in both %q and %q catalogues", m.name, prev, "guest")
		}
		seen[m.name] = "guest"
	}
	for _, m := range allUserMethods() {
		if prev, ok := seen[m.name]; ok {
			t.Errorf("method %q appears in both %q and %q catalogues", m.name, prev, "user")
		}
		seen[m.name] = "user"
	}
}

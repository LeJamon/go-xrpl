package handlers

import (
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// RegisterAll wires every RPC method handler into the given registry.
// This is the single source of truth shared by the HTTP server, the
// WebSocket server, and the local `xrpld rpc` CLI — previously each
// constructor maintained its own list, which had already drifted
// (ledger_range / version / download_shard / crawl_shards differed
// between paths). Adding a new method here exposes it on every entry
// point at once.
func RegisterAll(registry *types.MethodRegistry) {
	// Server-information methods
	registry.Register("server_info", &ServerInfoMethod{})
	registry.Register("server_state", &ServerStateMethod{})
	registry.Register("ping", &PingMethod{})
	registry.Register("random", &RandomMethod{})
	registry.Register("server_definitions", &ServerDefinitionsMethod{})
	registry.Register("feature", &FeatureMethod{})
	registry.Register("fee", &FeeMethod{})
	registry.Register("version", &VersionMethod{})

	// Ledger
	registry.Register("ledger", &LedgerMethod{})
	registry.Register("ledger_closed", &LedgerClosedMethod{})
	registry.Register("ledger_current", &LedgerCurrentMethod{})
	registry.Register("ledger_data", &LedgerDataMethod{})
	registry.Register("ledger_entry", &LedgerEntryMethod{})
	registry.Register("ledger_range", &LedgerRangeMethod{})
	registry.Register("ledger_header", &LedgerHeaderMethod{})
	registry.Register("ledger_request", &LedgerRequestMethod{})
	registry.Register("ledger_cleaner", &LedgerCleanerMethod{})
	registry.Register("ledger_accept", &LedgerAcceptMethod{})

	// Account
	registry.Register("account_info", &AccountInfoMethod{})
	registry.Register("account_channels", &AccountChannelsMethod{})
	registry.Register("account_currencies", &AccountCurrenciesMethod{})
	registry.Register("account_lines", &AccountLinesMethod{})
	registry.Register("account_nfts", &AccountNftsMethod{})
	registry.Register("account_objects", &AccountObjectsMethod{})
	registry.Register("account_offers", &AccountOffersMethod{})
	registry.Register("account_tx", &AccountTxMethod{})
	registry.Register("gateway_balances", &GatewayBalancesMethod{})
	registry.Register("noripple_check", &NoRippleCheckMethod{})
	registry.Register("owner_info", &OwnerInfoMethod{})

	// Transaction
	registry.Register("tx", &TxMethod{})
	registry.Register("tx_history", &TxHistoryMethod{})
	registry.Register("submit", &SubmitMethod{})
	registry.Register("submit_multisigned", &SubmitMultisignedMethod{})
	registry.Register("sign", &SignMethod{})
	registry.Register("sign_for", &SignForMethod{})
	registry.Register("transaction_entry", &TransactionEntryMethod{})
	registry.Register("simulate", &SimulateMethod{})
	registry.Register("tx_reduce_relay", &TxReduceRelayMethod{})

	// Order book / pathfinding
	registry.Register("book_changes", &BookChangesMethod{})
	registry.Register("book_offers", &BookOffersMethod{})
	registry.Register("path_find", &PathFindMethod{})
	registry.Register("ripple_path_find", &RipplePathFindMethod{})

	// Channels
	registry.Register("channel_authorize", &ChannelAuthorizeMethod{})
	registry.Register("channel_verify", &ChannelVerifyMethod{})

	// Subscriptions (HTTP returns notSupported; WebSocket short-circuits
	// before dispatch).
	registry.Register("subscribe", &SubscribeMethod{})
	registry.Register("unsubscribe", &UnsubscribeMethod{})

	// JSON method proxy
	registry.Register("json", &JsonMethod{})

	// Utility
	registry.Register("wallet_propose", &WalletProposeMethod{})
	registry.Register("deposit_authorized", &DepositAuthorizedMethod{})
	registry.Register("nft_buy_offers", &NftBuyOffersMethod{})
	registry.Register("nft_sell_offers", &NftSellOffersMethod{})

	// Admin
	registry.Register("stop", &StopMethod{})
	registry.Register("validation_create", &ValidationCreateMethod{})
	registry.Register("manifest", &ManifestMethod{})
	registry.Register("peer_reservations_add", &PeerReservationsAddMethod{})
	registry.Register("peer_reservations_del", &PeerReservationsDelMethod{})
	registry.Register("peer_reservations_list", &PeerReservationsListMethod{})
	registry.Register("peers", &PeersMethod{})
	registry.Register("consensus_info", &ConsensusInfoMethod{})
	registry.Register("validator_list_sites", &ValidatorListSitesMethod{})
	registry.Register("validators", &ValidatorsMethod{})
	registry.Register("validator_info", &ValidatorInfoMethod{})
	registry.Register("unl_list", &UnlListMethod{})
	registry.Register("download_shard", &DownloadShardMethod{})
	registry.Register("crawl_shards", &CrawlShardsMethod{})
	registry.Register("can_delete", &CanDeleteMethod{})
	registry.Register("get_counts", &GetCountsMethod{})
	registry.Register("log_level", &LogLevelMethod{})
	registry.Register("logrotate", &LogRotateMethod{})
	registry.Register("blacklist", &BlackListMethod{})
	registry.Register("fetch_info", &FetchInfoMethod{})
	registry.Register("connect", &ConnectMethod{})
	registry.Register("print", &PrintMethod{})

	// Feature-specific
	registry.Register("amm_info", &AMMInfoMethod{})
	registry.Register("vault_info", &VaultInfoMethod{})
	registry.Register("get_aggregate_price", &GetAggregatePriceMethod{})
}

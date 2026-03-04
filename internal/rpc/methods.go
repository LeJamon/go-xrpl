package rpc

import (
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_handlers"
)

// registerAllMethods registers all XRPL RPC methods
// This function is called by NewServer to set up the complete method registry
func (s *Server) registerAllMethods() {
	// Server Information Methods
	s.registry.Register("server_info", &rpc_handlers.ServerInfoMethod{})
	s.registry.Register("server_state", &rpc_handlers.ServerStateMethod{})
	s.registry.Register("ping", &rpc_handlers.PingMethod{})
	s.registry.Register("random", &rpc_handlers.RandomMethod{})
	s.registry.Register("server_definitions", &rpc_handlers.ServerDefinitionsMethod{})
	s.registry.Register("feature", &rpc_handlers.FeatureMethod{})
	s.registry.Register("fee", &rpc_handlers.FeeMethod{})
	s.registry.Register("version", &rpc_handlers.VersionMethod{})

	// Ledger Methods
	s.registry.Register("ledger", &rpc_handlers.LedgerMethod{})
	s.registry.Register("ledger_closed", &rpc_handlers.LedgerClosedMethod{})
	s.registry.Register("ledger_current", &rpc_handlers.LedgerCurrentMethod{})
	s.registry.Register("ledger_data", &rpc_handlers.LedgerDataMethod{})
	s.registry.Register("ledger_entry", &rpc_handlers.LedgerEntryMethod{})

	// Account Methods
	s.registry.Register("account_info", &rpc_handlers.AccountInfoMethod{})
	s.registry.Register("account_channels", &rpc_handlers.AccountChannelsMethod{})
	s.registry.Register("account_currencies", &rpc_handlers.AccountCurrenciesMethod{})
	s.registry.Register("account_lines", &rpc_handlers.AccountLinesMethod{})
	s.registry.Register("account_nfts", &rpc_handlers.AccountNftsMethod{})
	s.registry.Register("account_objects", &rpc_handlers.AccountObjectsMethod{})
	s.registry.Register("account_offers", &rpc_handlers.AccountOffersMethod{})
	s.registry.Register("account_tx", &rpc_handlers.AccountTxMethod{})
	s.registry.Register("gateway_balances", &rpc_handlers.GatewayBalancesMethod{})
	s.registry.Register("noripple_check", &rpc_handlers.NoRippleCheckMethod{})

	// Transaction Methods
	s.registry.Register("tx", &rpc_handlers.TxMethod{})
	s.registry.Register("tx_history", &rpc_handlers.TxHistoryMethod{})
	s.registry.Register("submit", &rpc_handlers.SubmitMethod{})
	s.registry.Register("submit_multisigned", &rpc_handlers.SubmitMultisignedMethod{})
	s.registry.Register("sign", &rpc_handlers.SignMethod{})
	s.registry.Register("sign_for", &rpc_handlers.SignForMethod{})
	s.registry.Register("transaction_entry", &rpc_handlers.TransactionEntryMethod{})

	// Path and Order Book Methods
	s.registry.Register("book_changes", &rpc_handlers.BookChangesMethod{})
	s.registry.Register("book_offers", &rpc_handlers.BookOffersMethod{})
	s.registry.Register("path_find", &rpc_handlers.PathFindMethod{})
	s.registry.Register("ripple_path_find", &rpc_handlers.RipplePathFindMethod{})

	// Channel Methods
	s.registry.Register("channel_authorize", &rpc_handlers.ChannelAuthorizeMethod{})
	s.registry.Register("channel_verify", &rpc_handlers.ChannelVerifyMethod{})

	// Subscription Methods (WebSocket only)
	s.registry.Register("subscribe", &rpc_handlers.SubscribeMethod{})
	s.registry.Register("unsubscribe", &rpc_handlers.UnsubscribeMethod{})

	// JSON method proxy
	s.registry.Register("json", &rpc_handlers.JsonMethod{})

	// Utility Methods
	s.registry.Register("wallet_propose", &rpc_handlers.WalletProposeMethod{})
	s.registry.Register("deposit_authorized", &rpc_handlers.DepositAuthorizedMethod{})
	s.registry.Register("nft_buy_offers", &rpc_handlers.NftBuyOffersMethod{})
	s.registry.Register("nft_sell_offers", &rpc_handlers.NftSellOffersMethod{})

	// Standalone mode methods
	s.registry.Register("ledger_accept", &rpc_handlers.LedgerAcceptMethod{})

	// Admin Methods (require admin role)
	s.registry.Register("stop", &rpc_handlers.StopMethod{})
	s.registry.Register("validation_create", &rpc_handlers.ValidationCreateMethod{})
	s.registry.Register("manifest", &rpc_handlers.ManifestMethod{})
	s.registry.Register("peer_reservations_add", &rpc_handlers.PeerReservationsAddMethod{})
	s.registry.Register("peer_reservations_del", &rpc_handlers.PeerReservationsDelMethod{})
	s.registry.Register("peer_reservations_list", &rpc_handlers.PeerReservationsListMethod{})
	s.registry.Register("peers", &rpc_handlers.PeersMethod{})
	s.registry.Register("consensus_info", &rpc_handlers.ConsensusInfoMethod{})
	s.registry.Register("validator_list_sites", &rpc_handlers.ValidatorListSitesMethod{})
	s.registry.Register("validators", &rpc_handlers.ValidatorsMethod{})

	// =========================================================================
	// Additional Methods (added for rippled compatibility)
	// =========================================================================

	// Server/Network Methods
	s.registry.Register("fetch_info", &rpc_handlers.FetchInfoMethod{})
	s.registry.Register("connect", &rpc_handlers.ConnectMethod{})
	s.registry.Register("print", &rpc_handlers.PrintMethod{})

	// Ledger Methods
	s.registry.Register("ledger_header", &rpc_handlers.LedgerHeaderMethod{})
	s.registry.Register("ledger_request", &rpc_handlers.LedgerRequestMethod{})
	s.registry.Register("ledger_cleaner", &rpc_handlers.LedgerCleanerMethod{})

	// Account Methods
	s.registry.Register("owner_info", &rpc_handlers.OwnerInfoMethod{})

	// Transaction Methods
	s.registry.Register("simulate", &rpc_handlers.SimulateMethod{})
	s.registry.Register("tx_reduce_relay", &rpc_handlers.TxReduceRelayMethod{})

	// Validator Methods
	s.registry.Register("validator_info", &rpc_handlers.ValidatorInfoMethod{})
	s.registry.Register("unl_list", &rpc_handlers.UnlListMethod{})

	// Admin/Operational Methods
	s.registry.Register("can_delete", &rpc_handlers.CanDeleteMethod{})
	s.registry.Register("get_counts", &rpc_handlers.GetCountsMethod{})
	s.registry.Register("log_level", &rpc_handlers.LogLevelMethod{})
	s.registry.Register("logrotate", &rpc_handlers.LogRotateMethod{})
	s.registry.Register("blacklist", &rpc_handlers.BlackListMethod{})

	// Feature-specific Methods (depend on unimplemented ledger entry types)
	s.registry.Register("amm_info", &rpc_handlers.AMMInfoMethod{})
	s.registry.Register("vault_info", &rpc_handlers.VaultInfoMethod{})
	s.registry.Register("get_aggregate_price", &rpc_handlers.GetAggregatePriceMethod{})
}

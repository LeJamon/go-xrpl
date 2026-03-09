package rpc

import (
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
)

// registerAllMethods registers all XRPL RPC methods
// This function is called by NewServer to set up the complete method registry
func (s *Server) registerAllMethods() {
	// Server Information Methods
	s.registry.Register("server_info", &handlers.ServerInfoMethod{})
	s.registry.Register("server_state", &handlers.ServerStateMethod{})
	s.registry.Register("ping", &handlers.PingMethod{})
	s.registry.Register("random", &handlers.RandomMethod{})
	s.registry.Register("server_definitions", &handlers.ServerDefinitionsMethod{})
	s.registry.Register("feature", &handlers.FeatureMethod{})
	s.registry.Register("fee", &handlers.FeeMethod{})
	s.registry.Register("version", &handlers.VersionMethod{})

	// Ledger Methods
	s.registry.Register("ledger", &handlers.LedgerMethod{})
	s.registry.Register("ledger_closed", &handlers.LedgerClosedMethod{})
	s.registry.Register("ledger_current", &handlers.LedgerCurrentMethod{})
	s.registry.Register("ledger_data", &handlers.LedgerDataMethod{})
	s.registry.Register("ledger_entry", &handlers.LedgerEntryMethod{})

	// Account Methods
	s.registry.Register("account_info", &handlers.AccountInfoMethod{})
	s.registry.Register("account_channels", &handlers.AccountChannelsMethod{})
	s.registry.Register("account_currencies", &handlers.AccountCurrenciesMethod{})
	s.registry.Register("account_lines", &handlers.AccountLinesMethod{})
	s.registry.Register("account_nfts", &handlers.AccountNftsMethod{})
	s.registry.Register("account_objects", &handlers.AccountObjectsMethod{})
	s.registry.Register("account_offers", &handlers.AccountOffersMethod{})
	s.registry.Register("account_tx", &handlers.AccountTxMethod{})
	s.registry.Register("gateway_balances", &handlers.GatewayBalancesMethod{})
	s.registry.Register("noripple_check", &handlers.NoRippleCheckMethod{})

	// Transaction Methods
	s.registry.Register("tx", &handlers.TxMethod{})
	s.registry.Register("tx_history", &handlers.TxHistoryMethod{})
	s.registry.Register("submit", &handlers.SubmitMethod{})
	s.registry.Register("submit_multisigned", &handlers.SubmitMultisignedMethod{})
	s.registry.Register("sign", &handlers.SignMethod{})
	s.registry.Register("sign_for", &handlers.SignForMethod{})
	s.registry.Register("transaction_entry", &handlers.TransactionEntryMethod{})

	// Path and Order Book Methods
	s.registry.Register("book_changes", &handlers.BookChangesMethod{})
	s.registry.Register("book_offers", &handlers.BookOffersMethod{})
	s.registry.Register("path_find", &handlers.PathFindMethod{})
	s.registry.Register("ripple_path_find", &handlers.RipplePathFindMethod{})

	// Channel Methods
	s.registry.Register("channel_authorize", &handlers.ChannelAuthorizeMethod{})
	s.registry.Register("channel_verify", &handlers.ChannelVerifyMethod{})

	// Subscription Methods (WebSocket only)
	s.registry.Register("subscribe", &handlers.SubscribeMethod{})
	s.registry.Register("unsubscribe", &handlers.UnsubscribeMethod{})

	// JSON method proxy
	s.registry.Register("json", &handlers.JsonMethod{})

	// Utility Methods
	s.registry.Register("wallet_propose", &handlers.WalletProposeMethod{})
	s.registry.Register("deposit_authorized", &handlers.DepositAuthorizedMethod{})
	s.registry.Register("nft_buy_offers", &handlers.NftBuyOffersMethod{})
	s.registry.Register("nft_sell_offers", &handlers.NftSellOffersMethod{})

	// Standalone mode methods
	s.registry.Register("ledger_accept", &handlers.LedgerAcceptMethod{})

	// Admin Methods (require admin role)
	s.registry.Register("stop", &handlers.StopMethod{})
	s.registry.Register("validation_create", &handlers.ValidationCreateMethod{})
	s.registry.Register("manifest", &handlers.ManifestMethod{})
	s.registry.Register("peer_reservations_add", &handlers.PeerReservationsAddMethod{})
	s.registry.Register("peer_reservations_del", &handlers.PeerReservationsDelMethod{})
	s.registry.Register("peer_reservations_list", &handlers.PeerReservationsListMethod{})
	s.registry.Register("peers", &handlers.PeersMethod{})
	s.registry.Register("consensus_info", &handlers.ConsensusInfoMethod{})
	s.registry.Register("validator_list_sites", &handlers.ValidatorListSitesMethod{})
	s.registry.Register("validators", &handlers.ValidatorsMethod{})

	// =========================================================================
	// Additional Methods (added for rippled compatibility)
	// =========================================================================

	// Server/Network Methods
	s.registry.Register("fetch_info", &handlers.FetchInfoMethod{})
	s.registry.Register("connect", &handlers.ConnectMethod{})
	s.registry.Register("print", &handlers.PrintMethod{})

	// Ledger Methods
	s.registry.Register("ledger_header", &handlers.LedgerHeaderMethod{})
	s.registry.Register("ledger_request", &handlers.LedgerRequestMethod{})
	s.registry.Register("ledger_cleaner", &handlers.LedgerCleanerMethod{})

	// Account Methods
	s.registry.Register("owner_info", &handlers.OwnerInfoMethod{})

	// Transaction Methods
	s.registry.Register("simulate", &handlers.SimulateMethod{})
	s.registry.Register("tx_reduce_relay", &handlers.TxReduceRelayMethod{})

	// Validator Methods
	s.registry.Register("validator_info", &handlers.ValidatorInfoMethod{})
	s.registry.Register("unl_list", &handlers.UnlListMethod{})

	// Admin/Operational Methods
	s.registry.Register("can_delete", &handlers.CanDeleteMethod{})
	s.registry.Register("get_counts", &handlers.GetCountsMethod{})
	s.registry.Register("log_level", &handlers.LogLevelMethod{})
	s.registry.Register("logrotate", &handlers.LogRotateMethod{})
	s.registry.Register("blacklist", &handlers.BlackListMethod{})

	// Feature-specific Methods (depend on unimplemented ledger entry types)
	s.registry.Register("amm_info", &handlers.AMMInfoMethod{})
	s.registry.Register("vault_info", &handlers.VaultInfoMethod{})
	s.registry.Register("get_aggregate_price", &handlers.GetAggregatePriceMethod{})
}

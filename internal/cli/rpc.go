package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
	"github.com/spf13/cobra"
)

// rpcCmd represents the rpc command group
var rpcCmd = &cobra.Command{
	Use:   "rpc",
	Short: "RPC client commands",
	Long:  `Execute RPC commands locally by calling the same handlers used by the server.`,
}

func init() {
	rootCmd.AddCommand(rpcCmd)
}

// methodRegistry holds all available RPC methods
var methodRegistry *rpc_types.MethodRegistry

// initMethodRegistry initializes the method registry with all available methods
func initMethodRegistry() *rpc_types.MethodRegistry {
	if methodRegistry != nil {
		return methodRegistry
	}
	
	registry := rpc_types.NewMethodRegistry()
	
	// Server methods
	registry.Register("ping", &rpc_handlers.PingMethod{})
	registry.Register("server_info", &rpc_handlers.ServerInfoMethod{})
	registry.Register("server_state", &rpc_handlers.ServerStateMethod{})
	registry.Register("random", &rpc_handlers.RandomMethod{})
	registry.Register("server_definitions", &rpc_handlers.ServerDefinitionsMethod{})
	registry.Register("feature", &rpc_handlers.FeatureMethod{})
	registry.Register("fee", &rpc_handlers.FeeMethod{})
	
	// Account methods
	registry.Register("account_info", &rpc_handlers.AccountInfoMethod{})
	registry.Register("account_channels", &rpc_handlers.AccountChannelsMethod{})
	registry.Register("account_currencies", &rpc_handlers.AccountCurrenciesMethod{})
	registry.Register("account_lines", &rpc_handlers.AccountLinesMethod{})
	registry.Register("account_nfts", &rpc_handlers.AccountNftsMethod{})
	registry.Register("account_objects", &rpc_handlers.AccountObjectsMethod{})
	registry.Register("account_offers", &rpc_handlers.AccountOffersMethod{})
	registry.Register("account_tx", &rpc_handlers.AccountTxMethod{})
	registry.Register("gateway_balances", &rpc_handlers.GatewayBalancesMethod{})
	registry.Register("noripple_check", &rpc_handlers.NoRippleCheckMethod{})
	
	// Ledger methods
	registry.Register("ledger", &rpc_handlers.LedgerMethod{})
	registry.Register("ledger_closed", &rpc_handlers.LedgerClosedMethod{})
	registry.Register("ledger_current", &rpc_handlers.LedgerCurrentMethod{})
	registry.Register("ledger_data", &rpc_handlers.LedgerDataMethod{})
	registry.Register("ledger_entry", &rpc_handlers.LedgerEntryMethod{})
	registry.Register("ledger_range", &rpc_handlers.LedgerRangeMethod{})
	
	// Transaction methods
	registry.Register("tx", &rpc_handlers.TxMethod{})
	registry.Register("tx_history", &rpc_handlers.TxHistoryMethod{})
	registry.Register("submit", &rpc_handlers.SubmitMethod{})
	registry.Register("submit_multisigned", &rpc_handlers.SubmitMultisignedMethod{})
	registry.Register("sign", &rpc_handlers.SignMethod{})
	registry.Register("sign_for", &rpc_handlers.SignForMethod{})
	registry.Register("transaction_entry", &rpc_handlers.TransactionEntryMethod{})
	
	// Utility methods
	registry.Register("book_offers", &rpc_handlers.BookOffersMethod{})
	registry.Register("path_find", &rpc_handlers.PathFindMethod{})
	registry.Register("ripple_path_find", &rpc_handlers.RipplePathFindMethod{})
	registry.Register("wallet_propose", &rpc_handlers.WalletProposeMethod{})
	registry.Register("deposit_authorized", &rpc_handlers.DepositAuthorizedMethod{})
	registry.Register("channel_authorize", &rpc_handlers.ChannelAuthorizeMethod{})
	registry.Register("channel_verify", &rpc_handlers.ChannelVerifyMethod{})
	registry.Register("json", &rpc_handlers.JsonMethod{})
	
	// NFT methods
	registry.Register("nft_buy_offers", &rpc_handlers.NftBuyOffersMethod{})
	registry.Register("nft_sell_offers", &rpc_handlers.NftSellOffersMethod{})
	
	// Admin methods (require admin role)
	registry.Register("stop", &rpc_handlers.StopMethod{})
	registry.Register("validation_create", &rpc_handlers.ValidationCreateMethod{})
	registry.Register("manifest", &rpc_handlers.ManifestMethod{})
	registry.Register("peer_reservations_add", &rpc_handlers.PeerReservationsAddMethod{})
	registry.Register("peer_reservations_del", &rpc_handlers.PeerReservationsDelMethod{})
	registry.Register("peer_reservations_list", &rpc_handlers.PeerReservationsListMethod{})
	registry.Register("peers", &rpc_handlers.PeersMethod{})
	registry.Register("consensus_info", &rpc_handlers.ConsensusInfoMethod{})
	registry.Register("validators", &rpc_handlers.ValidatorsMethod{})
	registry.Register("validator_list_sites", &rpc_handlers.ValidatorListSitesMethod{})
	registry.Register("download_shard", &rpc_handlers.DownloadShardMethod{})
	registry.Register("crawl_shards", &rpc_handlers.CrawlShardsMethod{})
	registry.Register("ledger_index", &rpc_handlers.LedgerIndexMethod{})
	
	// Subscription methods (for WebSocket)
	registry.Register("subscribe", &rpc_handlers.SubscribeMethod{})
	registry.Register("unsubscribe", &rpc_handlers.UnsubscribeMethod{})
	
	methodRegistry = registry
	return registry
}

// executeMethod calls an RPC method handler directly
func executeMethod(method string, params interface{}) error {
	registry := initMethodRegistry()
	
	handler, exists := registry.Get(method)
	if !exists {
		return fmt.Errorf("unknown method: %s", method)
	}
	
	// Create RPC context (CLI runs as admin role)
	rpcCtx := &rpc_types.RpcContext{
		Context:    context.Background(),
		Role:       rpc_types.RoleAdmin,
		ApiVersion: rpc_types.DefaultApiVersion,
		IsAdmin:    true,
		ClientIP:   "127.0.0.1", // Local CLI
	}
	
	// Marshal params to JSON if provided
	var paramBytes json.RawMessage
	if params != nil {
		bytes, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal parameters: %w", err)
		}
		paramBytes = json.RawMessage(bytes)
	}
	
	// Call the method handler directly
	result, rpcErr := handler.Handle(rpcCtx, paramBytes)
	if rpcErr != nil {
		return fmt.Errorf("RPC error [%d]: %s", rpcErr.Code, rpcErr.Message)
	}
	
	// Pretty print the result
	if result != nil {
		prettyJSON, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Printf("%+v\n", result)
			return nil
		}
		fmt.Println(string(prettyJSON))
	}
	
	return nil
}

// =============================================================================
// SERVER COMMANDS
// =============================================================================

var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Ping the server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("ping", nil)
	},
}

var serverInfoCmd = &cobra.Command{
	Use:   "server_info [counters]",
	Short: "Get server information",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 && args[0] == "counters" {
			params = map[string]interface{}{"counters": true}
		}
		return executeMethod("server_info", params)
	},
}

var serverStateCmd = &cobra.Command{
	Use:   "server_state [counters]",
	Short: "Get server state",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 && args[0] == "counters" {
			params = map[string]interface{}{"counters": true}
		}
		return executeMethod("server_state", params)
	},
}

var randomCmd = &cobra.Command{
	Use:   "random",
	Short: "Generate a random number",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("random", nil)
	},
}

var serverDefinitionsCmd = &cobra.Command{
	Use:   "server_definitions [hash]",
	Short: "Get server field and type definitions",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 {
			params = map[string]interface{}{"hash": args[0]}
		}
		return executeMethod("server_definitions", params)
	},
}

var featureCmd = &cobra.Command{
	Use:   "feature [feature_name] [accept|reject]",
	Short: "Get or set amendment/feature status",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 {
			params = map[string]interface{}{
				"feature": args[0],
			}
			if len(args) > 1 {
				params["vote"] = args[1]
			}
		}
		return executeMethod("feature", params)
	},
}

var feeCmd = &cobra.Command{
	Use:   "fee",
	Short: "Get current fee information",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("fee", nil)
	},
}

// =============================================================================
// ACCOUNT COMMANDS
// =============================================================================

var accountInfoCmd = &cobra.Command{
	Use:   "account_info <account> [ledger]",
	Short: "Get account information",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("account_info", params)
	},
}

var accountChannelsCmd = &cobra.Command{
	Use:   "account_channels <account> [destination_account] [ledger]",
	Short: "Get account payment channels",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 && args[1] != "" {
			params["destination_account"] = args[1]
		}
		if len(args) > 2 {
			params["ledger_index"] = args[2]
		}
		return executeMethod("account_channels", params)
	},
}

var accountCurrenciesCmd = &cobra.Command{
	Use:   "account_currencies <account> [ledger]",
	Short: "Get currencies an account can send or receive",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("account_currencies", params)
	},
}

var accountLinesCmd = &cobra.Command{
	Use:   "account_lines <account> [peer] [ledger]",
	Short: "Get account trust lines",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 && args[1] != "" {
			params["peer"] = args[1]
		}
		if len(args) > 2 {
			params["ledger_index"] = args[2]
		}
		return executeMethod("account_lines", params)
	},
}

var accountNftsCmd = &cobra.Command{
	Use:   "account_nfts <account> [ledger]",
	Short: "Get NFTs owned by an account",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("account_nfts", params)
	},
}

var accountObjectsCmd = &cobra.Command{
	Use:   "account_objects <account> [ledger]",
	Short: "Get objects owned by an account",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("account_objects", params)
	},
}

var accountOffersCmd = &cobra.Command{
	Use:   "account_offers <account> [ledger]",
	Short: "Get offers placed by an account",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("account_offers", params)
	},
}

var accountTxCmd = &cobra.Command{
	Use:   "account_tx <account> [ledger_index_min] [ledger_index_max] [limit] [binary]",
	Short: "Get account transaction history",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		
		if len(args) > 1 {
			if min, err := strconv.Atoi(args[1]); err == nil {
				params["ledger_index_min"] = min
			}
		}
		if len(args) > 2 {
			if max, err := strconv.Atoi(args[2]); err == nil {
				params["ledger_index_max"] = max
			}
		}
		if len(args) > 3 {
			if limit, err := strconv.Atoi(args[3]); err == nil {
				params["limit"] = limit
			}
		}
		if len(args) > 4 && args[4] == "binary" {
			params["binary"] = true
		}
		
		return executeMethod("account_tx", params)
	},
}

var gatewayBalancesCmd = &cobra.Command{
	Use:   "gateway_balances <issuer_account> [ledger] [hotwallet1] [hotwallet2]",
	Short: "Get gateway balances",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		if len(args) > 2 {
			hotwallets := args[2:]
			params["hotwallet"] = hotwallets
		}
		return executeMethod("gateway_balances", params)
	},
}

var norippleCheckCmd = &cobra.Command{
	Use:   "noripple_check <account> [ledger]",
	Short: "Check NoRipple flag settings",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("noripple_check", params)
	},
}

// =============================================================================
// LEDGER COMMANDS
// =============================================================================

var ledgerCmd = &cobra.Command{
	Use:   "ledger [ledger_identifier] [full]",
	Short: "Get ledger information",
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{}
		
		if len(args) > 0 {
			switch args[0] {
			case "current", "closed", "validated":
				params["ledger_index"] = args[0]
			default:
				// Try to parse as number, otherwise assume it's a hash
				if _, err := strconv.Atoi(args[0]); err == nil {
					params["ledger_index"] = args[0]
				} else {
					params["ledger_hash"] = args[0]
				}
			}
		}
		
		if len(args) > 1 && args[1] == "full" {
			params["full"] = true
		}
		
		return executeMethod("ledger", params)
	},
}

var ledgerClosedCmd = &cobra.Command{
	Use:   "ledger_closed",
	Short: "Get the last closed ledger",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("ledger_closed", nil)
	},
}

var ledgerCurrentCmd = &cobra.Command{
	Use:   "ledger_current",
	Short: "Get the current working ledger",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("ledger_current", nil)
	},
}

var ledgerDataCmd = &cobra.Command{
	Use:   "ledger_data [ledger] [limit] [marker]",
	Short: "Get ledger objects",
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{}
		
		if len(args) > 0 {
			params["ledger_index"] = args[0]
		}
		if len(args) > 1 {
			if limit, err := strconv.Atoi(args[1]); err == nil {
				params["limit"] = limit
			}
		}
		if len(args) > 2 {
			params["marker"] = args[2]
		}
		
		return executeMethod("ledger_data", params)
	},
}

var ledgerEntryCmd = &cobra.Command{
	Use:   "ledger_entry <type> [additional_args...]",
	Short: "Get a specific ledger entry",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"type": args[0],
		}
		// Additional args depend on the entry type
		if len(args) > 1 {
			for i, arg := range args[1:] {
				params[fmt.Sprintf("arg%d", i)] = arg
			}
		}
		return executeMethod("ledger_entry", params)
	},
}

var ledgerRangeCmd = &cobra.Command{
	Use:   "ledger_range <start> <end>",
	Short: "Get range of ledgers",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		start, err1 := strconv.Atoi(args[0])
		end, err2 := strconv.Atoi(args[1])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("invalid ledger indices")
		}
		
		params := map[string]interface{}{
			"ledger_index_min": start,
			"ledger_index_max": end,
		}
		return executeMethod("ledger_range", params)
	},
}

// =============================================================================
// TRANSACTION COMMANDS
// =============================================================================

var txCmd = &cobra.Command{
	Use:   "tx <transaction_hash>",
	Short: "Get transaction information",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"transaction": args[0],
		}
		return executeMethod("tx", params)
	},
}

var txHistoryCmd = &cobra.Command{
	Use:   "tx_history <start_index>",
	Short: "Get transaction history",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		start, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid start index")
		}
		params := map[string]interface{}{
			"start": start,
		}
		return executeMethod("tx_history", params)
	},
}

var submitCmd = &cobra.Command{
	Use:   "submit <tx_blob> | <private_key> <tx_json>",
	Short: "Submit a transaction",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		
		if len(args) == 1 {
			// Single argument - assume it's a tx_blob
			params = map[string]interface{}{
				"tx_blob": args[0],
			}
		} else if len(args) == 2 {
			// Two arguments - private key and tx_json
			params = map[string]interface{}{
				"secret":  args[0],
				"tx_json": args[1],
			}
		} else {
			return fmt.Errorf("invalid number of arguments")
		}
		
		return executeMethod("submit", params)
	},
}

var submitMultisignedCmd = &cobra.Command{
	Use:   "submit_multisigned <tx_json>",
	Short: "Submit a multisigned transaction",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"tx_json": args[0],
		}
		return executeMethod("submit_multisigned", params)
	},
}

var signCmd = &cobra.Command{
	Use:   "sign <private_key> <tx_json> [offline]",
	Short: "Sign a transaction",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"secret":  args[0],
			"tx_json": args[1],
		}
		if len(args) > 2 && args[2] == "offline" {
			params["offline"] = true
		}
		return executeMethod("sign", params)
	},
}

var signForCmd = &cobra.Command{
	Use:   "sign_for <signer_address> <signer_private_key> <tx_json> [offline]",
	Short: "Sign a transaction for multisigning",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"account": args[0],
			"secret":  args[1],
			"tx_json": args[2],
		}
		if len(args) > 3 && args[3] == "offline" {
			params["offline"] = true
		}
		return executeMethod("sign_for", params)
	},
}

var transactionEntryCmd = &cobra.Command{
	Use:   "transaction_entry <tx_hash> <ledger>",
	Short: "Get transaction from a specific ledger",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"tx_hash":      args[0],
			"ledger_index": args[1],
		}
		return executeMethod("transaction_entry", params)
	},
}

// =============================================================================
// UTILITY COMMANDS
// =============================================================================

var bookOffersCmd = &cobra.Command{
	Use:   "book_offers <taker_pays> <taker_gets> [taker] [ledger] [limit] [proof] [marker]",
	Short: "Get order book offers",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"taker_pays": args[0],
			"taker_gets": args[1],
		}
		
		if len(args) > 2 && args[2] != "" {
			params["taker"] = args[2]
		}
		if len(args) > 3 {
			params["ledger_index"] = args[3]
		}
		if len(args) > 4 {
			if limit, err := strconv.Atoi(args[4]); err == nil {
				params["limit"] = limit
			}
		}
		if len(args) > 5 {
			params["proof"] = args[5] == "true"
		}
		if len(args) > 6 {
			params["marker"] = args[6]
		}
		
		return executeMethod("book_offers", params)
	},
}

var pathFindCmd = &cobra.Command{
	Use:   "path_find <source_account> <destination_account> <destination_amount>",
	Short: "Find payment paths",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"source_account":      args[0],
			"destination_account": args[1],
			"destination_amount":  args[2],
		}
		return executeMethod("path_find", params)
	},
}

var ripplePathFindCmd = &cobra.Command{
	Use:   "ripple_path_find <json> [ledger]",
	Short: "Find payment paths (ripple format)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var pathRequest interface{}
		if err := json.Unmarshal([]byte(args[0]), &pathRequest); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		
		params := pathRequest
		if len(args) > 1 {
			// Convert to map to add ledger
			if paramsMap, ok := params.(map[string]interface{}); ok {
				paramsMap["ledger_index"] = args[1]
				params = paramsMap
			}
		}
		
		return executeMethod("ripple_path_find", params)
	},
}

var walletProposeCmd = &cobra.Command{
	Use:   "wallet_propose [passphrase]",
	Short: "Generate wallet credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 {
			params = map[string]interface{}{
				"passphrase": strings.Join(args, " "),
			}
		}
		return executeMethod("wallet_propose", params)
	},
}

var depositAuthorizedCmd = &cobra.Command{
	Use:   "deposit_authorized <source_account> <destination_account> [ledger]",
	Short: "Check if deposit is authorized",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"source_account":      args[0],
			"destination_account": args[1],
		}
		if len(args) > 2 {
			params["ledger_index"] = args[2]
		}
		return executeMethod("deposit_authorized", params)
	},
}

var channelAuthorizeCmd = &cobra.Command{
	Use:   "channel_authorize <private_key> <channel_id> <drops>",
	Short: "Authorize a payment channel claim",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		amount, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid amount: %w", err)
		}
		
		params := map[string]interface{}{
			"secret":  args[0],
			"channel": args[1],
			"amount":  amount,
		}
		return executeMethod("channel_authorize", params)
	},
}

var channelVerifyCmd = &cobra.Command{
	Use:   "channel_verify <public_key> <channel_id> <drops> <signature>",
	Short: "Verify a payment channel claim",
	Args:  cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		amount, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid amount: %w", err)
		}
		
		params := map[string]interface{}{
			"public_key": args[0],
			"channel":    args[1],
			"amount":     amount,
			"signature":  args[3],
		}
		return executeMethod("channel_verify", params)
	},
}

// Generic JSON command for any method
var jsonCmd = &cobra.Command{
	Use:   "json <method> <json_params>",
	Short: "Execute any RPC method with JSON parameters",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		method := args[0]
		jsonParams := args[1]
		
		var params interface{}
		if err := json.Unmarshal([]byte(jsonParams), &params); err != nil {
			return fmt.Errorf("invalid JSON parameters: %w", err)
		}
		
		return executeMethod(method, params)
	},
}

// =============================================================================
// NFT COMMANDS
// =============================================================================

var nftBuyOffersCmd = &cobra.Command{
	Use:   "nft_buy_offers <nft_id> [ledger]",
	Short: "Get buy offers for an NFT",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"nft_id": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("nft_buy_offers", params)
	},
}

var nftSellOffersCmd = &cobra.Command{
	Use:   "nft_sell_offers <nft_id> [ledger]",
	Short: "Get sell offers for an NFT",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"nft_id": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("nft_sell_offers", params)
	},
}

var nftHistoryCmd = &cobra.Command{
	Use:   "nft_history <nft_id>",
	Short: "Get NFT transaction history",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"nft_id": args[0],
		}
		return executeMethod("nft_history", params)
	},
}

var nftsByIssuerCmd = &cobra.Command{
	Use:   "nfts_by_issuer <issuer> [ledger]",
	Short: "Get NFTs by issuer",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"issuer": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("nfts_by_issuer", params)
	},
}

var nftInfoCmd = &cobra.Command{
	Use:   "nft_info <nft_id> [ledger]",
	Short: "Get NFT information",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"nft_id": args[0],
		}
		if len(args) > 1 {
			params["ledger_index"] = args[1]
		}
		return executeMethod("nft_info", params)
	},
}

// =============================================================================
// ADMIN COMMANDS
// =============================================================================

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the server gracefully",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("stop", nil)
	},
}

var validationCreateCmd = &cobra.Command{
	Use:   "validation_create [seed|passphrase|key]",
	Short: "Create validation credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		var params map[string]interface{}
		if len(args) > 0 {
			params = map[string]interface{}{
				"secret": strings.Join(args, " "),
			}
		}
		return executeMethod("validation_create", params)
	},
}

var manifestCmd = &cobra.Command{
	Use:   "manifest <public_key>",
	Short: "Get validator manifest",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"public_key": args[0],
		}
		return executeMethod("manifest", params)
	},
}

var peerReservationsAddCmd = &cobra.Command{
	Use:   "peer_reservations_add <public_key> [description]",
	Short: "Add peer reservation",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"public_key": args[0],
		}
		if len(args) > 1 {
			params["description"] = strings.Join(args[1:], " ")
		}
		return executeMethod("peer_reservations_add", params)
	},
}

var peerReservationsDelCmd = &cobra.Command{
	Use:   "peer_reservations_del <public_key>",
	Short: "Delete peer reservation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := map[string]interface{}{
			"public_key": args[0],
		}
		return executeMethod("peer_reservations_del", params)
	},
}

var peerReservationsListCmd = &cobra.Command{
	Use:   "peer_reservations_list",
	Short: "List peer reservations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("peer_reservations_list", nil)
	},
}

var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "Get connected peers information",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("peers", nil)
	},
}

var consensusInfoCmd = &cobra.Command{
	Use:   "consensus_info",
	Short: "Get consensus information",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("consensus_info", nil)
	},
}

var validatorsCmd = &cobra.Command{
	Use:   "validators",
	Short: "Get validator information",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("validators", nil)
	},
}

var validatorListSitesCmd = &cobra.Command{
	Use:   "validator_list_sites",
	Short: "Get validator list sites",
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeMethod("validator_list_sites", nil)
	},
}

// =============================================================================
// ADD ALL COMMANDS
// =============================================================================

func init() {
	// Add all RPC commands organized by category
	rpcCmd.AddCommand(
		// Server commands
		pingCmd,
		serverInfoCmd,
		serverStateCmd,
		randomCmd,
		serverDefinitionsCmd,
		featureCmd,
		feeCmd,
		
		// Account commands
		accountInfoCmd,
		accountChannelsCmd,
		accountCurrenciesCmd,
		accountLinesCmd,
		accountNftsCmd,
		accountObjectsCmd,
		accountOffersCmd,
		accountTxCmd,
		gatewayBalancesCmd,
		norippleCheckCmd,
		
		// Ledger commands
		ledgerCmd,
		ledgerClosedCmd,
		ledgerCurrentCmd,
		ledgerDataCmd,
		ledgerEntryCmd,
		ledgerRangeCmd,
		
		// Transaction commands
		txCmd,
		txHistoryCmd,
		submitCmd,
		submitMultisignedCmd,
		signCmd,
		signForCmd,
		transactionEntryCmd,
		
		// Utility commands
		bookOffersCmd,
		pathFindCmd,
		ripplePathFindCmd,
		walletProposeCmd,
		depositAuthorizedCmd,
		channelAuthorizeCmd,
		channelVerifyCmd,
		
		// NFT commands
		nftBuyOffersCmd,
		nftSellOffersCmd,
		nftHistoryCmd,
		nftsByIssuerCmd,
		nftInfoCmd,
		
		// Admin commands
		stopCmd,
		validationCreateCmd,
		manifestCmd,
		peerReservationsAddCmd,
		peerReservationsDelCmd,
		peerReservationsListCmd,
		peersCmd,
		consensusInfoCmd,
		validatorsCmd,
		validatorListSitesCmd,
		
		// Generic JSON command
		jsonCmd,
	)
}
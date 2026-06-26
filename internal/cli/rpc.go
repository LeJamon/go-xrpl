package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/spf13/cobra"
)

// rpcCmd represents the rpc command group
var rpcCmd = &cobra.Command{
	Use:   "rpc",
	Short: "RPC client commands",
	Long: `Forward RPC commands to a running go-xrpl node over HTTP JSON-RPC.

The target node's host and port are read from the HTTP port in --conf, so a
config file is required. Start the node with 'xrpld server --conf ...' first;
admin methods (stop, peers, feature, ...) succeed when the configured HTTP
port grants admin to localhost.`,
}

func init() {
	rootCmd.AddCommand(rpcCmd)
	for _, spec := range rpcCommandSpecs {
		rpcCmd.AddCommand(spec.command())
	}
	rpcCmd.AddCommand(jsonCmd)
}

const rpcRequestTimeout = 30 * time.Second

// rpcCommandSpec is a single `xrpld rpc <name>` subcommand. The command name
// and (by default) the RPC method are the first token of Use; params builds
// the JSON parameters object from positional args. Keeping the per-command
// arg→param mapping in one closure collapses ~50 near-identical command
// literals into a single table.
type rpcCommandSpec struct {
	use    string
	short  string
	long   string
	method string // defaults to the first token of use
	args   cobra.PositionalArgs
	params func(args []string) (any, error)
}

func (s rpcCommandSpec) methodName() string {
	if s.method != "" {
		return s.method
	}
	return strings.Fields(s.use)[0]
}

func (s rpcCommandSpec) command() *cobra.Command {
	method := s.methodName()
	build := s.params
	return &cobra.Command{
		Use:   s.use,
		Short: s.short,
		Long:  s.long,
		Args:  s.args,
		RunE: func(cmd *cobra.Command, args []string) error {
			var params any
			if build != nil {
				p, err := build(args)
				if err != nil {
					return err
				}
				params = p
			}
			return runRPC(cmd, method, params)
		},
	}
}

// runRPC forwards a single method call to the running node's JSON-RPC port and
// prints the result. The request uses XRPL's rippled-style envelope —
// {"method": m, "params": [p]} — and the response is the {"result": {...}}
// object the server returns.
func runRPC(cmd *cobra.Command, method string, params any) error {
	cfg, err := requireConfig()
	if err != nil {
		return err
	}
	endpoint, port, err := rpcEndpoint(cfg)
	if err != nil {
		return err
	}

	reqBody := map[string]any{"method": method}
	if params != nil {
		reqBody["params"] = []any{params}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, rpcRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if user, pass := rpcCredentials(port); user != "" {
		httpReq.SetBasicAuth(user, pass)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("connecting to node at %s: %w (is the server running?)", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s: %w", endpoint, err)
	}

	return printRPCResult(cmd.OutOrStdout(), method, respBody)
}

// rpcEndpoint resolves the JSON-RPC URL to POST to from the HTTP ports in the
// config. An admin port is preferred so admin methods work; ports are sorted
// by name for deterministic selection.
func rpcEndpoint(cfg *config.Config) (string, *config.PortConfig, error) {
	ports := cfg.GetHTTPPorts()
	if len(ports) == 0 {
		return "", nil, fmt.Errorf("no HTTP port configured in %s; 'xrpld rpc' forwards to a running node's JSON-RPC port", configFile)
	}

	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)

	chosen := names[0]
	for _, name := range names {
		if p := ports[name]; len(p.Admin) > 0 || p.AdminUser != "" {
			chosen = name
			break
		}
	}

	p := ports[chosen]
	host := p.IP
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/", host, p.Port), &p, nil
}

// rpcCredentials returns the basic-auth credentials to present to the node,
// preferring the port's admin credentials, mirroring rippled's RPC client.
func rpcCredentials(p *config.PortConfig) (user, pass string) {
	if p == nil {
		return "", ""
	}
	if p.AdminUser != "" {
		return p.AdminUser, p.AdminPassword
	}
	if p.User != "" {
		return p.User, p.Password
	}
	return "", ""
}

// printRPCResult writes the node's result object and reports an error exit when
// the node returned an error status. The error detail is already in the printed
// JSON, so a server-side error maps to cmdexit.ErrReported (exit 1, no extra
// "Error:" line); a malformed response is printed verbatim.
func printRPCResult(w io.Writer, method string, body []byte) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Result == nil {
		fmt.Fprintln(w, strings.TrimRight(string(body), "\n"))
		return nil
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, envelope.Result, "", "  "); err == nil {
		fmt.Fprintln(w, pretty.String())
	} else {
		fmt.Fprintln(w, string(envelope.Result))
	}

	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(envelope.Result, &status); err == nil && status.Status == "error" {
		return cmdexit.ErrReported
	}
	return nil
}

// optionalLedger sets ledger_index from args[i] when present.
func optionalLedger(params map[string]any, args []string, i int) {
	if len(args) > i {
		params["ledger_index"] = args[i]
	}
}

var rpcCommandSpecs = []rpcCommandSpec{
	{use: "ping", short: "Ping the server"},
	{
		use:   "server_info [counters]",
		short: "Get server information",
		params: func(args []string) (any, error) {
			if len(args) > 0 && args[0] == "counters" {
				return map[string]any{"counters": true}, nil
			}
			return nil, nil
		},
	},
	{
		use:   "server_state [counters]",
		short: "Get server state",
		params: func(args []string) (any, error) {
			if len(args) > 0 && args[0] == "counters" {
				return map[string]any{"counters": true}, nil
			}
			return nil, nil
		},
	},
	{use: "random", short: "Generate a random number"},
	{
		use:   "server_definitions [hash]",
		short: "Get server field and type definitions",
		params: func(args []string) (any, error) {
			if len(args) > 0 {
				return map[string]any{"hash": args[0]}, nil
			}
			return nil, nil
		},
	},
	{
		use:   "feature [feature_name] [accept|reject]",
		short: "Get or set amendment/feature status",
		params: func(args []string) (any, error) {
			if len(args) == 0 {
				return nil, nil
			}
			params := map[string]any{"feature": args[0]}
			if len(args) > 1 {
				params["vote"] = args[1]
			}
			return params, nil
		},
	},
	{use: "fee", short: "Get current fee information"},

	{
		use:   "account_info <account> [ledger]",
		short: "Get account information",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "account_channels <account> [destination_account] [ledger]",
		short: "Get account payment channels",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if len(args) > 1 && args[1] != "" {
				params["destination_account"] = args[1]
			}
			optionalLedger(params, args, 2)
			return params, nil
		},
	},
	{
		use:   "account_currencies <account> [ledger]",
		short: "Get currencies an account can send or receive",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "account_lines <account> [peer] [ledger]",
		short: "Get account trust lines",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if len(args) > 1 && args[1] != "" {
				params["peer"] = args[1]
			}
			optionalLedger(params, args, 2)
			return params, nil
		},
	},
	{
		use:   "account_nfts <account> [ledger]",
		short: "Get NFTs owned by an account",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "account_objects <account> [ledger]",
		short: "Get objects owned by an account",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "account_offers <account> [ledger]",
		short: "Get offers placed by an account",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "account_tx <account> [ledger_index_min] [ledger_index_max] [limit] [binary]",
		short: "Get account transaction history",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
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
			return params, nil
		},
	},
	{
		use:   "gateway_balances <issuer_account> [ledger] [hotwallet1] [hotwallet2]",
		short: "Get gateway balances",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			if len(args) > 2 {
				params["hotwallet"] = args[2:]
			}
			return params, nil
		},
	},
	{
		use:   "noripple_check <account> [ledger]",
		short: "Check NoRipple flag settings",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},

	{
		use:   "ledger [ledger_identifier] [full]",
		short: "Get ledger information",
		params: func(args []string) (any, error) {
			params := map[string]any{}
			if len(args) > 0 {
				switch args[0] {
				case "current", "closed", "validated":
					params["ledger_index"] = args[0]
				default:
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
			return params, nil
		},
	},
	{use: "ledger_closed", short: "Get the last closed ledger"},
	{use: "ledger_current", short: "Get the current working ledger"},
	{
		use:   "ledger_data [ledger] [limit] [marker]",
		short: "Get ledger objects",
		params: func(args []string) (any, error) {
			params := map[string]any{}
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
			return params, nil
		},
	},
	{
		use:   "ledger_entry <key=value>...",
		short: "Get a specific ledger entry",
		long: `Get a specific ledger entry by index hash or by a typed selector.

Arguments are key=value pairs forwarded directly to the ledger_entry RPC
parameters object. Common shapes:

  ledger_entry index=<32-byte-hex>
  ledger_entry account_root=<address>
  ledger_entry index=<hex> ledger_index=validated
  ledger_entry directory=<hex> binary=true

See rippled's LedgerEntry.cpp for the full list of selectors.`,
		args: cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{}
			for _, arg := range args {
				k, v, ok := strings.Cut(arg, "=")
				if !ok || k == "" {
					return nil, fmt.Errorf("invalid argument %q: expected key=value", arg)
				}
				switch v {
				case "true":
					params[k] = true
				case "false":
					params[k] = false
				default:
					if n, err := strconv.Atoi(v); err == nil {
						params[k] = n
					} else {
						params[k] = v
					}
				}
			}
			return params, nil
		},
	},
	{
		use:   "ledger_range <start> <end>",
		short: "Get range of ledgers",
		args:  cobra.ExactArgs(2),
		params: func(args []string) (any, error) {
			start, err1 := strconv.Atoi(args[0])
			end, err2 := strconv.Atoi(args[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid ledger indices")
			}
			return map[string]any{
				"ledger_index_min": start,
				"ledger_index_max": end,
			}, nil
		},
	},

	{
		use:   "tx <transaction_hash>",
		short: "Get transaction information",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			return map[string]any{"transaction": args[0]}, nil
		},
	},
	{
		use:   "tx_history <start_index>",
		short: "Get transaction history",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			start, err := strconv.Atoi(args[0])
			if err != nil {
				return nil, fmt.Errorf("invalid start index")
			}
			return map[string]any{"start": start}, nil
		},
	},
	{
		use:   "submit <tx_blob> | <private_key> <tx_json>",
		short: "Submit a transaction",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			switch len(args) {
			case 1:
				return map[string]any{"tx_blob": args[0]}, nil
			case 2:
				return map[string]any{"secret": args[0], "tx_json": args[1]}, nil
			default:
				return nil, fmt.Errorf("invalid number of arguments")
			}
		},
	},
	{
		use:   "submit_multisigned <tx_json>",
		short: "Submit a multisigned transaction",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			return map[string]any{"tx_json": args[0]}, nil
		},
	},
	{
		use:   "sign <private_key> <tx_json> [offline]",
		short: "Sign a transaction",
		args:  cobra.MinimumNArgs(2),
		params: func(args []string) (any, error) {
			params := map[string]any{"secret": args[0], "tx_json": args[1]}
			if len(args) > 2 && args[2] == "offline" {
				params["offline"] = true
			}
			return params, nil
		},
	},
	{
		use:   "sign_for <signer_address> <signer_private_key> <tx_json> [offline]",
		short: "Sign a transaction for multisigning",
		args:  cobra.MinimumNArgs(3),
		params: func(args []string) (any, error) {
			params := map[string]any{
				"account": args[0],
				"secret":  args[1],
				"tx_json": args[2],
			}
			if len(args) > 3 && args[3] == "offline" {
				params["offline"] = true
			}
			return params, nil
		},
	},
	{
		use:   "transaction_entry <tx_hash> <ledger>",
		short: "Get transaction from a specific ledger",
		args:  cobra.ExactArgs(2),
		params: func(args []string) (any, error) {
			return map[string]any{
				"tx_hash":      args[0],
				"ledger_index": args[1],
			}, nil
		},
	},

	{
		use:   "book_offers <taker_pays> <taker_gets> [taker] [ledger] [limit] [proof] [marker]",
		short: "Get order book offers",
		args:  cobra.MinimumNArgs(2),
		params: func(args []string) (any, error) {
			params := map[string]any{
				"taker_pays": args[0],
				"taker_gets": args[1],
			}
			if len(args) > 2 && args[2] != "" {
				params["taker"] = args[2]
			}
			optionalLedger(params, args, 3)
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
			return params, nil
		},
	},
	{
		use:   "path_find <source_account> <destination_account> <destination_amount>",
		short: "Find payment paths",
		args:  cobra.ExactArgs(3),
		params: func(args []string) (any, error) {
			return map[string]any{
				"source_account":      args[0],
				"destination_account": args[1],
				"destination_amount":  args[2],
			}, nil
		},
	},
	{
		use:   "ripple_path_find <json> [ledger]",
		short: "Find payment paths (ripple format)",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			var pathRequest any
			if err := json.Unmarshal([]byte(args[0]), &pathRequest); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			if len(args) > 1 {
				if paramsMap, ok := pathRequest.(map[string]any); ok {
					paramsMap["ledger_index"] = args[1]
					return paramsMap, nil
				}
			}
			return pathRequest, nil
		},
	},
	{
		use:   "wallet_propose [passphrase]",
		short: "Generate wallet credentials",
		params: func(args []string) (any, error) {
			if len(args) > 0 {
				return map[string]any{"passphrase": strings.Join(args, " ")}, nil
			}
			return nil, nil
		},
	},
	{
		use:   "deposit_authorized <source_account> <destination_account> [ledger]",
		short: "Check if deposit is authorized",
		args:  cobra.MinimumNArgs(2),
		params: func(args []string) (any, error) {
			params := map[string]any{
				"source_account":      args[0],
				"destination_account": args[1],
			}
			optionalLedger(params, args, 2)
			return params, nil
		},
	},
	{
		use:   "channel_authorize <private_key> <channel_id> <drops>",
		short: "Authorize a payment channel claim",
		args:  cobra.ExactArgs(3),
		params: func(args []string) (any, error) {
			amount, err := strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid amount: %w", err)
			}
			return map[string]any{
				"secret":  args[0],
				"channel": args[1],
				"amount":  amount,
			}, nil
		},
	},
	{
		use:   "channel_verify <public_key> <channel_id> <drops> <signature>",
		short: "Verify a payment channel claim",
		args:  cobra.ExactArgs(4),
		params: func(args []string) (any, error) {
			amount, err := strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid amount: %w", err)
			}
			return map[string]any{
				"public_key": args[0],
				"channel":    args[1],
				"amount":     amount,
				"signature":  args[3],
			}, nil
		},
	},

	{
		use:   "nft_buy_offers <nft_id> [ledger]",
		short: "Get buy offers for an NFT",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"nft_id": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "nft_sell_offers <nft_id> [ledger]",
		short: "Get sell offers for an NFT",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"nft_id": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "nft_history <nft_id>",
		short: "Get NFT transaction history",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			return map[string]any{"nft_id": args[0]}, nil
		},
	},
	{
		use:   "nfts_by_issuer <issuer> [ledger]",
		short: "Get NFTs by issuer",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"issuer": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},
	{
		use:   "nft_info <nft_id> [ledger]",
		short: "Get NFT information",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"nft_id": args[0]}
			optionalLedger(params, args, 1)
			return params, nil
		},
	},

	{use: "stop", short: "Stop the server gracefully"},
	{
		use:   "validation_create [seed|passphrase|key]",
		short: "Create validation credentials",
		params: func(args []string) (any, error) {
			if len(args) > 0 {
				return map[string]any{"secret": strings.Join(args, " ")}, nil
			}
			return nil, nil
		},
	},
	{
		use:   "manifest <public_key>",
		short: "Get validator manifest",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			return map[string]any{"public_key": args[0]}, nil
		},
	},
	{
		use:   "peer_reservations_add <public_key> [description]",
		short: "Add peer reservation",
		args:  cobra.MinimumNArgs(1),
		params: func(args []string) (any, error) {
			params := map[string]any{"public_key": args[0]}
			if len(args) > 1 {
				params["description"] = strings.Join(args[1:], " ")
			}
			return params, nil
		},
	},
	{
		use:   "peer_reservations_del <public_key>",
		short: "Delete peer reservation",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			return map[string]any{"public_key": args[0]}, nil
		},
	},
	{use: "peer_reservations_list", short: "List peer reservations"},
	{use: "peers", short: "Get connected peers information"},
	{use: "consensus_info", short: "Get consensus information"},
	{use: "validators", short: "Get validator information"},
	{use: "validator_list_sites", short: "Get validator list sites"},
}

// jsonCmd is the generic escape hatch: it takes the method name and a raw JSON
// params object, so it cannot be expressed by the fixed-method table above.
var jsonCmd = &cobra.Command{
	Use:   "json <method> <json_params>",
	Short: "Execute any RPC method with JSON parameters",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var params any
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
			return fmt.Errorf("invalid JSON parameters: %w", err)
		}
		return runRPC(cmd, args[0], params)
	},
}

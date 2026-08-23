package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/spf13/cobra"
)

func (a *application) newRPCCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "rpc",
		Short: "RPC client commands",
		Long: `Forward RPC commands to a running go-xrpl node over HTTP JSON-RPC.

The target node's host and port are read from the HTTP port in --conf, so a
config file is required. Start the node with 'goxrpl server --conf ...' first;
admin methods (stop, peers, feature, ...) succeed when the configured HTTP
port grants admin to localhost.`,
	}
	for _, spec := range rpcCommandSpecs {
		command.AddCommand(spec.commandWithRunner(nil, a.runRPC))
	}
	command.AddCommand(newJSONCommand(a.runRPC))
	command.PersistentFlags().Bool(
		insecureRPCFlag,
		false,
		"allow credentials to be sent over cleartext HTTP to a non-loopback host",
	)
	return command
}

const (
	rpcRequestTimeout   = 30 * time.Second
	maxRPCResponseBytes = 64 << 20
	insecureRPCFlag     = "allow-insecure-rpc-credentials"
	adminUserParam      = "admin_user"
	adminPasswordParam  = "admin_password"
)

func newRPCHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type rpcRunFunc func(*cobra.Command, string, any) error

// rpcCommandSpec is a single `goxrpl rpc <name>` subcommand. The command name
// and (by default) the RPC method are the first token of Use; params builds
// the JSON parameters object from positional args. Keeping the per-command
// arg→param mapping in one closure collapses ~50 near-identical command
// literals into a single table.
type rpcCommandSpec struct {
	use              string
	short            string
	long             string
	method           string
	args             cobra.PositionalArgs
	params           func(args []string) (any, error)
	paramsWithSecret func(cmd *cobra.Command, args []string, secret string, provided bool) (any, error)
}

func (s rpcCommandSpec) methodName() string {
	if s.method != "" {
		return s.method
	}
	return strings.Fields(s.use)[0]
}

func (s rpcCommandSpec) command() *cobra.Command {
	return s.commandWithRunner(nil, nil)
}

func (s rpcCommandSpec) commandWithRunner(ask rpcSecretPrompt, run rpcRunFunc) *cobra.Command {
	method := s.methodName()
	args := s.args
	if args == nil {
		args = cobra.NoArgs
	}
	var commandSecretFlags *rpcSecretFlags
	command := &cobra.Command{
		Use:   s.use,
		Short: s.short,
		Long:  s.long,
		Args:  args,
		RunE: func(cmd *cobra.Command, args []string) error {
			var params any
			if s.paramsWithSecret != nil {
				secret, provided, err := commandSecretFlags.resolve(cmd)
				if err != nil {
					return err
				}
				params, err = s.paramsWithSecret(cmd, args, secret, provided)
				if err != nil {
					return err
				}
			} else if s.params != nil {
				var err error
				params, err = s.params(args)
				if err != nil {
					return err
				}
			}
			if run == nil {
				return fmt.Errorf("RPC command is not attached to a root command")
			}
			return run(cmd, method, params)
		},
	}
	if s.paramsWithSecret != nil {
		commandSecretFlags = bindRPCSecretFlags(command, ask)
	}
	return command
}

// runRPC forwards a single method call to the running node's JSON-RPC port and
// prints the result. The request uses XRPL's rippled-style envelope —
// {"method": m, "params": [p]} — and the response is the {"result": {...}}
// object the server returns.
func (a *application) runRPC(cmd *cobra.Command, method string, params any) error {
	cfg, err := a.requireConfig(false)
	if err != nil {
		return err
	}
	endpoint, port, err := rpcEndpoint(cfg, a.options.configFile)
	if err != nil {
		return err
	}

	params, err = rpcRequestParams(params, port)
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

	if rpcPortHasCredentials(port) && !rpcEndpointIsLoopback(endpoint) && !allowInsecureRPC(cmd) {
		return fmt.Errorf(
			"refusing to send RPC credentials over cleartext HTTP to %s; use --%s to allow this explicitly",
			endpoint,
			insecureRPCFlag,
		)
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
	if user, pass := rpcBasicCredentials(port); user != "" || pass != "" {
		httpReq.SetBasicAuth(user, pass)
	}

	resp, err := a.deps.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("connecting to node at %s: %w (is the server running?)", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := readLimitedRPCBody(resp.Body, maxRPCResponseBytes)
	if err != nil {
		return fmt.Errorf("reading response from %s: %w", endpoint, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("RPC endpoint %s returned HTTP %s", endpoint, resp.Status)
	}

	return printRPCResult(cmd.OutOrStdout(), method, respBody)
}

// rpcEndpoint resolves the JSON-RPC URL to POST to from the HTTP ports in the
// config. An admin port is preferred so admin methods work; ports are sorted
// by name for deterministic selection.
func rpcEndpoint(cfg *config.Config, configPath string) (string, *config.PortConfig, error) {
	ports := cfg.HTTPPorts()
	if len(ports) == 0 {
		return "", nil, fmt.Errorf("no HTTP port configured in %s; 'goxrpl rpc' forwards to a running node's JSON-RPC port", configPath)
	}

	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)

	chosen := names[0]
	for _, name := range names {
		p := ports[name]
		host := rpcTargetHost(p.IP)
		adminCapable, err := rpcPortIsAdminCapable(p, host)
		if err != nil {
			return "", nil, fmt.Errorf("invalid admin configuration for port %q: %w", name, err)
		}
		if adminCapable {
			chosen = name
			break
		}
	}

	p := ports[chosen]
	host := rpcTargetHost(p.IP)
	endpoint := (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(p.Port)),
		Path:   "/",
	}).String()
	return endpoint, &p, nil
}

func rpcTargetHost(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return host
	}
}

func rpcPortIsAdminCapable(p config.PortConfig, host string) (bool, error) {
	if len(p.Admin) == 0 {
		return false, nil
	}
	nets, err := p.ParseAdminNets()
	if err != nil {
		return false, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false, nil
	}
	return config.IPInNets(ip, nets), nil
}

func rpcRequestParams(params any, p *config.PortConfig) (any, error) {
	if p == nil || p.AdminUser == "" && p.AdminPassword == "" {
		return params, nil
	}

	requestParams := make(map[string]any)
	if params != nil {
		source, ok := params.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("RPC parameters must be a JSON object when admin credentials are configured")
		}
		for key, value := range source {
			requestParams[key] = value
		}
	}
	requestParams[adminUserParam] = p.AdminUser
	requestParams[adminPasswordParam] = p.AdminPassword
	return requestParams, nil
}

func rpcBasicCredentials(p *config.PortConfig) (user, pass string) {
	if p == nil {
		return "", ""
	}
	return p.User, p.Password
}

func rpcPortHasCredentials(p *config.PortConfig) bool {
	return p != nil && (p.AdminUser != "" || p.AdminPassword != "" || p.User != "" || p.Password != "")
}

func rpcEndpointIsLoopback(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func allowInsecureRPC(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	flag := cmd.Flags().Lookup(insecureRPCFlag)
	if flag == nil {
		flag = cmd.InheritedFlags().Lookup(insecureRPCFlag)
	}
	if flag == nil {
		return false
	}
	allowed, err := strconv.ParseBool(flag.Value.String())
	return err == nil && allowed
}

func readLimitedRPCBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return body, nil
}

// printRPCResult writes the node's result object and reports an error exit when
// the node returned an error status. The error detail is already in the printed
// JSON, so a server-side error maps to cmdexit.ErrReported (exit 1, no extra
// "Error:" line).
func printRPCResult(w io.Writer, method string, body []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		if err == nil {
			err = fmt.Errorf("expected a JSON object")
		}
		return fmt.Errorf("%s RPC returned malformed JSON: %w", method, err)
	}
	resultJSON, ok := envelope["result"]
	if !ok || bytes.Equal(bytes.TrimSpace(resultJSON), []byte("null")) {
		return fmt.Errorf("%s RPC response is missing a result object", method)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultJSON, &result); err != nil || result == nil {
		if err == nil {
			err = fmt.Errorf("expected a JSON object")
		}
		return fmt.Errorf("%s RPC returned an invalid result: %w", method, err)
	}
	var status string
	if rawStatus, ok := result["status"]; !ok || json.Unmarshal(rawStatus, &status) != nil ||
		status != "success" && status != "error" {
		return fmt.Errorf("%s RPC response has an invalid result status", method)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, resultJSON, "", "  "); err == nil {
		fmt.Fprintln(w, pretty.String())
	} else {
		fmt.Fprintln(w, string(resultJSON))
	}

	if status == "error" {
		return cmdexit.ErrReported
	}
	return nil
}

func parseJSONObject(field, value string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()

	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("invalid %s JSON object: %w", field, err)
	}
	if object == nil {
		return nil, fmt.Errorf("invalid %s JSON object: expected an object", field)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return nil, fmt.Errorf("invalid %s JSON object: %w", field, err)
	}
	return object, nil
}

func parseLedgerRangeBound(field, value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: expected an integer", field, value)
	}
	if parsed > int64(^uint32(0)) {
		return 0, fmt.Errorf("invalid %s %q: ledger index exceeds uint32", field, value)
	}
	return parsed, nil
}

func parseUint32(field, value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: expected an unsigned 32-bit integer", field, value)
	}
	return uint32(parsed), nil
}

func parsePositiveUint32(field, value string) (uint32, error) {
	parsed, err := parseUint32(field, value)
	if err != nil {
		return 0, err
	}
	if parsed == 0 {
		return 0, fmt.Errorf("invalid %s %q: expected a positive integer", field, value)
	}
	return parsed, nil
}

func parseBool(field, value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s %q: expected true or false", field, value)
	}
}

func parseEnum(field, value string, allowed ...string) (string, error) {
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("invalid %s %q: expected %s", field, value, strings.Join(allowed, " or "))
}

func setLedgerSelector(params map[string]any, value string) error {
	selection, err := ledgerselector.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid ledger selector %q: %w", value, err)
	}
	if selection.Kind() == ledgerselector.KindHash {
		params["ledger_hash"] = selection.String()
	} else {
		params["ledger_index"] = selection.String()
	}
	return nil
}

func setOptionalLedger(params map[string]any, args []string, i int) error {
	if len(args) <= i {
		return nil
	}
	return setLedgerSelector(params, args[i])
}

func warnPositionalSecret(cmd *cobra.Command) {
	fmt.Fprintln(
		cmd.ErrOrStderr(),
		"Warning: positional secrets are deprecated; use --secret-prompt, --secret-file, or --secret-stdin",
	)
}

func setOfflineOption(params map[string]any, command, value string) error {
	if _, err := parseEnum(command+" option", value, "offline"); err != nil {
		return err
	}
	params["offline"] = true
	return nil
}

func submitParams(cmd *cobra.Command, args []string, secret string, provided bool) (any, error) {
	if provided {
		if len(args) != 1 {
			return nil, fmt.Errorf("submit with a secret source requires exactly one tx_json argument")
		}
		txJSON, err := parseJSONObject("tx_json", args[0])
		if err != nil {
			return nil, err
		}
		return map[string]any{"secret": secret, "tx_json": txJSON}, nil
	}
	if len(args) == 1 {
		return map[string]any{"tx_blob": args[0]}, nil
	}
	warnPositionalSecret(cmd)
	txJSON, err := parseJSONObject("tx_json", args[1])
	if err != nil {
		return nil, err
	}
	return map[string]any{"secret": args[0], "tx_json": txJSON}, nil
}

func signParams(cmd *cobra.Command, args []string, secret string, provided bool) (any, error) {
	txIndex := 0
	offlineIndex := 1
	if !provided {
		if len(args) < 2 {
			return nil, fmt.Errorf("sign requires a secret source or the deprecated positional secret")
		}
		warnPositionalSecret(cmd)
		secret = args[0]
		txIndex = 1
		offlineIndex = 2
	}
	if provided && len(args) > 2 || !provided && len(args) > 3 {
		return nil, fmt.Errorf("invalid number of arguments for sign")
	}
	txJSON, err := parseJSONObject("tx_json", args[txIndex])
	if err != nil {
		return nil, err
	}
	params := map[string]any{"secret": secret, "tx_json": txJSON}
	if len(args) > offlineIndex {
		if err := setOfflineOption(params, "sign", args[offlineIndex]); err != nil {
			return nil, err
		}
	}
	return params, nil
}

func signForParams(cmd *cobra.Command, args []string, secret string, provided bool) (any, error) {
	txIndex := 1
	offlineIndex := 2
	if !provided {
		if len(args) < 3 {
			return nil, fmt.Errorf("sign_for requires a secret source or the deprecated positional secret")
		}
		warnPositionalSecret(cmd)
		secret = args[1]
		txIndex = 2
		offlineIndex = 3
	}
	if provided && len(args) > 3 || !provided && len(args) > 4 {
		return nil, fmt.Errorf("invalid number of arguments for sign_for")
	}
	txJSON, err := parseJSONObject("tx_json", args[txIndex])
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"account": args[0],
		"secret":  secret,
		"tx_json": txJSON,
	}
	if len(args) > offlineIndex {
		if err := setOfflineOption(params, "sign_for", args[offlineIndex]); err != nil {
			return nil, err
		}
	}
	return params, nil
}

func channelAuthorizeParams(
	cmd *cobra.Command,
	args []string,
	secret string,
	provided bool,
) (any, error) {
	channelIndex := 0
	amountIndex := 1
	if !provided {
		if len(args) != 3 {
			return nil, fmt.Errorf("channel_authorize requires a secret source or the deprecated positional secret")
		}
		warnPositionalSecret(cmd)
		secret = args[0]
		channelIndex = 1
		amountIndex = 2
	} else if len(args) != 2 {
		return nil, fmt.Errorf("channel_authorize with a secret source requires channel_id and drops")
	}
	amount, err := strconv.ParseUint(args[amountIndex], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}
	return map[string]any{
		"secret":     secret,
		"channel_id": args[channelIndex],
		"amount":     strconv.FormatUint(amount, 10),
	}, nil
}

func walletProposeParams(
	cmd *cobra.Command,
	args []string,
	secret string,
	provided bool,
) (any, error) {
	if provided {
		if len(args) != 0 {
			return nil, fmt.Errorf("wallet_propose does not accept a positional passphrase with a secret source")
		}
		return map[string]any{"passphrase": secret}, nil
	}
	if len(args) == 0 {
		return nil, nil
	}
	warnPositionalSecret(cmd)
	return map[string]any{"passphrase": args[0]}, nil
}

func validationCreateParams(
	cmd *cobra.Command,
	args []string,
	secret string,
	provided bool,
) (any, error) {
	if provided {
		if len(args) != 0 {
			return nil, fmt.Errorf("validation_create does not accept a positional secret with a secret source")
		}
		return map[string]any{"secret": secret}, nil
	}
	if len(args) == 0 {
		return nil, nil
	}
	warnPositionalSecret(cmd)
	return map[string]any{"secret": args[0]}, nil
}

var rpcCommandSpecs = []rpcCommandSpec{
	{use: "ping", short: "Ping the server"},
	{use: "server_info", short: "Get server information"},
	{use: "server_state", short: "Get server state"},
	{use: "random", short: "Generate a random number"},
	{
		use:   "server_definitions [hash]",
		short: "Get server field and type definitions",
		args:  cobra.RangeArgs(0, 1),
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
		args:  cobra.RangeArgs(0, 2),
		params: func(args []string) (any, error) {
			if len(args) == 0 {
				return nil, nil
			}
			params := map[string]any{"feature": args[0]}
			if len(args) > 1 {
				vote, err := parseEnum("feature vote", args[1], "accept", "reject")
				if err != nil {
					return nil, err
				}
				params["vetoed"] = vote == "reject"
			}
			return params, nil
		},
	},
	{use: "fee", short: "Get current fee information"},

	{
		use:   "account_info <account> [ledger]",
		short: "Get account information",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_channels <account> [destination_account] [ledger]",
		short: "Get account payment channels",
		args:  cobra.RangeArgs(1, 3),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if len(args) > 1 && args[1] != "" {
				params["destination_account"] = args[1]
			}
			if err := setOptionalLedger(params, args, 2); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_currencies <account> [ledger]",
		short: "Get currencies an account can send or receive",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_lines <account> [peer] [ledger]",
		short: "Get account trust lines",
		args:  cobra.RangeArgs(1, 3),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if len(args) > 1 && args[1] != "" {
				params["peer"] = args[1]
			}
			if err := setOptionalLedger(params, args, 2); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_nfts <account> [ledger]",
		short: "Get NFTs owned by an account",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_objects <account> [ledger]",
		short: "Get objects owned by an account",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_offers <account> [ledger]",
		short: "Get offers placed by an account",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "account_tx <account> [ledger_index_min] [ledger_index_max] [limit] [binary]",
		short: "Get account transaction history",
		args:  cobra.RangeArgs(1, 5),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if len(args) > 1 {
				minimum, err := parseLedgerRangeBound("ledger_index_min", args[1])
				if err != nil {
					return nil, err
				}
				params["ledger_index_min"] = minimum
			}
			if len(args) > 2 {
				maximum, err := parseLedgerRangeBound("ledger_index_max", args[2])
				if err != nil {
					return nil, err
				}
				params["ledger_index_max"] = maximum
			}
			if len(args) > 3 {
				limit, err := parsePositiveUint32("limit", args[3])
				if err != nil {
					return nil, err
				}
				params["limit"] = limit
			}
			if len(args) > 4 {
				if _, err := parseEnum("output format", args[4], "binary"); err != nil {
					return nil, err
				}
				params["binary"] = true
			}
			return params, nil
		},
	},
	{
		use:   "gateway_balances <issuer_account> [ledger] [hotwallet1] [hotwallet2]",
		short: "Get gateway balances",
		args:  cobra.RangeArgs(1, 4),
		params: func(args []string) (any, error) {
			params := map[string]any{"account": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			if len(args) > 2 {
				params["hotwallet"] = args[2:]
			}
			return params, nil
		},
	},
	{
		use:   "noripple_check <account> <gateway|user> [ledger]",
		short: "Check NoRipple flag settings",
		args:  cobra.RangeArgs(2, 3),
		params: func(args []string) (any, error) {
			role, err := parseEnum("role", args[1], "gateway", "user")
			if err != nil {
				return nil, err
			}
			params := map[string]any{"account": args[0], "role": role}
			if err := setOptionalLedger(params, args, 2); err != nil {
				return nil, err
			}
			return params, nil
		},
	},

	{
		use:   "ledger [ledger_identifier] [full|tx]",
		short: "Get ledger information",
		args:  cobra.RangeArgs(0, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{}
			if len(args) > 0 {
				if err := setLedgerSelector(params, args[0]); err != nil {
					return nil, err
				}
			}
			if len(args) > 1 {
				option, err := parseEnum("ledger option", args[1], "full", "tx")
				if err != nil {
					return nil, err
				}
				if option == "full" {
					params["full"] = true
				} else {
					params["transactions"] = true
					params["expand"] = true
				}
			}
			return params, nil
		},
	},
	{use: "ledger_closed", short: "Get the last closed ledger"},
	{use: "ledger_current", short: "Get the current working ledger"},
	{
		use:   "ledger_data [ledger] [limit] [marker]",
		short: "Get ledger objects",
		args:  cobra.RangeArgs(0, 3),
		params: func(args []string) (any, error) {
			params := map[string]any{}
			if len(args) > 0 {
				if err := setLedgerSelector(params, args[0]); err != nil {
					return nil, err
				}
			}
			if len(args) > 1 {
				limit, err := parseUint32("limit", args[1])
				if err != nil {
					return nil, err
				}
				params["limit"] = limit
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
				case "true", "false":
					boolean, _ := parseBool(k, v)
					params[k] = boolean
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
			start, err := parseUint32("start ledger", args[0])
			if err != nil {
				return nil, err
			}
			stop, err := parseUint32("stop ledger", args[1])
			if err != nil {
				return nil, err
			}
			if start == 0 || stop == 0 {
				return nil, fmt.Errorf("start and stop ledgers must be greater than zero")
			}
			if start > stop {
				return nil, fmt.Errorf("start ledger must not be greater than stop ledger")
			}
			if stop-start > 1000 {
				return nil, fmt.Errorf("ledger range must not exceed 1000 ledgers")
			}
			return map[string]any{
				"start_ledger": start,
				"stop_ledger":  stop,
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
			start, err := parseUint32("start index", args[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"start": start}, nil
		},
	},
	{
		use:   "submit <tx_blob|tx_json>",
		short: "Submit a transaction",
		long: `Submit a signed transaction blob, or submit tx_json with a secret from
--secret-prompt, --secret-file, or --secret-stdin. Positional secrets are
deprecated and emit a warning.`,
		args:             cobra.RangeArgs(1, 2),
		paramsWithSecret: submitParams,
	},
	{
		use:   "submit_multisigned <tx_json>",
		short: "Submit a multisigned transaction",
		args:  cobra.ExactArgs(1),
		params: func(args []string) (any, error) {
			txJSON, err := parseJSONObject("tx_json", args[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"tx_json": txJSON}, nil
		},
	},
	{
		use:   "sign <tx_json> [offline]",
		short: "Sign a transaction",
		long: `Sign tx_json using --secret-prompt, --secret-file, or --secret-stdin.
Positional secrets are deprecated and emit a warning.`,
		args:             cobra.RangeArgs(1, 3),
		paramsWithSecret: signParams,
	},
	{
		use:   "sign_for <signer_address> <tx_json> [offline]",
		short: "Sign a transaction for multisigning",
		long: `Sign tx_json for an account using --secret-prompt, --secret-file, or
--secret-stdin. Positional secrets are deprecated and emit a warning.`,
		args:             cobra.RangeArgs(2, 4),
		paramsWithSecret: signForParams,
	},
	{
		use:   "transaction_entry <tx_hash> <ledger>",
		short: "Get transaction from a specific ledger",
		args:  cobra.ExactArgs(2),
		params: func(args []string) (any, error) {
			params := map[string]any{"tx_hash": args[0]}
			if err := setLedgerSelector(params, args[1]); err != nil {
				return nil, err
			}
			return params, nil
		},
	},

	{
		use:   "book_offers <taker_pays> <taker_gets> [taker] [ledger] [limit]",
		short: "Get order book offers",
		args:  cobra.RangeArgs(2, 5),
		params: func(args []string) (any, error) {
			takerPays, err := parseJSONObject("taker_pays", args[0])
			if err != nil {
				return nil, err
			}
			takerGets, err := parseJSONObject("taker_gets", args[1])
			if err != nil {
				return nil, err
			}
			params := map[string]any{
				"taker_pays": takerPays,
				"taker_gets": takerGets,
			}
			if len(args) > 2 && args[2] != "" {
				params["taker"] = args[2]
			}
			if err := setOptionalLedger(params, args, 3); err != nil {
				return nil, err
			}
			if len(args) > 4 {
				limit, err := parsePositiveUint32("limit", args[4])
				if err != nil {
					return nil, err
				}
				params["limit"] = limit
			}
			return params, nil
		},
	},
	{
		use:   "ripple_path_find <json> [ledger]",
		short: "Find payment paths (ripple format)",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			pathRequest, err := parseJSONObject("path request", args[0])
			if err != nil {
				return nil, err
			}
			if len(args) > 1 {
				if err := setLedgerSelector(pathRequest, args[1]); err != nil {
					return nil, err
				}
			}
			return pathRequest, nil
		},
	},
	{
		use:   "wallet_propose",
		short: "Generate wallet credentials",
		long: `Generate random wallet credentials, or supply a passphrase with
--secret-prompt, --secret-file, or --secret-stdin. Positional passphrases are
deprecated and emit a warning.`,
		args:             cobra.RangeArgs(0, 1),
		paramsWithSecret: walletProposeParams,
	},
	{
		use:   "deposit_authorized <source_account> <destination_account> [ledger]",
		short: "Check if deposit is authorized",
		args:  cobra.RangeArgs(2, 3),
		params: func(args []string) (any, error) {
			params := map[string]any{
				"source_account":      args[0],
				"destination_account": args[1],
			}
			if err := setOptionalLedger(params, args, 2); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "channel_authorize <channel_id> <drops>",
		short: "Authorize a payment channel claim",
		long: `Authorize a payment channel claim using --secret-prompt, --secret-file,
or --secret-stdin. Positional secrets are deprecated and emit a warning.`,
		args:             cobra.RangeArgs(2, 3),
		paramsWithSecret: channelAuthorizeParams,
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
				"channel_id": args[1],
				"amount":     strconv.FormatUint(amount, 10),
				"signature":  args[3],
			}, nil
		},
	},

	{
		use:   "nft_buy_offers <nft_id> [ledger]",
		short: "Get buy offers for an NFT",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"nft_id": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},
	{
		use:   "nft_sell_offers <nft_id> [ledger]",
		short: "Get sell offers for an NFT",
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"nft_id": args[0]}
			if err := setOptionalLedger(params, args, 1); err != nil {
				return nil, err
			}
			return params, nil
		},
	},

	{use: "stop", short: "Stop the server gracefully"},
	{
		use:   "validation_create",
		short: "Create validation credentials",
		long: `Create random validation credentials, or supply a secret with
--secret-prompt, --secret-file, or --secret-stdin. Positional secrets are
deprecated and emit a warning.`,
		args:             cobra.RangeArgs(0, 1),
		paramsWithSecret: validationCreateParams,
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
		args:  cobra.RangeArgs(1, 2),
		params: func(args []string) (any, error) {
			params := map[string]any{"public_key": args[0]}
			if len(args) > 1 {
				params["description"] = args[1]
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

func newJSONCommand(run rpcRunFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "json <method> <json_params>",
		Short: "Execute any RPC method with JSON parameters",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseJSONObject("parameters", args[1])
			if err != nil {
				return err
			}
			if run == nil {
				return fmt.Errorf("RPC command is not attached to a root command")
			}
			return run(cmd, args[0], params)
		},
	}
}

package config

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// fetchDepthMin mirrors rippled's hard floor (Config.cpp:671-672).
const fetchDepthMin = 10

// LedgerHistory is a typed union for the `ledger_history` TOML key.
// TOML accepts an integer (number of ledgers to keep) or one of the
// strings "full" or "none" (case-insensitive, matching rippled's
// boost::iequals comparison in Config.cpp:653-657).
type LedgerHistory struct {
	// Set is true once a value has been parsed; false means the key was absent.
	Set bool
	// Full is true when the TOML value was the string "full".
	Full bool
	// Count is the integer value when Set && !Full (and not "none", which is 0).
	Count int
}

// IsZero reports whether no value has been provided.
func (lh LedgerHistory) IsZero() bool { return !lh.Set }

// Value returns the integer representation used elsewhere in the codebase:
// the explicit count, or math.MaxInt32 for "full" (matching rippled's
// std::numeric_limits<uint32_t>::max() behaviour in Config.cpp:654-655 so
// that downstream comparisons such as `online_delete < ledger_history`
// fire the same way as in rippled — see SHAMapStoreImp.cpp:148-154).
func (lh LedgerHistory) Value() int {
	if lh.Full {
		return math.MaxInt32
	}
	return lh.Count
}

// FetchDepth is a typed union for the `fetch_depth` TOML key.
// TOML accepts an integer or one of the strings "full" or "none"
// (case-insensitive, matching rippled's boost::iequals comparison in
// Config.cpp:664-666).
type FetchDepth struct {
	Set   bool
	Full  bool
	Count int
}

func (fd FetchDepth) IsZero() bool { return !fd.Set }

// Value returns the integer representation: math.MaxInt32 for "full",
// otherwise the explicit count clamped to a minimum of 10 (rippled's
// FETCH_DEPTH < 10 → 10 floor in Config.cpp:671-672).
func (fd FetchDepth) Value() int {
	if fd.Full {
		return math.MaxInt32
	}
	if fd.Count < fetchDepthMin {
		return fetchDepthMin
	}
	return fd.Count
}

// NetworkID is a typed union for the `network_id` TOML key.
// TOML accepts an integer (network ID) or one of the named strings
// "main", "testnet", "devnet". A digit-string (e.g. "21338") is parsed
// numerically to match rippled's beast::lexicalCastThrow<uint32_t>
// fallback in Config.cpp:531-532.
type NetworkID struct {
	Set  bool
	ID   int
	Name string
}

func (n NetworkID) IsZero() bool { return !n.Set }

// RPCStartupCommand represents a single entry in the `rpc_startup` TOML
// array. Each entry MUST have a `command` field; the remaining fields are
// command-specific parameters that are forwarded verbatim to the RPC layer
// (rippled treats this section the same way), so they stay in an open
// `Params` map rather than being modelled as a fixed struct.
type RPCStartupCommand struct {
	// Command is the RPC method name (required, validated by ValidateConfig).
	Command string
	// Params holds all other key/value pairs for this entry.
	Params map[string]any
}

// AsMap returns a map containing the command plus all params, preserving
// the legacy `[]map[string]interface{}` shape for downstream consumers
// that need to forward the data unchanged.
func (c RPCStartupCommand) AsMap() map[string]any {
	out := make(map[string]any, len(c.Params)+1)
	for k, v := range c.Params {
		out[k] = v
	}
	if c.Command != "" {
		out["command"] = c.Command
	}
	return out
}

// configDecodeHook returns a mapstructure decode hook that converts raw
// TOML scalars (int64 / string) and maps into the typed union types
// declared above. Without this hook viper would fail to assign an int64
// into a struct field.
func configDecodeHook() mapstructure.DecodeHookFunc {
	ledgerHistoryType := reflect.TypeOf(LedgerHistory{})
	fetchDepthType := reflect.TypeOf(FetchDepth{})
	networkIDType := reflect.TypeOf(NetworkID{})
	rpcStartupCmdType := reflect.TypeOf(RPCStartupCommand{})

	return func(from, to reflect.Type, data any) (any, error) {
		switch to {
		case ledgerHistoryType:
			return decodeLedgerHistory(data)
		case fetchDepthType:
			return decodeFetchDepth(data)
		case networkIDType:
			return decodeNetworkID(data)
		case rpcStartupCmdType:
			return decodeRPCStartupCommand(data)
		}
		return data, nil
	}
}

func decodeLedgerHistory(data any) (LedgerHistory, error) {
	switch v := data.(type) {
	case int:
		return LedgerHistory{Set: true, Count: v}, nil
	case int64:
		return LedgerHistory{Set: true, Count: int(v)}, nil
	case uint64:
		return LedgerHistory{Set: true, Count: int(v)}, nil
	case float64:
		return LedgerHistory{Set: true, Count: int(v)}, nil
	case string:
		switch {
		case strings.EqualFold(v, "full"):
			return LedgerHistory{Set: true, Full: true}, nil
		case strings.EqualFold(v, "none"):
			return LedgerHistory{Set: true, Count: 0}, nil
		}
		if n, err := strconv.Atoi(v); err == nil {
			return LedgerHistory{Set: true, Count: n}, nil
		}
		return LedgerHistory{}, fmt.Errorf("invalid ledger_history value: %q (expected integer, \"full\", or \"none\")", v)
	case nil:
		return LedgerHistory{}, nil
	default:
		return LedgerHistory{}, fmt.Errorf("invalid ledger_history type: %T", data)
	}
}

func decodeFetchDepth(data any) (FetchDepth, error) {
	switch v := data.(type) {
	case int:
		return FetchDepth{Set: true, Count: v}, nil
	case int64:
		return FetchDepth{Set: true, Count: int(v)}, nil
	case uint64:
		return FetchDepth{Set: true, Count: int(v)}, nil
	case float64:
		return FetchDepth{Set: true, Count: int(v)}, nil
	case string:
		switch {
		case strings.EqualFold(v, "full"):
			return FetchDepth{Set: true, Full: true}, nil
		case strings.EqualFold(v, "none"):
			return FetchDepth{Set: true, Count: 0}, nil
		}
		if n, err := strconv.Atoi(v); err == nil {
			return FetchDepth{Set: true, Count: n}, nil
		}
		return FetchDepth{}, fmt.Errorf("invalid fetch_depth value: %q (expected integer, \"full\", or \"none\")", v)
	case nil:
		return FetchDepth{}, nil
	default:
		return FetchDepth{}, fmt.Errorf("invalid fetch_depth type: %T", data)
	}
}

func decodeNetworkID(data any) (NetworkID, error) {
	switch v := data.(type) {
	case int:
		return NetworkID{Set: true, ID: v}, nil
	case int64:
		return NetworkID{Set: true, ID: int(v)}, nil
	case uint64:
		return NetworkID{Set: true, ID: int(v)}, nil
	case float64:
		return NetworkID{Set: true, ID: int(v)}, nil
	case string:
		// Rippled's named-string aliases are case-sensitive (Config.cpp:525-530
		// uses operator==). Other strings fall through to a uint32 lexical_cast.
		switch v {
		case "main", "testnet", "devnet":
			return NetworkID{Set: true, Name: v}, nil
		}
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return NetworkID{Set: true, ID: int(n)}, nil
		}
		return NetworkID{Set: true, Name: v}, nil
	case nil:
		return NetworkID{}, nil
	default:
		return NetworkID{}, fmt.Errorf("invalid network_id type: %T", data)
	}
}

func decodeRPCStartupCommand(data any) (RPCStartupCommand, error) {
	m, ok := data.(map[string]any)
	if !ok {
		return RPCStartupCommand{}, fmt.Errorf("invalid rpc_startup entry type: %T (expected table)", data)
	}
	cmd := RPCStartupCommand{Params: make(map[string]any, len(m))}
	for k, v := range m {
		if k == "command" {
			s, isStr := v.(string)
			if !isStr {
				return RPCStartupCommand{}, fmt.Errorf("rpc_startup `command` must be a string, got %T", v)
			}
			cmd.Command = s
			continue
		}
		cmd.Params[k] = v
	}
	if cmd.Command == "" {
		return RPCStartupCommand{}, fmt.Errorf("rpc_startup entry missing 'command' field")
	}
	return cmd, nil
}

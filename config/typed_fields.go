package config

import (
	"fmt"
	"reflect"

	"github.com/go-viper/mapstructure/v2"
)

// LedgerHistory is a typed union for the `ledger_history` TOML key.
// TOML accepts an integer (number of ledgers to keep) or one of the
// strings "full" or "none".
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
// the explicit count, -1 for "full", or 0 for "none".
func (lh LedgerHistory) Value() int {
	if lh.Full {
		return -1
	}
	return lh.Count
}

// FetchDepth is a typed union for the `fetch_depth` TOML key.
// TOML accepts an integer or the string "full".
type FetchDepth struct {
	Set   bool
	Full  bool
	Count int
}

func (fd FetchDepth) IsZero() bool { return !fd.Set }

// Value returns the integer representation: explicit count or -1 for "full".
func (fd FetchDepth) Value() int {
	if fd.Full {
		return -1
	}
	return fd.Count
}

// NetworkID is a typed union for the `network_id` TOML key.
// TOML accepts an integer (network ID) or one of the named strings
// "main", "testnet", "devnet".
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
		switch v {
		case "full":
			return LedgerHistory{Set: true, Full: true}, nil
		case "none":
			return LedgerHistory{Set: true, Count: 0}, nil
		default:
			return LedgerHistory{}, fmt.Errorf("invalid ledger_history value: %q (expected integer, \"full\", or \"none\")", v)
		}
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
		if v == "full" {
			return FetchDepth{Set: true, Full: true}, nil
		}
		return FetchDepth{}, fmt.Errorf("invalid fetch_depth value: %q (expected integer or \"full\")", v)
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
	return cmd, nil
}

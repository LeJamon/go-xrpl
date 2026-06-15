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
// the explicit count, or a sufficiently large sentinel (math.MaxInt32) for
// "full". Rippled uses std::numeric_limits<uint32_t>::max() (Config.cpp:654-655);
// math.MaxInt32 is not numerically equal but is large enough to fire the
// downstream `online_delete < ledger_history` comparison the same way as
// rippled — see SHAMapStoreImp.cpp:148-154.
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
//
// The decoder clamps any explicit count below 10 up to 10 to mirror
// rippled's hard floor (Config.cpp:671-672), so `Count` is the value
// that callers should observe and `Value()` is a thin convenience over
// the same number.
type FetchDepth struct {
	Set   bool
	Full  bool
	Count int
}

// IsZero reports whether fetch_depth was left unset (so the default applies).
func (fd FetchDepth) IsZero() bool { return !fd.Set }

// Value returns the integer representation: math.MaxInt32 for "full",
// otherwise `Count` (which the decoder has already clamped to >= 10).
func (fd FetchDepth) Value() int {
	if fd.Full {
		return math.MaxInt32
	}
	return fd.Count
}

// NetworkID is a typed union for the `network_id` TOML key.
// TOML accepts an integer (network ID) or one of the named strings
// "main", "testnet", "devnet". A digit-string (e.g. "21338") is parsed
// numerically to match rippled's beast::lexicalCastThrow<uint32_t>
// fallback in Config.cpp:531-532. Any other string (including empty) is
// rejected at decode time, mirroring rippled's lexical-cast throw.
type NetworkID struct {
	Set  bool
	ID   int
	Name string
}

// IsZero reports whether network_id was left unset (so the default applies).
func (n NetworkID) IsZero() bool { return !n.Set }

// configDecodeHook returns a mapstructure decode hook that converts raw
// TOML scalars (int64 / string) into the typed union types declared
// above. Without this hook viper would fail to assign an int64 into a
// struct field.
func configDecodeHook() mapstructure.DecodeHookFunc {
	ledgerHistoryType := reflect.TypeFor[LedgerHistory]()
	fetchDepthType := reflect.TypeFor[FetchDepth]()
	networkIDType := reflect.TypeFor[NetworkID]()

	return func(from, to reflect.Type, data any) (any, error) {
		switch to {
		case ledgerHistoryType:
			set, full, count, err := decodeKeywordOrUint32("ledger_history", data, 0)
			if err != nil {
				return LedgerHistory{}, err
			}
			return LedgerHistory{Set: set, Full: full, Count: count}, nil
		case fetchDepthType:
			set, full, count, err := decodeKeywordOrUint32("fetch_depth", data, fetchDepthMin)
			if err != nil {
				return FetchDepth{}, err
			}
			return FetchDepth{Set: set, Full: full, Count: count}, nil
		case networkIDType:
			return decodeNetworkID(data)
		}
		return data, nil
	}
}

// asUint32 normalises a numeric `data` value into a non-negative int that
// fits in a uint32, mirroring rippled's beast::lexicalCastThrow<uint32_t>
// rejection of negatives and out-of-range values. Returns ok=false when
// `data` is not a recognised numeric type.
func asUint32(field string, data any) (n int, ok bool, err error) {
	checkRange := func(v int64) (int, error) {
		if v < 0 || v > math.MaxUint32 {
			return 0, fmt.Errorf("invalid %s value: %d (out of range [0, %d])", field, v, uint32(math.MaxUint32))
		}
		return int(v), nil
	}
	switch v := data.(type) {
	case int:
		out, err := checkRange(int64(v))
		return out, true, err
	case int64:
		out, err := checkRange(v)
		return out, true, err
	case uint64:
		if v > math.MaxUint32 {
			return 0, true, fmt.Errorf("invalid %s value: %d (out of range [0, %d])", field, v, uint32(math.MaxUint32))
		}
		return int(v), true, nil
	case float64:
		if v < 0 || v > math.MaxUint32 || v != math.Trunc(v) {
			return 0, true, fmt.Errorf("invalid %s value: %v (must be a non-negative integer ≤ %d)", field, v, uint32(math.MaxUint32))
		}
		return int(v), true, nil
	}
	return 0, false, nil
}

// decodeKeywordOrUint32 decodes the shared ledger_history / fetch_depth
// value shape: a uint32 count, or "full" / "none" (case-insensitive).
// Explicit counts below clampMin are raised to clampMin (0 disables
// clamping).
func decodeKeywordOrUint32(field string, data any, clampMin int) (set, full bool, count int, err error) {
	clamp := func(n int) int {
		if n < clampMin {
			return clampMin
		}
		return n
	}
	if n, ok, err := asUint32(field, data); ok {
		if err != nil {
			return false, false, 0, err
		}
		return true, false, clamp(n), nil
	}
	switch v := data.(type) {
	case string:
		switch {
		case strings.EqualFold(v, "full"):
			return true, true, 0, nil
		case strings.EqualFold(v, "none"):
			return true, false, clamp(0), nil
		}
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return true, false, clamp(int(n)), nil
		}
		return false, false, 0, fmt.Errorf("invalid %s value: %q (expected integer, \"full\", or \"none\")", field, v)
	case nil:
		return false, false, 0, nil
	default:
		return false, false, 0, fmt.Errorf("invalid %s type: %T", field, data)
	}
}

func decodeNetworkID(data any) (NetworkID, error) {
	if n, ok, err := asUint32("network_id", data); ok {
		if err != nil {
			return NetworkID{}, err
		}
		return NetworkID{Set: true, ID: n}, nil
	}
	switch v := data.(type) {
	case string:
		// Rippled's named-string aliases are case-sensitive (Config.cpp:525-530
		// uses operator==). Other strings fall through to a uint32 lexical_cast,
		// which throws on empty / non-digit / out-of-range input.
		if _, ok := networkIDByName[v]; ok {
			return NetworkID{Set: true, Name: v}, nil
		}
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return NetworkID{Set: true, ID: int(n)}, nil
		}
		return NetworkID{}, fmt.Errorf("invalid network_id value: %q (expected integer or one of \"main\", \"testnet\", \"devnet\")", v)
	case nil:
		return NetworkID{}, nil
	default:
		return NetworkID{}, fmt.Errorf("invalid network_id type: %T", data)
	}
}

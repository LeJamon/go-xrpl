package state

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
)

func decodeLedgerHex(field, value string, dst []byte) error {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return fmt.Errorf("%s: invalid hex: %w", field, err)
	}
	if len(decoded) != len(dst) {
		return fmt.Errorf("%s: decoded length %d, want %d", field, len(decoded), len(dst))
	}
	copy(dst, decoded)
	return nil
}

func parseLedgerUint64(field, value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid UInt64 %q: %w", field, value, err)
	}
	return parsed, nil
}

func decodeLedgerAccount(field, value string) ([20]byte, error) {
	account, err := DecodeAccountID(value)
	if err != nil {
		return [20]byte{}, fmt.Errorf("%s: invalid account: %w", field, err)
	}
	return account, nil
}

func decodeLedgerAmount(field string, value any) (Amount, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return Amount{}, fmt.Errorf("%s: marshal decoded amount: %w", field, err)
	}
	amount, err := AmountFromJSON(raw)
	if err != nil {
		return Amount{}, fmt.Errorf("%s: invalid amount: %w", field, err)
	}
	return amount, nil
}

func decodeNativeLedgerBalance(field string, value any) (uint64, error) {
	drops, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("%s: decoded XRP amount has type %T", field, value)
	}
	balance, err := strconv.ParseUint(drops, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid XRP drops %q: %w", field, drops, err)
	}
	return balance, nil
}

func decodedFieldUnchanged(fields map[string]any, field string, value any) bool {
	decoded, ok := fields[field]
	return ok && reflect.DeepEqual(decoded, value)
}

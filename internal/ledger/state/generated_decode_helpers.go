package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
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

func decodeLedgerAmount(field string, value any) (amount Amount, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			amount = Amount{}
			err = fmt.Errorf("%s: invalid amount: %v", field, recovered)
		}
	}()

	amount, err = decodeLedgerAmountValue(value)
	if err != nil {
		return Amount{}, fmt.Errorf("%s: invalid amount: %w", field, err)
	}
	return amount, nil
}

func decodeLedgerAmountValue(value any) (Amount, error) {
	decoded, ok := value.(ledgerfields.AmountValue)
	if !ok {
		var err error
		decoded, err = ledgerfields.ParseAmountValue(value)
		if err != nil {
			return Amount{}, err
		}
	}
	return decodeLedgerAmountTyped(decoded.Value, decoded.Currency, decoded.Issuer, decoded.MPTIssuanceID)
}

func decodeLedgerAmountTyped(value, currency, issuer, mptID string) (Amount, error) {
	if mptID != "" {
		if currency != "" || issuer != "" {
			return Amount{}, errors.New("Invalid Asset's Json specification")
		}
		id, err := hex.DecodeString(mptID)
		if err != nil || len(id) != 24 {
			return Amount{}, errors.New("invalid MPTokenIssuanceID")
		}
		parts, err := amountValueParts(value, false, true)
		if err != nil {
			return Amount{}, err
		}
		units, err := integralAmount(parts, maxMPTAmount, "MPT amount out of range")
		if err != nil {
			return Amount{}, err
		}
		return NewMPTAmountWithIssuanceID(units, "", strings.ToUpper(mptID)), nil
	}
	if currency == "" && issuer == "" {
		parts, err := amountValueParts(value, false, true)
		if err != nil {
			return Amount{}, err
		}
		drops, err := integralAmount(parts, maxNativeAmount, "Native currency amount out of range")
		if err != nil {
			return Amount{}, err
		}
		return NewXRPAmountFromInt(drops), nil
	}
	if currency == "" || issuer == "" {
		return Amount{}, errors.New("Invalid Asset's Json specification")
	}
	if currency == "XRP" {
		return Amount{}, errors.New("XRP may not be specified as an object")
	}
	if currency != "1" {
		cur, err := currencyFromJSONString(currency)
		if err != nil {
			return Amount{}, err
		}
		if cur == [20]byte{} {
			return Amount{}, errors.New("invalid issuer")
		}
	}
	issuerID, err := issuerFromJSONString(issuer)
	if err != nil {
		return Amount{}, err
	}
	parts, err := amountValueParts(value, false, false)
	if err != nil {
		return Amount{}, err
	}
	mantissa, exponent := reduceIOUMantissa(parts.mantissa, parts.exponent)
	if parts.negative {
		mantissa = -mantissa
	}
	return NewIssuedAmountFromValue(mantissa, exponent, currency, EncodeAccountIDSafe(issuerID)), nil
}

func nonNegativeNativeDrops(field string, amount Amount) (uint64, error) {
	if !amount.IsNative() {
		return 0, fmt.Errorf("%s: expected native XRP amount", field)
	}
	drops := amount.Drops()
	if drops < 0 {
		return 0, fmt.Errorf("%s: negative XRP amount %d", field, drops)
	}
	return uint64(drops), nil
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

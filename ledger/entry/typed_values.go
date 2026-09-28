package entry

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// AmountValue is the typed JSON shape used by an XRPL Amount field.
//
// A value with no asset identity fields is XRP. Currency and Issuer together
// identify an issued currency, while MPTIssuanceID identifies an MPT. Value is
// kept as the exact decimal string emitted by the binary codec.
type AmountValue struct {
	Value         string
	Currency      string
	Issuer        string
	MPTIssuanceID string
}

// ParseAmountValue converts a binary codec's decoded amount to its typed wire
// representation. It checks the asset shape without rounding the decimal value.
func ParseAmountValue(value any) (AmountValue, error) {
	return amountValueFromAny(value, "Amount", false)
}

func amountValueFromAny(value any, field string, xrpOnly bool) (AmountValue, error) {
	if value == nil {
		return AmountValue{}, nil
	}
	switch v := value.(type) {
	case string:
		return AmountValue{Value: v}, nil
	case map[string]any:
		result := AmountValue{}
		rawValue, ok := v["value"]
		if !ok {
			return result, fmt.Errorf("ledgerfields: %s: amount is missing value", field)
		}
		valueString, ok := rawValue.(string)
		if !ok {
			return result, fmt.Errorf("ledgerfields: %s: amount value has type %T, want string", field, rawValue)
		}
		result.Value = valueString

		rawID, hasID := v["mpt_issuance_id"]
		if hasID {
			id, ok := rawID.(string)
			if !ok {
				return result, fmt.Errorf("ledgerfields: %s: MPT issuance ID has type %T, want string", field, rawID)
			}
			if id == "" {
				return result, fmt.Errorf("ledgerfields: %s: MPT issuance ID is empty", field)
			}
			if _, hasCurrency := v["currency"]; hasCurrency {
				return result, fmt.Errorf("ledgerfields: %s: MPT amount cannot carry currency", field)
			}
			if _, hasIssuer := v["issuer"]; hasIssuer {
				return result, fmt.Errorf("ledgerfields: %s: MPT amount cannot carry issuer", field)
			}
			result.MPTIssuanceID = id
			if xrpOnly {
				return result, fmt.Errorf("ledgerfields: %s: non-XRP amount is not allowed", field)
			}
			return result, nil
		}

		rawCurrency, hasCurrency := v["currency"]
		rawIssuer, hasIssuer := v["issuer"]
		if !hasCurrency && !hasIssuer {
			if xrpOnly {
				return result, fmt.Errorf("ledgerfields: %s: XRP amount must be a string", field)
			}
			return result, fmt.Errorf("ledgerfields: %s: amount has no asset identity", field)
		}
		if !hasCurrency || !hasIssuer {
			return result, fmt.Errorf("ledgerfields: %s: issued amount requires currency and issuer", field)
		}
		currency, ok := rawCurrency.(string)
		if !ok {
			return result, fmt.Errorf("ledgerfields: %s: currency has type %T, want string", field, rawCurrency)
		}
		issuer, ok := rawIssuer.(string)
		if !ok {
			return result, fmt.Errorf("ledgerfields: %s: issuer has type %T, want string", field, rawIssuer)
		}
		if currency == "" || issuer == "" {
			return result, fmt.Errorf("ledgerfields: %s: issued amount requires non-empty currency and issuer", field)
		}
		result.Currency = currency
		result.Issuer = issuer
		if xrpOnly {
			return result, fmt.Errorf("ledgerfields: %s: non-XRP amount is not allowed", field)
		}
		return result, nil
	default:
		return AmountValue{}, fmt.Errorf("ledgerfields: %s: amount has unsupported type %T", field, value)
	}
}

func amountValueToAny(value AmountValue, field string, xrpOnly bool) (any, error) {
	if value.MPTIssuanceID != "" {
		if value.Currency != "" || value.Issuer != "" {
			return nil, fmt.Errorf("ledgerfields: %s: MPT amount cannot carry currency or issuer", field)
		}
		if value.Value == "" {
			return nil, fmt.Errorf("ledgerfields: %s: amount value is empty", field)
		}
		if xrpOnly {
			return nil, fmt.Errorf("ledgerfields: %s: non-XRP amount is not allowed", field)
		}
		return map[string]any{
			"value":           value.Value,
			"mpt_issuance_id": value.MPTIssuanceID,
		}, nil
	}
	if value.Currency != "" || value.Issuer != "" {
		if value.Currency == "" || value.Issuer == "" {
			return nil, fmt.Errorf("ledgerfields: %s: issued amount requires currency and issuer", field)
		}
		if value.Value == "" {
			return nil, fmt.Errorf("ledgerfields: %s: amount value is empty", field)
		}
		if xrpOnly {
			return nil, fmt.Errorf("ledgerfields: %s: non-XRP amount is not allowed", field)
		}
		return map[string]any{
			"value":    value.Value,
			"currency": value.Currency,
			"issuer":   value.Issuer,
		}, nil
	}
	if value.Value == "" {
		value.Value = "0"
	}
	return value.Value, nil
}

func hashValueFromString(value, field string, length int) ([]byte, error) {
	if value == "" || value == "0" {
		return make([]byte, length), nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("ledgerfields: %s: invalid hexadecimal value: %w", field, err)
	}
	if len(decoded) != length {
		return nil, fmt.Errorf("ledgerfields: %s: invalid length %d, want %d bytes", field, len(decoded), length)
	}
	return decoded, nil
}

func hashValueToString(value []byte) string {
	return strings.ToUpper(hex.EncodeToString(value))
}

func blobValueFromString(value, field string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if len(value)%2 != 0 {
		value = "0" + value
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("ledgerfields: %s: invalid hexadecimal value: %w", field, err)
	}
	return decoded, nil
}

func blobValueToString(value []byte) string {
	return strings.ToUpper(hex.EncodeToString(value))
}

func accountIDValueFromString(value, field string) ([20]byte, error) {
	var result [20]byte
	if value == "" {
		return result, nil
	}
	var accountID []byte
	var err error
	switch {
	case strings.HasPrefix(value, "r"):
		_, accountID, err = addresscodec.DecodeClassicAddressToAccountID(value)
	case strings.HasPrefix(value, "X"), strings.HasPrefix(value, "T"):
		accountID, _, _, err = addresscodec.DecodeXAddress(value)
	default:
		err = fmt.Errorf("invalid account address format")
	}
	if err != nil {
		return result, fmt.Errorf("ledgerfields: %s: invalid account ID: %w", field, err)
	}
	if len(accountID) != len(result) {
		return result, fmt.Errorf("ledgerfields: %s: invalid account ID length %d, want %d", field, len(accountID), len(result))
	}
	copy(result[:], accountID)
	return result, nil
}

func accountIDValueToString(value [20]byte) (string, error) {
	address, err := addresscodec.EncodeAccountIDToClassicAddress(value[:])
	if err != nil {
		return "", err
	}
	return address, nil
}

func uint64ValueFromString(value, field string, baseTen bool) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	base := 16
	if baseTen {
		base = 10
	}
	parsed, err := strconv.ParseUint(value, base, 64)
	if err != nil {
		return 0, fmt.Errorf("ledgerfields: %s: invalid UInt64 value %q: %w", field, value, err)
	}
	return parsed, nil
}

func uint64ValueToString(value uint64, baseTen bool) string {
	base := 16
	if baseTen {
		base = 10
	}
	return strconv.FormatUint(value, base)
}

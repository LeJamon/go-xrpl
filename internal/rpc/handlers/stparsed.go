package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type innerFieldStyle uint8

const (
	innerRequired innerFieldStyle = iota
	innerOptional
	innerDefault
)

var innerObjectTemplates = map[string]map[string]innerFieldStyle{
	"SignerEntry": {
		"Account":       innerRequired,
		"SignerWeight":  innerRequired,
		"WalletLocator": innerOptional,
	},
	"Signer": {
		"Account":       innerRequired,
		"SigningPubKey": innerRequired,
		"TxnSignature":  innerRequired,
	},
	"Majority": {
		"Amendment": innerRequired,
		"CloseTime": innerRequired,
	},
	"DisabledValidator": {
		"PublicKey":           innerRequired,
		"FirstLedgerSequence": innerRequired,
	},
	"NFToken": {
		"NFTokenID": innerRequired,
		"URI":       innerOptional,
	},
	"VoteEntry": {
		"Account":    innerRequired,
		"TradingFee": innerDefault,
		"VoteWeight": innerRequired,
	},
	"AuctionSlot": {
		"Account":       innerRequired,
		"Expiration":    innerRequired,
		"DiscountedFee": innerDefault,
		"Price":         innerRequired,
		"AuthAccounts":  innerOptional,
	},
	"XChainClaimAttestationCollectionElement": {
		"AttestationSignerAccount": innerRequired,
		"PublicKey":                innerRequired,
		"Signature":                innerRequired,
		"Amount":                   innerRequired,
		"Account":                  innerRequired,
		"AttestationRewardAccount": innerRequired,
		"WasLockingChainSend":      innerRequired,
		"XChainClaimID":            innerRequired,
		"Destination":              innerOptional,
	},
	"XChainCreateAccountAttestationCollectionElement": {
		"AttestationSignerAccount": innerRequired,
		"PublicKey":                innerRequired,
		"Signature":                innerRequired,
		"Amount":                   innerRequired,
		"Account":                  innerRequired,
		"AttestationRewardAccount": innerRequired,
		"WasLockingChainSend":      innerRequired,
		"XChainAccountCreateCount": innerRequired,
		"Destination":              innerRequired,
		"SignatureReward":          innerRequired,
	},
	"XChainClaimProofSig": {
		"AttestationSignerAccount": innerRequired,
		"PublicKey":                innerRequired,
		"Amount":                   innerRequired,
		"AttestationRewardAccount": innerRequired,
		"WasLockingChainSend":      innerRequired,
		"Destination":              innerOptional,
	},
	"XChainCreateAccountProofSig": {
		"AttestationSignerAccount": innerRequired,
		"PublicKey":                innerRequired,
		"Amount":                   innerRequired,
		"SignatureReward":          innerRequired,
		"AttestationRewardAccount": innerRequired,
		"WasLockingChainSend":      innerRequired,
		"Destination":              innerRequired,
	},
	"AuthAccount": {
		"Account": innerRequired,
	},
	"PriceData": {
		"BaseAsset":  innerRequired,
		"QuoteAsset": innerRequired,
		"AssetPrice": innerOptional,
		"Scale":      innerDefault,
	},
	"Credential": {
		"Issuer":         innerRequired,
		"CredentialType": innerRequired,
	},
	"Permission": {
		"PermissionValue": innerRequired,
	},
	"BatchSigner": {
		"Account":       innerRequired,
		"SigningPubKey": innerOptional,
		"TxnSignature":  innerOptional,
		"Signers":       innerOptional,
	},
	"Book": {
		"BookDirectory": innerRequired,
		"BookNode":      innerRequired,
	},
	"CounterpartySignature": {
		"SigningPubKey": innerOptional,
		"TxnSignature":  innerOptional,
		"Signers":       innerOptional,
	},
	"SponsorSignature": {
		"SigningPubKey": innerOptional,
		"TxnSignature":  innerOptional,
		"Signers":       innerOptional,
	},
}

func serializedFieldParseMessage(value any, path string, defs *binarycodecdefs.Definitions) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fieldValue := object[name]
		field, err := defs.FieldInstanceByName(name)
		if err != nil {
			return fmt.Sprintf("Field '%s.%s' is unknown.", path, name)
		}

		fieldPath := path + "." + name
		switch field.Type {
		case "STObject":
			child, ok := fieldValue.(map[string]any)
			if !ok {
				return fmt.Sprintf("Field '%s' is not a JSON object.", fieldPath)
			}
			if message := serializedFieldParseMessage(child, fieldPath, defs); message != "" {
				return message
			}
			if !meetsInnerObjectTemplate(name, child) {
				return fmt.Sprintf("Object '%s' contents did not meet requirements for that type.", name)
			}
		case "STArray":
			if fieldValue == nil {
				continue
			}
			items, ok := fieldValue.([]any)
			if !ok {
				return fmt.Sprintf("Field '%s' is not a JSON array.", fieldPath)
			}
			for i, item := range items {
				itemObject, ok := item.(map[string]any)
				if !ok || len(itemObject) != 1 {
					return fmt.Sprintf(
						"Field '%s[%d]' must be an object with a single key/object value.",
						fieldPath, i)
				}
				var wrapperName string
				var wrapperValue any
				for wrapperName, wrapperValue = range itemObject {
				}
				if _, err := defs.FieldInstanceByName(wrapperName); err != nil {
					return fmt.Sprintf("Field '%s.%s' is unknown.", fieldPath, wrapperName)
				}
				wrapperObject, ok := wrapperValue.(map[string]any)
				if !ok {
					return fmt.Sprintf(
						"Field '%s[%d]' must be an object with a single key/object value.",
						fieldPath, i)
				}
				itemPath := fmt.Sprintf("%s.[%d].%s", fieldPath, i, wrapperName)
				if message := serializedFieldParseMessage(wrapperObject, itemPath, defs); message != "" {
					return fmt.Sprintf("Error at '%s'. %s", itemPath, message)
				}
				if !meetsInnerObjectTemplate(wrapperName, wrapperObject) {
					return fmt.Sprintf(
						"Error at '%s'. Object '%s' contents did not meet requirements for that type.",
						itemPath, wrapperName)
				}
			}
		default:
			normalized, message := validateSerializedLeaf(name, field.Type, fieldValue, fieldPath, defs)
			if message != "" {
				return message
			}
			object[name] = normalized
		}
	}

	return ""
}

func validateSerializedLeaf(
	name string,
	fieldType string,
	value any,
	path string,
	defs *binarycodecdefs.Definitions,
) (any, string) {
	switch fieldType {
	case "UInt8":
		return validateUInt8(name, value, path, defs)
	case "UInt16":
		return validateUInt16(value, path, defs)
	case "UInt32":
		return validateUInt32(name, value, path, defs)
	case "UInt64":
		return validateUInt64(name, value, path)
	case "Int32":
		return validateInt32(value, path)
	case "Hash128":
		return validateHash(value, path, 16)
	case "Hash160":
		return validateHash(value, path, 20)
	case "Hash192":
		return validateHash(value, path, 24)
	case "Hash256":
		return validateHash(value, path, 32)
	case "Blob":
		return validateBlob(value, path)
	case "AccountID":
		return validateAccount(value, path)
	case "Vector256", "PathSet":
		if value == nil {
			return value, ""
		}
		items, ok := value.([]any)
		if !ok {
			return value, fmt.Sprintf("Field '%s' must be a JSON array.", path)
		}
		if len(items) == 0 {
			return value, ""
		}
	}

	serializedType := binarycodectypes.SerializedTypeFor(fieldType)
	if serializedType == nil {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if _, err := serializedType.FromJSON(value); err != nil {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateUInt8(name string, value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		if name == "TransactionResult" {
			if code, err := defs.TransactionResultCode(text); err == nil {
				return uint8(code), ""
			}
		}
		if !isUnsignedDecimal(text) {
			return value, fmt.Sprintf("Field '%s' has bad type.", path)
		}
		n, err := strconv.ParseUint(text, 10, 8)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint8(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint8 {
		return value, fmt.Sprintf("Field '%s' is out of range.", path)
	}
	return uint8(n), ""
}

func validateUInt16(value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		if code, err := defs.TransactionTypeCode(text); err == nil {
			return uint16(code), ""
		}
		if code, err := defs.LedgerEntryTypeCode(text); err == nil {
			return uint16(code), ""
		}
		n, err := strconv.ParseUint(text, 10, 16)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint16(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint16 {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return uint16(n), ""
}

func validateUInt32(name string, value any, path string, defs *binarycodecdefs.Definitions) (any, string) {
	if text, ok := value.(string); ok {
		if name == "PermissionValue" {
			if code, err := defs.DelegatablePermissionValue(text); err == nil {
				return uint32(code), ""
			}
		}
		n, err := strconv.ParseUint(text, 10, 32)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return uint32(n), ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative || n > math.MaxUint32 {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return uint32(n), ""
}

func validateUInt64(name string, value any, path string) (any, string) {
	if text, ok := value.(string); ok {
		base := 16
		if binarycodecdefs.IsBaseTenUInt64FieldName(name) {
			base = 10
		}
		n, err := strconv.ParseUint(text, base, 64)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return n, ""
	}

	negative, n, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if negative {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return n, ""
}

func validateInt32(value any, path string) (any, string) {
	if text, ok := value.(string); ok {
		n, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return value, fmt.Sprintf("Field '%s' has invalid data.", path)
		}
		return int32(n), ""
	}

	negative, magnitude, ok := integerNumericValue(value)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if (!negative && magnitude > math.MaxInt32) || (negative && magnitude > uint64(math.MaxInt32)+1) {
		return value, fmt.Sprintf("Field '%s' is out of range.", path)
	}
	n := int64(magnitude)
	if negative {
		n = -n
	}
	return int32(n), ""
}

func validateHash(value any, path string, size int) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if text == "" {
		return value, ""
	}
	decoded, err := hex.DecodeString(text)
	if err != nil || len(decoded) != size {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateBlob(value any, path string) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if _, err := hex.DecodeString(text); err != nil {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func validateAccount(value any, path string) (any, string) {
	text, ok := value.(string)
	if !ok {
		return value, fmt.Sprintf("Field '%s' has bad type.", path)
	}
	if len(text) == 40 {
		if decoded, err := hex.DecodeString(text); err == nil && len(decoded) == 20 {
			return value, ""
		}
	}
	if !types.IsValidClassicAddress(text) {
		return value, fmt.Sprintf("Field '%s' has invalid data.", path)
	}
	return value, ""
}

func integerNumericValue(value any) (negative bool, magnitude uint64, ok bool) {
	switch n := value.(type) {
	case json.Number:
		if strings.ContainsAny(n.String(), ".eE") {
			return false, 0, false
		}
		if strings.HasPrefix(n.String(), "-") {
			parsed, err := strconv.ParseInt(n.String(), 10, 32)
			if err != nil {
				return false, 0, false
			}
			return parsed < 0, uint64(-parsed), true
		}
		parsed, err := strconv.ParseUint(n.String(), 10, 32)
		return false, parsed, err == nil
	case int:
		return signedMagnitude(int64(n))
	case int8:
		return signedMagnitude(int64(n))
	case int16:
		return signedMagnitude(int64(n))
	case int32:
		return signedMagnitude(int64(n))
	case int64:
		return signedMagnitude(n)
	case uint:
		return false, uint64(n), true
	case uint8:
		return false, uint64(n), true
	case uint16:
		return false, uint64(n), true
	case uint32:
		return false, uint64(n), true
	case uint64:
		return false, n, true
	}
	return false, 0, false
}

func signedMagnitude(value int64) (negative bool, magnitude uint64, ok bool) {
	if value < 0 {
		return true, uint64(-(value + 1)) + 1, true
	}
	return false, uint64(value), true
}

func isUnsignedDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func meetsInnerObjectTemplate(name string, object map[string]any) bool {
	template, ok := innerObjectTemplates[name]
	if !ok {
		return true
	}
	for fieldName, style := range template {
		value, present := object[fieldName]
		if style == innerRequired && !present {
			return false
		}
		if style == innerDefault && present && isDefaultSerializedValue(value) {
			return false
		}
	}
	for fieldName := range object {
		if _, ok := template[fieldName]; !ok {
			return false
		}
	}
	return true
}

func isDefaultSerializedValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return typed == "" || typed == "0"
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	if _, magnitude, ok := integerNumericValue(value); ok {
		return magnitude == 0
	}
	return false
}

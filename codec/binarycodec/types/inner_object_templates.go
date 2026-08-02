package types

import (
	"errors"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

type innerObjectFieldStyle uint8

const (
	innerRequired innerObjectFieldStyle = iota
	innerOptional
	innerDefault
)

type innerObjectField struct {
	name  string
	style innerObjectFieldStyle
}

var innerObjectTemplates = map[string][]innerObjectField{
	"SignerEntry": {
		{name: "Account", style: innerRequired},
		{name: "SignerWeight", style: innerRequired},
		{name: "WalletLocator", style: innerOptional},
	},
	"Signer": {
		{name: "Account", style: innerRequired},
		{name: "SigningPubKey", style: innerRequired},
		{name: "TxnSignature", style: innerRequired},
	},
	"Majority": {
		{name: "Amendment", style: innerRequired},
		{name: "CloseTime", style: innerRequired},
	},
	"DisabledValidator": {
		{name: "PublicKey", style: innerRequired},
		{name: "FirstLedgerSequence", style: innerRequired},
	},
	"NFToken": {
		{name: "NFTokenID", style: innerRequired},
		{name: "URI", style: innerOptional},
	},
	"VoteEntry": {
		{name: "Account", style: innerRequired},
		{name: "TradingFee", style: innerDefault},
		{name: "VoteWeight", style: innerRequired},
	},
	"AuctionSlot": {
		{name: "Account", style: innerRequired},
		{name: "Expiration", style: innerRequired},
		{name: "DiscountedFee", style: innerDefault},
		{name: "Price", style: innerRequired},
		{name: "AuthAccounts", style: innerOptional},
	},
	"XChainClaimAttestationCollectionElement": {
		{name: "AttestationSignerAccount", style: innerRequired},
		{name: "PublicKey", style: innerRequired},
		{name: "Signature", style: innerRequired},
		{name: "Amount", style: innerRequired},
		{name: "Account", style: innerRequired},
		{name: "AttestationRewardAccount", style: innerRequired},
		{name: "WasLockingChainSend", style: innerRequired},
		{name: "XChainClaimID", style: innerRequired},
		{name: "Destination", style: innerOptional},
	},
	"XChainCreateAccountAttestationCollectionElement": {
		{name: "AttestationSignerAccount", style: innerRequired},
		{name: "PublicKey", style: innerRequired},
		{name: "Signature", style: innerRequired},
		{name: "Amount", style: innerRequired},
		{name: "Account", style: innerRequired},
		{name: "AttestationRewardAccount", style: innerRequired},
		{name: "WasLockingChainSend", style: innerRequired},
		{name: "XChainAccountCreateCount", style: innerRequired},
		{name: "Destination", style: innerRequired},
		{name: "SignatureReward", style: innerRequired},
	},
	"XChainClaimProofSig": {
		{name: "AttestationSignerAccount", style: innerRequired},
		{name: "PublicKey", style: innerRequired},
		{name: "Amount", style: innerRequired},
		{name: "AttestationRewardAccount", style: innerRequired},
		{name: "WasLockingChainSend", style: innerRequired},
		{name: "Destination", style: innerOptional},
	},
	"XChainCreateAccountProofSig": {
		{name: "AttestationSignerAccount", style: innerRequired},
		{name: "PublicKey", style: innerRequired},
		{name: "Amount", style: innerRequired},
		{name: "SignatureReward", style: innerRequired},
		{name: "AttestationRewardAccount", style: innerRequired},
		{name: "WasLockingChainSend", style: innerRequired},
		{name: "Destination", style: innerRequired},
	},
	"AuthAccount": {
		{name: "Account", style: innerRequired},
	},
	"PriceData": {
		{name: "BaseAsset", style: innerRequired},
		{name: "QuoteAsset", style: innerRequired},
		{name: "AssetPrice", style: innerOptional},
		{name: "Scale", style: innerDefault},
	},
	"Credential": {
		{name: "Issuer", style: innerRequired},
		{name: "CredentialType", style: innerRequired},
	},
	"Permission": {
		{name: "PermissionValue", style: innerRequired},
	},
	"BatchSigner": {
		{name: "Account", style: innerRequired},
		{name: "SigningPubKey", style: innerOptional},
		{name: "TxnSignature", style: innerOptional},
		{name: "Signers", style: innerOptional},
	},
	"Book": {
		{name: "BookDirectory", style: innerRequired},
		{name: "BookNode", style: innerRequired},
	},
	"CounterpartySignature": {
		{name: "SigningPubKey", style: innerOptional},
		{name: "TxnSignature", style: innerOptional},
		{name: "Signers", style: innerOptional},
	},
}

// MeetsInnerObjectTemplate reports whether an object satisfies the registered
// template for its SField. Fields without a registered template are valid.
func MeetsInnerObjectTemplate(fieldName string, values map[string]any) bool {
	fieldOrder := make([]string, 0, len(values))
	for name := range values {
		fieldOrder = append(fieldOrder, name)
	}
	return validateInnerObject(fieldName, values, fieldOrder) == nil
}

// CanonicalizeInnerObjectTemplate removes discardable fields and reports
// whether the remaining object satisfies its registered SField template.
// Fields without a registered template are left unchanged and are valid.
func CanonicalizeInnerObjectTemplate(fieldName string, values map[string]any) bool {
	if _, ok := innerObjectTemplates[fieldName]; !ok {
		return true
	}
	for name := range values {
		if isDiscardableInnerField(name) {
			delete(values, name)
		}
	}
	return MeetsInnerObjectTemplate(fieldName, values)
}

func validateInnerObject(fieldName string, values map[string]any, fieldOrder []string) error {
	template, ok := innerObjectTemplates[fieldName]
	if !ok {
		return nil
	}

	for _, field := range template {
		value, present := values[field.name]
		switch {
		case field.style == innerRequired && !present:
			return errors.New("Field '" + field.name + "' is required but missing.")
		case field.style == innerDefault && present && isDefaultInnerValue(value):
			return errors.New("Field '" + field.name + "' may not be explicitly set to default.")
		}
	}

	for _, name := range fieldOrder {
		if !innerObjectFieldAllowed(template, name) {
			return errors.New("Field '" + name + "' found in disallowed location.")
		}
	}
	return nil
}

func innerObjectFieldAllowed(template []innerObjectField, name string) bool {
	for _, field := range template {
		if field.name == name {
			return true
		}
	}
	return isDiscardableInnerField(name)
}

func isDiscardableInnerField(name string) bool {
	field, err := definitions.Get().FieldInstanceByName(name)
	return err == nil && field.Nth > 256
}

func isDefaultInnerValue(value any) bool {
	switch value := value.(type) {
	case int:
		return value == 0
	case int8:
		return value == 0
	case int16:
		return value == 0
	case int32:
		return value == 0
	case int64:
		return value == 0
	case uint:
		return value == 0
	case uint8:
		return value == 0
	case uint16:
		return value == 0
	case uint32:
		return value == 0
	case uint64:
		return value == 0
	case float64:
		return value == 0
	default:
		return false
	}
}

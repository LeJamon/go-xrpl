package types

import (
	"reflect"
	"testing"
)

func TestInnerObjectTemplateRegistry(t *testing.T) {
	expected := map[string][]innerObjectField{
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

	if !reflect.DeepEqual(innerObjectTemplates, expected) {
		t.Fatalf("inner object template registry mismatch:\n got: %#v\nwant: %#v", innerObjectTemplates, expected)
	}
}

func TestMeetsInnerObjectTemplate(t *testing.T) {
	valid := map[string]any{
		"Account":      "",
		"SignerWeight": 1,
	}
	if !MeetsInnerObjectTemplate("SignerEntry", valid) {
		t.Fatal("valid SignerEntry did not meet its template")
	}

	withDiscardable := map[string]any{
		"Account":      "",
		"SignerWeight": 1,
		"hash":         "",
	}
	if !MeetsInnerObjectTemplate("SignerEntry", withDiscardable) {
		t.Fatal("discardable field should be allowed by an inner object template")
	}

	withDisallowed := map[string]any{
		"Account":      "",
		"Amount":       "1",
		"SignerWeight": 1,
	}
	if MeetsInnerObjectTemplate("SignerEntry", withDisallowed) {
		t.Fatal("non-discardable field should not be allowed by an inner object template")
	}
}

func TestValidateInnerObjectTemplateSemantics(t *testing.T) {
	t.Run("missing required precedes disallowed field", func(t *testing.T) {
		err := validateInnerObject("SignerEntry", map[string]any{"Amount": "1"}, []string{"Amount"})
		if err == nil || err.Error() != "Field 'Account' is required but missing." {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	t.Run("required and optional defaults are accepted", func(t *testing.T) {
		values := map[string]any{
			"Account":       "",
			"SignerWeight":  0,
			"WalletLocator": "",
		}
		if err := validateInnerObject("SignerEntry", values, []string{"Account", "SignerWeight", "WalletLocator"}); err != nil {
			t.Fatalf("explicit required or optional default rejected: %v", err)
		}
	})

	t.Run("default style default is rejected", func(t *testing.T) {
		values := map[string]any{
			"BaseAsset":  "XRP",
			"QuoteAsset": "USD",
			"Scale":      uint8(0),
		}
		err := validateInnerObject("PriceData", values, []string{"BaseAsset", "QuoteAsset", "Scale"})
		if err == nil || err.Error() != "Field 'Scale' may not be explicitly set to default." {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})
}

package ledgerfields

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields/spec"
	"github.com/LeJamon/go-xrpl/protocol"
)

// This file drives one fully-populated fixture per typed ledger-entry type
// through the whole generated accessor surface — Decode, Encode, ToMap, Hash,
// every Emit* method, PreviousTxn — plus the Decode bounds-checking and
// unknown-field paths. Each fixture sets *every* field the type's spec
// declares, so the generated per-field decode arms and emit lines all run.
// TestGeneratedSLE_FixtureCompleteness pins the fixtures to spec.Specs so a
// new field or entry type can't silently slip past the round-trip guard.
//
// The two XChainOwned* entry types are intentionally excluded: they ship as
// registered stubs and are out of scope for this pass (issue #751).

const (
	fxAccount = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	fxIssuer  = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	fxHash256 = "1111111111111111111111111111111111111111111111111111111111111111"
	fxHashB   = "2222222222222222222222222222222222222222222222222222222222222222"
	fxCurrUSD = "0000000000000000000000005553440000000000"
	fxCurrEUR = "0000000000000000000000004555520000000000"
	fxHash128 = "00000000000000000000000000000001"
	fxHash192 = "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"
	fxBlob    = "DEADBEEF"
	fxXRP     = "1000000"
)

// outOfScopeXChain lists the registered-but-stubbed XChain ledger objects the
// coverage fixtures deliberately skip (issue #751: their accessors are out of
// scope and excluded from the ≥90% target).
var outOfScopeXChain = map[string]bool{
	"XChainOwnedClaimID":              true,
	"XChainOwnedCreateAccountClaimID": true,
}

// coverageFixtures maps a ledger-entry-type name to a canonical JSON map that
// populates every field the type carries. Values are codec-valid but
// otherwise arbitrary; the inner-object wrapper keys (SignerEntry, VoteEntry,
// …) mirror the production serializers under internal/ledger/state and
// internal/tx so binarycodec accepts them.
var coverageFixtures = map[string]map[string]any{
	"AccountRoot": {
		"Account":              fxAccount,
		"Balance":              fxXRP,
		"Sequence":             uint32(1),
		"OwnerCount":           uint32(2),
		"Flags":                uint32(0),
		"RegularKey":           fxIssuer,
		"Domain":               fxBlob,
		"EmailHash":            fxHash128,
		"MessageKey":           fxBlob,
		"TransferRate":         uint32(1000000005),
		"TickSize":             uint32(5),
		"NFTokenMinter":        fxIssuer,
		"MintedNFTokens":       uint32(3),
		"BurnedNFTokens":       uint32(1),
		"FirstNFTokenSequence": uint32(7),
		"AccountTxnID":         fxHash256,
		"WalletLocator":        fxHashB,
		"TicketCount":          uint32(2),
		"AMMID":                fxHash256,
		"VaultID":              fxHashB,
		"WalletSize":           uint32(4),
		"PreviousTxnID":        fxHash256,
		"PreviousTxnLgrSeq":    uint32(9),
	},
	"Offer": {
		"Account":           fxAccount,
		"Sequence":          uint32(7),
		"TakerPays":         map[string]any{"value": "100", "currency": "USD", "issuer": fxIssuer},
		"TakerGets":         fxXRP,
		"BookDirectory":     fxHash256,
		"BookNode":          "0",
		"OwnerNode":         "0",
		"Expiration":        uint32(500),
		"Flags":             uint32(0),
		"DomainID":          fxHashB,
		"AdditionalBooks":   []any{map[string]any{"Book": map[string]any{"BookDirectory": fxHash256, "BookNode": "0"}}},
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"DirectoryNode": {
		"Flags":             uint32(0),
		"RootIndex":         fxHash256,
		"Indexes":           []any{fxHash256, fxHashB},
		"IndexNext":         "0",
		"IndexPrevious":     "0",
		"Owner":             fxAccount,
		"TakerPaysCurrency": fxCurrUSD,
		"TakerPaysIssuer":   fxCurrEUR,
		"TakerGetsCurrency": fxCurrUSD,
		"TakerGetsIssuer":   fxCurrEUR,
		"ExchangeRate":      "5a",
		"NFTokenID":         fxHashB,
		"DomainID":          fxHash256,
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"RippleState": {
		"Flags":             uint32(0),
		"Balance":           map[string]any{"value": "10", "currency": "USD", "issuer": fxIssuer},
		"LowLimit":          map[string]any{"value": "0", "currency": "USD", "issuer": fxAccount},
		"HighLimit":         map[string]any{"value": "100", "currency": "USD", "issuer": fxIssuer},
		"LowNode":           "0",
		"HighNode":          "0",
		"LowQualityIn":      uint32(1),
		"LowQualityOut":     uint32(2),
		"HighQualityIn":     uint32(3),
		"HighQualityOut":    uint32(4),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"NFTokenOffer": {
		"Owner":             fxIssuer,
		"NFTokenID":         fxHash256,
		"Amount":            fxXRP,
		"OwnerNode":         "0",
		"NFTokenOfferNode":  "0",
		"Destination":       fxIssuer,
		"Expiration":        uint32(500),
		"Flags":             uint32(1),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Check": {
		"Account":           fxAccount,
		"Destination":       fxIssuer,
		"SendMax":           fxXRP,
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"DestinationNode":   "0",
		"Expiration":        uint32(500),
		"InvoiceID":         fxHash256,
		"SourceTag":         uint32(11),
		"DestinationTag":    uint32(22),
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"DID": {
		"Account":           fxAccount,
		"DIDDocument":       fxBlob,
		"URI":               fxBlob,
		"Data":              fxBlob,
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"NegativeUNL": {
		"Flags":               uint32(0),
		"DisabledValidators":  []any{map[string]any{"DisabledValidator": map[string]any{"PublicKey": fxBlob}}},
		"ValidatorToDisable":  fxBlob,
		"ValidatorToReEnable": fxBlob,
		"PreviousTxnID":       fxHash256,
		"PreviousTxnLgrSeq":   uint32(9),
	},
	"NFTokenPage": {
		"PreviousPageMin":   fxHash256,
		"NextPageMin":       fxHashB,
		"NFTokens":          []any{map[string]any{"NFToken": map[string]any{"NFTokenID": fxHash256}}},
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"SignerList": {
		"OwnerNode":    "0",
		"SignerQuorum": uint32(3),
		"SignerEntries": []any{map[string]any{
			"SignerEntry": map[string]any{"Account": fxIssuer, "SignerWeight": uint32(1)},
		}},
		"SignerListID":      uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Ticket": {
		"Account":           fxAccount,
		"OwnerNode":         "0",
		"TicketSequence":    uint32(5),
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Amendments": {
		"Flags":      uint32(0),
		"Amendments": []any{fxHash256, fxHashB},
		"Majorities": []any{map[string]any{
			"Majority": map[string]any{"Amendment": fxHash256, "CloseTime": uint32(100)},
		}},
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"LedgerHashes": {
		"FirstLedgerSequence": uint32(1),
		"LastLedgerSequence":  uint32(2),
		"Hashes":              []any{fxHash256, fxHashB},
		"Flags":               uint32(0),
	},
	"Bridge": {
		"Account":                fxAccount,
		"SignatureReward":        fxXRP,
		"MinAccountCreateAmount": fxXRP,
		"XChainBridge": map[string]any{
			"LockingChainDoor":  fxAccount,
			"LockingChainIssue": map[string]any{"currency": "XRP"},
			"IssuingChainDoor":  fxIssuer,
			"IssuingChainIssue": map[string]any{"currency": "USD", "issuer": fxIssuer},
		},
		"XChainClaimID":            "0",
		"XChainAccountCreateCount": "0",
		"XChainAccountClaimCount":  "0",
		"OwnerNode":                "0",
		"Flags":                    uint32(0),
		"PreviousTxnID":            fxHash256,
		"PreviousTxnLgrSeq":        uint32(9),
	},
	"DepositPreauth": {
		"Account":              fxAccount,
		"Authorize":            fxIssuer,
		"OwnerNode":            "0",
		"AuthorizeCredentials": []any{map[string]any{"Credential": map[string]any{"Issuer": fxIssuer, "CredentialType": fxBlob}}},
		"Flags":                uint32(0),
		"PreviousTxnID":        fxHash256,
		"PreviousTxnLgrSeq":    uint32(9),
	},
	"FeeSettings": {
		"BaseFee":               "a",
		"ReferenceFeeUnits":     uint32(10),
		"ReserveBase":           uint32(200),
		"ReserveIncrement":      uint32(50),
		"BaseFeeDrops":          fxXRP,
		"ReserveBaseDrops":      fxXRP,
		"ReserveIncrementDrops": fxXRP,
		"Flags":                 uint32(0),
		"PreviousTxnID":         fxHash256,
		"PreviousTxnLgrSeq":     uint32(9),
	},
	"Escrow": {
		"Account":           fxAccount,
		"Destination":       fxIssuer,
		"Amount":            fxXRP,
		"Condition":         fxBlob,
		"CancelAfter":       uint32(600),
		"FinishAfter":       uint32(500),
		"SourceTag":         uint32(11),
		"DestinationTag":    uint32(22),
		"OwnerNode":         "0",
		"DestinationNode":   "0",
		"TransferRate":      uint32(1000000005),
		"IssuerNode":        "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"PayChannel": {
		"Account":           fxAccount,
		"Destination":       fxIssuer,
		"Amount":            fxXRP,
		"Balance":           fxXRP,
		"PublicKey":         fxBlob,
		"SettleDelay":       uint32(60),
		"Expiration":        uint32(500),
		"CancelAfter":       uint32(600),
		"SourceTag":         uint32(11),
		"DestinationTag":    uint32(22),
		"OwnerNode":         "0",
		"DestinationNode":   "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"AMM": {
		"Account":    fxAccount,
		"TradingFee": uint32(500),
		"VoteSlots": []any{map[string]any{
			"VoteEntry": map[string]any{"Account": fxAccount, "TradingFee": uint32(500), "VoteWeight": uint32(1000)},
		}},
		"AuctionSlot": map[string]any{
			"Account":       fxAccount,
			"Expiration":    uint32(100),
			"DiscountedFee": uint32(10),
			"Price":         map[string]any{"value": "1000", "currency": "039C99CD9AB0B70B32ECDA51EAAE471625608EA2", "issuer": fxAccount},
			"AuthAccounts":  []any{map[string]any{"AuthAccount": map[string]any{"Account": fxIssuer}}},
		},
		"LPTokenBalance":    map[string]any{"value": "1000", "currency": "039C99CD9AB0B70B32ECDA51EAAE471625608EA2", "issuer": fxAccount},
		"Asset":             map[string]any{"currency": "XRP"},
		"Asset2":            map[string]any{"currency": "USD", "issuer": fxIssuer},
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"MPTokenIssuance": {
		"Issuer":            fxAccount,
		"Sequence":          uint32(1),
		"TransferFee":       uint32(500),
		"OwnerNode":         "0",
		"AssetScale":        uint32(2),
		"MaximumAmount":     "1000000000",
		"OutstandingAmount": "500000000",
		"LockedAmount":      "0",
		"MPTokenMetadata":   fxBlob,
		"DomainID":          fxHash256,
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"MPToken": {
		"Account":           fxAccount,
		"MPTokenIssuanceID": fxHash192,
		"MPTAmount":         "1000",
		"LockedAmount":      "0",
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Oracle": {
		"Owner":    fxAccount,
		"Provider": fxBlob,
		"PriceDataSeries": []any{map[string]any{
			"PriceData": map[string]any{"BaseAsset": fxCurrUSD, "QuoteAsset": fxCurrEUR, "AssetPrice": "64", "Scale": uint32(2)},
		}},
		"AssetClass":        fxBlob,
		"LastUpdateTime":    uint32(100),
		"URI":               fxBlob,
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Credential": {
		"Subject":           fxAccount,
		"Issuer":            fxIssuer,
		"CredentialType":    fxBlob,
		"Expiration":        uint32(500),
		"URI":               fxBlob,
		"IssuerNode":        "0",
		"SubjectNode":       "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"PermissionedDomain": {
		"Owner":               fxAccount,
		"Sequence":            uint32(1),
		"AcceptedCredentials": []any{map[string]any{"Credential": map[string]any{"Issuer": fxIssuer, "CredentialType": fxBlob}}},
		"OwnerNode":           "0",
		"Flags":               uint32(0),
		"PreviousTxnID":       fxHash256,
		"PreviousTxnLgrSeq":   uint32(9),
	},
	"Delegate": {
		"Account":           fxAccount,
		"Authorize":         fxIssuer,
		"Permissions":       []any{map[string]any{"Permission": map[string]any{"PermissionValue": uint32(1)}}},
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
	"Vault": {
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             fxAccount,
		"Account":           fxIssuer,
		"Data":              fxBlob,
		"Asset":             map[string]any{"currency": "XRP"},
		"AssetsTotal":       "1000",
		"AssetsAvailable":   "500",
		"AssetsMaximum":     "10000",
		"LossUnrealized":    "0",
		"ShareMPTID":        fxHash192,
		"WithdrawalPolicy":  uint32(1),
		"Flags":             uint32(0),
		"PreviousTxnID":     fxHash256,
		"PreviousTxnLgrSeq": uint32(9),
	},
}

// encodeIface is the typed-Encode contract every generated entry satisfies.
type encodeIface interface{ Encode() ([]byte, error) }

// hashIface is the typed-Hash contract every generated entry satisfies.
type hashIface interface {
	Hash(index [32]byte) ([32]byte, error)
}

// toMapIface is the canonical-map contract every generated entry satisfies.
type toMapIface interface{ ToMap() map[string]any }

func TestGeneratedSLE_RoundTripAndAccessors(t *testing.T) {
	for name, fixture := range coverageFixtures {
		t.Run(name, func(t *testing.T) {
			// Real SLE blobs carry LedgerEntryType, and ToMap re-emits it; add
			// it here so the canonical bytes line up with the typed Encode.
			input := make(map[string]any, len(fixture)+1)
			for k, v := range fixture {
				input[k] = v
			}
			input["LedgerEntryType"] = name
			canonical, err := binarycodec.EncodeBytes(input)
			if err != nil {
				t.Fatalf("EncodeBytes: %v", err)
			}

			cur := New(name)
			if cur == nil {
				t.Fatalf("New(%q) returned nil", name)
			}
			if err := cur.Decode(canonical); err != nil {
				t.Fatalf("Decode: %v", err)
			}

			// Round-trip: typed Encode must reproduce the canonical bytes.
			got, err := cur.(encodeIface).Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(got, canonical) {
				t.Fatalf("round-trip mismatch:\n canonical %x\n encoded   %x", canonical, got)
			}

			// ToMap must echo every field the spec declares.
			m := cur.(toMapIface).ToMap()
			for _, f := range specFieldNames(name) {
				if _, present := m[f]; !present {
					t.Errorf("ToMap missing spec field %q", f)
				}
			}

			// Exercise every emit path with three prev shapes: identical
			// values (no-op diff), an empty entry (every field "added"), and a
			// mismatched concrete type (the type-assertion guard returns).
			prevEqual := New(name)
			if err := prevEqual.Decode(canonical); err != nil {
				t.Fatalf("Decode prev: %v", err)
			}
			prevEmpty := New(name)
			prevWrong := New("AccountRoot")
			if name == "AccountRoot" {
				prevWrong = New("Offer")
			}

			cur.EmitNewFields(map[string]any{})
			cur.EmitFinalFields(map[string]any{})
			cur.EmitDeleteFinalFields(map[string]any{})
			cur.EmitChangeOrigFields(map[string]any{})
			cur.EmitPreviousFields(prevEqual, map[string]any{})
			cur.EmitPreviousFields(prevEmpty, map[string]any{})
			cur.EmitPreviousFields(prevWrong, map[string]any{})
			cur.EmitDeletePreviousFields(prevEqual, map[string]any{})
			cur.PreviousTxn()

			// Hash must equal sha512Half(HashPrefixLeafNode || data || index).
			var index [32]byte
			for i := range index {
				index[i] = byte(i + 1)
			}
			h, err := cur.(hashIface).Hash(index)
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			want := common.Sha512Half(protocol.HashPrefixLeafNode[:], canonical, index[:])
			if h != want {
				t.Fatalf("hash mismatch:\n want %x\n got  %x", want, h)
			}

			// Bounds-checking: decoding every truncation must never panic.
			for cut := 0; cut < len(canonical); cut++ {
				_ = New(name).Decode(canonical[:cut])
			}

			// Typed metadata refuses to silently drop an unrecognised field:
			// a trailing UInt32 with a bogus ordinal must trip ErrUnknownField.
			unknown := append(append([]byte{}, canonical...), 0x20, 0xFF, 0x00, 0x00, 0x00, 0x00)
			err = New(name).Decode(unknown)
			if err == nil {
				t.Errorf("Decode accepted unknown field, want ErrUnknownField")
			} else if _, ok := err.(*ErrUnknownField); !ok {
				t.Errorf("Decode unknown field: got %v, want *ErrUnknownField", err)
			}
		})
	}
}

// TestGeneratedSLE_FixtureCompleteness keeps coverageFixtures in lockstep with
// spec.Specs: every in-scope entry type needs a fixture, every fixture must set
// every field the type declares, and no fixture may reference a type the spec
// no longer carries. This is what stops the round-trip coverage from silently
// going stale when a field or entry type is added to the spec.
func TestGeneratedSLE_FixtureCompleteness(t *testing.T) {
	for _, entry := range spec.Specs {
		if outOfScopeXChain[entry.Name] {
			if !HasTyped(entry.Name) {
				t.Errorf("%q listed out-of-scope but is not registered", entry.Name)
			}
			if _, ok := coverageFixtures[entry.Name]; ok {
				t.Errorf("%q is out-of-scope XChain but has a coverage fixture", entry.Name)
			}
			continue
		}
		fixture, ok := coverageFixtures[entry.Name]
		if !ok {
			t.Errorf("no coverage fixture for ledger type %q; add one to coverageFixtures", entry.Name)
			continue
		}
		for _, f := range entry.Fields {
			if f.DecodeOnly {
				// DecodeOnly fields appear only on legacy blobs; a canonical
				// coverage fixture never carries them.
				continue
			}
			if _, set := fixture[f.Name]; !set {
				t.Errorf("coverage fixture %q is missing field %q", entry.Name, f.Name)
			}
		}
	}

	known := make(map[string]bool, len(spec.Specs))
	for _, e := range spec.Specs {
		known[e.Name] = true
	}
	for name := range coverageFixtures {
		if !known[name] {
			t.Errorf("coverage fixture %q has no matching spec.Specs entry", name)
		}
	}
}

// specFieldNames returns the declared field names for a ledger-entry type, or
// nil if the spec doesn't list it.
func specFieldNames(name string) []string {
	for _, e := range spec.Specs {
		if e.Name != name {
			continue
		}
		var out []string
		for _, f := range e.Fields {
			if f.DecodeOnly {
				// DecodeOnly fields are never carried on the struct or echoed
				// by ToMap, so exclude them from the declared-field set.
				continue
			}
			out = append(out, f.Name)
		}
		return out
	}
	return nil
}

package replaytool

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	entryschema "github.com/LeJamon/go-xrpl/ledger/entry/schema"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Types whose threading is conditional on fixPreviousTxnID, mirroring
// internal/tx/applystate.conditionalThreadingTypes.
var conditionalThreadingTypes = map[string]bool{
	"DirectoryNode": true,
	"Amendments":    true,
	"FeeSettings":   true,
	"NegativeUNL":   true,
	"AMM":           true,
}

var threadedEntryTypes = func() map[string]bool {
	result := make(map[string]bool, len(entryschema.Specs))
	for _, spec := range entryschema.Specs {
		for _, field := range spec.Fields {
			if field.Name == "PreviousTxnID" {
				result[spec.Name] = true
				break
			}
		}
	}
	return result
}()

// isThreadedType reports whether an entry type carries threaded
// PreviousTxnID/PreviousTxnLgrSeq fields. It mirrors
// internal/tx/applystate.isThreadedType.
func isThreadedType(entryType string, rules *amendment.Rules) bool {
	if conditionalThreadingTypes[entryType] {
		return rules.Enabled(amendment.FeatureFixPreviousTxnID)
	}
	return threadedEntryTypes[entryType]
}

// createdField is one ledger-entry field whose STObject default (a
// "zero" that rippled's isDefault() reports true) is omitted from a CreatedNode's
// NewFields, even though the real serialized SLE carries it. Value is the form
// binarycodec.Encode accepts for that zero, identical to the bytes
// binarycodec.Decode would have produced had the field been present.
type createdField struct {
	Name  string
	Value any
}

// requiredDefaults lists, per ledger-entry type, the soeREQUIRED fields rippled
// omits from NewFields when they sit at their STObject default. After copying a
// CreatedNode's NewFields we re-add any of these the metadata did not carry, so
// the re-encoded SLE matches mainnet byte-for-byte.
//
// A field belongs here iff it is soeREQUIRED for the type AND its default is a
// zero that isDefault() reports true (UInt = 0, native XRP Amount = 0 drops,
// Hash256 = zero, empty array) AND it is metadata-eligible (rippled only drops
// default fields that would otherwise be emitted into NewFields). sfFlags is
// soeREQUIRED on every type (a common field), so every type carries Flags: 0.
// Fields that are never at default-zero on creation (Account, ordinary account
// Sequence and non-native Balance, BookDirectory, RootIndex, ...) are excluded,
// as are
// soeOPTIONAL/soeDEFAULT fields, the never-in-metadata fields PreviousTxnID/Seq
// (handled by threading) and Indexes, and LedgerEntryType (carried at the node
// level).
//
// Representations: UInt32 -> int(0); UInt64 -> "0" (lowercase hex, no leading
// zeros, == binarycodec UInt64.ToJSON); native Amount -> "0" (drops); Issue ->
// {"currency":"XRP"}.
//
// DirectoryNode deliberately carries only Flags: its Indexes (sMD_Never) never
// appears in metadata and is reconstructed from object membership instead (see
// replay_reconstruct_dir.go); RootIndex (sMD_Always) is always already present;
// the soeOPTIONAL book fields a book directory drops are restored by
// fillBookDirectoryDefaults.
var requiredDefaults = map[string][]createdField{
	"AccountRoot": {
		{Name: "Sequence", Value: 0},
		{Name: "Balance", Value: "0"},
		{Name: "Flags", Value: 0},
		{Name: "OwnerCount", Value: 0},
	},
	"Offer": {
		{Name: "Flags", Value: 0},
		{Name: "BookNode", Value: "0"},
		{Name: "OwnerNode", Value: "0"},
	},
	"DirectoryNode": {
		{Name: "Flags", Value: 0},
	},
	"RippleState": {
		{Name: "Flags", Value: 0},
	},
	"NFTokenOffer": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "NFTokenOfferNode", Value: "0"},
	},
	"Check": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "DestinationNode", Value: "0"},
	},
	"DID": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"NegativeUNL": {
		{Name: "Flags", Value: 0},
	},
	"NFTokenPage": {
		{Name: "Flags", Value: 0},
	},
	"SignerList": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "SignerListID", Value: 0},
	},
	"Ticket": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"Amendments": {
		{Name: "Flags", Value: 0},
	},
	"LedgerHashes": {
		{Name: "Flags", Value: 0},
	},
	"Bridge": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "XChainClaimID", Value: "0"},
		{Name: "XChainAccountCreateCount", Value: "0"},
		{Name: "XChainAccountClaimCount", Value: "0"},
	},
	"DepositPreauth": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"XChainOwnedClaimID": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"FeeSettings": {
		{Name: "Flags", Value: 0},
	},
	"XChainOwnedCreateAccountClaimID": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "XChainAccountCreateCount", Value: "0"},
	},
	"Escrow": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"PayChannel": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "Balance", Value: "0"},
	},
	"Loan": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "LoanBrokerNode", Value: "0"},
	},
	"LoanBroker": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "VaultNode", Value: "0"},
	},
	"AMM": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "Asset", Value: map[string]any{"currency": "XRP"}},
		{Name: "Asset2", Value: map[string]any{"currency": "XRP"}},
	},
	"MPTokenIssuance": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "OutstandingAmount", Value: "0"},
	},
	"MPToken": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"Oracle": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"Credential": {
		{Name: "Flags", Value: 0},
		{Name: "IssuerNode", Value: "0"},
	},
	"PermissionedDomain": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"Delegate": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
	},
	"Vault": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
		{Name: "Asset", Value: map[string]any{"currency": "XRP"}},
	},
}

// explicitlyCreatedDefaults lists optional fields that the sole creation path
// always writes, including when their value is zero. CreatedNode.NewFields drops
// those zero values, but the canonical SLE retains the explicitly present field.
var explicitlyCreatedDefaults = map[string][]createdField{
	"RippleState": {
		{Name: "LowNode", Value: "0"},
		{Name: "HighNode", Value: "0"},
	},
	"Delegate": {
		{Name: "DestinationNode", Value: "0"},
	},
}

func fillCreatedDefaults(obj map[string]any, entryType string) error {
	if entryType == "Credential" {
		if err := fillCredentialDefaults(obj); err != nil {
			return err
		}
	}
	for _, f := range requiredDefaults[entryType] {
		if _, present := obj[f.Name]; !present {
			obj[f.Name] = f.Value
		}
	}
	for _, f := range explicitlyCreatedDefaults[entryType] {
		if _, present := obj[f.Name]; !present {
			obj[f.Name] = f.Value
		}
	}
	if entryType == "Escrow" {
		fillEscrowDirectoryDefaults(obj)
	}
	return nil
}

func fillCredentialDefaults(obj map[string]any) error {
	issuer, issuerPresent := metaAccountID(obj, "Issuer")
	if !issuerPresent {
		return fmt.Errorf("Credential has invalid Issuer")
	}
	subject, subjectPresent := metaAccountID(obj, "Subject")
	if !subjectPresent {
		return fmt.Errorf("Credential has invalid Subject")
	}
	if issuer != subject {
		if _, present := obj["SubjectNode"]; !present {
			obj["SubjectNode"] = "0"
		}
		return nil
	}
	if _, present := obj["SubjectNode"]; present {
		return fmt.Errorf("self-issued Credential has SubjectNode")
	}
	flags, present := metaUint32(obj["Flags"])
	if !present || flags != ledgerentry.LsfAccepted {
		return fmt.Errorf("self-issued Credential has invalid Flags")
	}
	return nil
}

func metaUint32(value any) (uint32, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 || v > float64(^uint32(0)) || v != float64(uint32(v)) {
			return 0, false
		}
		return uint32(v), true
	case uint32:
		return v, true
	case int:
		if v < 0 || uint64(v) > uint64(^uint32(0)) {
			return 0, false
		}
		return uint32(v), true
	default:
		return 0, false
	}
}

func fillEscrowDirectoryDefaults(obj map[string]any) {
	account, hasAccount := metaAccountID(obj, "Account")
	destination, hasDestination := metaAccountID(obj, "Destination")
	if !hasAccount || !hasDestination {
		return
	}
	if account != destination {
		if _, present := obj["DestinationNode"]; !present {
			obj["DestinationNode"] = "0"
		}
	}

	issuer, hasIssuer := metaIssuer(obj, "Amount")
	if hasIssuer && issuer != account && issuer != destination {
		if _, present := obj["IssuerNode"]; !present {
			obj["IssuerNode"] = "0"
		}
	}
}

// zeroHash160 is the JSON form binarycodec produces for an all-zero Hash160: the
// XRP side of an order book, whose Currency and Issuer are both zero.
const zeroHash160 = "0000000000000000000000000000000000000000"

// fillBookDirectoryDefaults restores the all-zero XRP-side book fields rippled
// drops from a created book directory's NewFields. These four fields are
// soeOPTIONAL, so requiredDefaults cannot carry them, yet rippled still omits
// whichever Currency/Issuer pair is all-zero (the XRP side of the book) because
// isDefault() reports a zero Hash160 as default — while the real SLE always
// serializes the full pair for both sides. A DirectoryNode is a book (not an
// owner) directory iff it carries an ExchangeRate; owner directories never have
// these fields and are left untouched.
func fillBookDirectoryDefaults(obj map[string]any, entryType string) error {
	if entryType != "DirectoryNode" {
		return nil
	}
	if _, isBook := obj["ExchangeRate"]; !isBook {
		return nil
	}
	if err := fillBookSideDefaults(obj, "TakerPaysMPT", "TakerPaysCurrency", "TakerPaysIssuer"); err != nil {
		return err
	}
	return fillBookSideDefaults(obj, "TakerGetsMPT", "TakerGetsCurrency", "TakerGetsIssuer")
}

func fillBookSideDefaults(obj map[string]any, mptField, currencyField, issuerField string) error {
	_, hasMPT := obj[mptField]
	_, hasCurrency := obj[currencyField]
	_, hasIssuer := obj[issuerField]
	if hasMPT {
		if hasCurrency || hasIssuer {
			return fmt.Errorf("book directory side has both %s and Issue fields", mptField)
		}
		return nil
	}
	if !hasCurrency {
		obj[currencyField] = zeroHash160
	}
	if !hasIssuer {
		obj[issuerField] = zeroHash160
	}
	return nil
}

// threadPreviousTxn stamps the threaded PreviousTxnID/PreviousTxnLgrSeq onto obj
// for threaded entry types, mirroring what ApplyStateTable writes to the state
// SLE: the current transaction's hash and ledger sequence. These fields never
// appear in CreatedNode NewFields or ModifiedNode FinalFields (sMD_DeleteFinal),
// so they must be supplied here for the reconstructed SLE to match mainnet.
func threadPreviousTxn(obj map[string]any, entryType string, txHash [32]byte, ledgerSeq uint32, rules *amendment.Rules) {
	if !isThreadedType(entryType, rules) {
		return
	}
	obj["PreviousTxnID"] = protocol.Hash256Hex(txHash)
	obj["PreviousTxnLgrSeq"] = ledgerSeq
}

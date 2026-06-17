package replaytool

import (
	"encoding/hex"
	"strings"
)

// fixPreviousTxnIDEnabled reflects that this tool reconstructs recent mainnet
// ledgers, where fixPreviousTxnID has long been active. It gates whether the
// directory/singleton types listed in conditionalThreadingTypes carry threaded
// PreviousTxnID/PreviousTxnLgrSeq fields. Replaying a pre-amendment ledger with
// this true would over-thread those types; that only degrades the reconstruction
// (account_hash will not verify), it never masks a real divergence.
const fixPreviousTxnIDEnabled = true

// Types whose threading is conditional on fixPreviousTxnID, mirroring
// internal/tx/applystate.conditionalThreadingTypes.
var conditionalThreadingTypes = map[string]bool{
	"DirectoryNode": true,
	"Amendments":    true,
	"FeeSettings":   true,
	"NegativeUNL":   true,
	"AMM":           true,
}

// nonThreadedTypes never carry PreviousTxnID, mirroring
// internal/tx/applystate.nonThreadedTypes. LedgerHashes is the only entry type
// whose format lacks the field; a new field-less type must be added here.
var nonThreadedTypes = map[string]bool{
	"LedgerHashes": true,
}

// isThreadedType reports whether an entry type carries threaded
// PreviousTxnID/PreviousTxnLgrSeq fields. It mirrors
// internal/tx/applystate.isThreadedType.
func isThreadedType(entryType string) bool {
	if nonThreadedTypes[entryType] {
		return false
	}
	if conditionalThreadingTypes[entryType] {
		return fixPreviousTxnIDEnabled
	}
	return true
}

// requiredField is one soeREQUIRED ledger-entry field whose STObject default (a
// "zero" that rippled's isDefault() reports true) is omitted from a CreatedNode's
// NewFields, even though the real serialized SLE always carries it. Value is the
// form binarycodec.Encode accepts for that zero, identical to the bytes
// binarycodec.Decode would have produced had the field been present.
type requiredField struct {
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
// Fields that are never at default-zero on creation (Account, Sequence,
// BookDirectory, RootIndex, non-native Balance, ...) are excluded, as are
// soeOPTIONAL/soeDEFAULT fields, the never-in-metadata fields PreviousTxnID/Seq
// (handled by threading) and Indexes, and LedgerEntryType (carried at the node
// level).
//
// Representations: UInt32 -> int(0); UInt64 -> "0" (lowercase hex, no leading
// zeros, == binarycodec UInt64.ToJSON); native Amount -> "0" (drops).
//
// DirectoryNode deliberately carries only Flags: its Indexes (sMD_Never) never
// appears in metadata and is reconstructed from object membership instead (see
// replay_reconstruct_dir.go); RootIndex (sMD_Always) is always already present.
var requiredDefaults = map[string][]requiredField{
	"AccountRoot": {
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
	"AMM": {
		{Name: "Flags", Value: 0},
		{Name: "OwnerNode", Value: "0"},
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
		{Name: "SubjectNode", Value: "0"},
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
	},
}

// fillRequiredDefaults adds every soeREQUIRED default-zero field for entryType
// that obj does not already carry. A soeREQUIRED field absent from a CreatedNode's
// NewFields is, by rippled's rule, at its default — so re-adding the default zero
// is exact.
func fillRequiredDefaults(obj map[string]any, entryType string) {
	for _, f := range requiredDefaults[entryType] {
		if _, present := obj[f.Name]; !present {
			obj[f.Name] = f.Value
		}
	}
}

// threadPreviousTxn stamps the threaded PreviousTxnID/PreviousTxnLgrSeq onto obj
// for threaded entry types, mirroring what ApplyStateTable writes to the state
// SLE: the current transaction's hash and ledger sequence. These fields never
// appear in CreatedNode NewFields or ModifiedNode FinalFields (sMD_DeleteFinal),
// so they must be supplied here for the reconstructed SLE to match mainnet.
func threadPreviousTxn(obj map[string]any, entryType string, txHash [32]byte, ledgerSeq uint32) {
	if !isThreadedType(entryType) {
		return
	}
	obj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(txHash[:]))
	obj["PreviousTxnLgrSeq"] = ledgerSeq
}
